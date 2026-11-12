package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

const (
	// wsReadLimit allows group messages with images or forwarded content.
	wsReadLimit        = 4 << 20
	eventBufferSize    = 256
	pingInterval       = 30 * time.Second
	defaultCallTimeout = 10 * time.Second
)

// dialTimeout bounds the TCP connect and WebSocket upgrade.
var dialTimeout = 10 * time.Second

// Conn is a OneBot 11 forward WebSocket connection. Action responses are
// matched to Call by their echo; everything else is delivered on Events.
type Conn struct {
	ws      *websocket.Conn
	events  chan []byte
	done    chan struct{}
	err     error
	dropped atomic.Int64
	seq     atomic.Uint64

	mu      sync.Mutex
	pending map[string]chan obResponse

	closeOnce sync.Once
	closeErr  error
}

type obResponse struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
	Echo    json.RawMessage `json:"echo"`
}

// errNotSent marks a Call error returned before the request reached the
// connection, so the server cannot have acted on it.
var errNotSent = errors.New("request not sent")

// callError is a response with status other than "ok" or a non-zero retcode.
type callError struct {
	action, status   string
	retCode          int
	message, wording string
}

func (e *callError) Error() string {
	return fmt.Sprintf("napcat: %s failed: status=%s retcode=%d message=%q wording=%q",
		e.action, e.status, e.retCode, e.message, e.wording)
}

// Dial connects to the OneBot server at rawURL, giving up after 10s. A
// non-empty token is sent both as the access_token query parameter and as a
// bearer Authorization header.
func Dial(ctx context.Context, rawURL, token string) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("napcat: invalid ws_url: %w", err)
	}
	header := http.Header{}
	if token != "" {
		q := u.Query()
		q.Set("access_token", token)
		u.RawQuery = q.Encode()
		header.Set("Authorization", "Bearer "+token)
	}
	dctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	ws, resp, err := websocket.Dial(dctx, u.String(), &websocket.DialOptions{HTTPHeader: header})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, redactError(fmt.Errorf("napcat: dial %s: %w", redactURL(rawURL), err), token)
	}
	ws.SetReadLimit(wsReadLimit)

	c := &Conn{
		ws:      ws,
		events:  make(chan []byte, eventBufferSize),
		done:    make(chan struct{}),
		pending: map[string]chan obResponse{},
	}
	go c.readLoop()
	go c.pingLoop()
	return c, nil
}

// redactURL drops the query and user info, which may hold credentials.
func redactURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid url>"
	}
	u.RawQuery, u.User = "", nil
	return u.String()
}

// redactError removes token from err's message.
func redactError(err error, token string) error {
	if token == "" {
		return err
	}
	msg := err.Error()
	redacted := strings.NewReplacer(token, "REDACTED", url.QueryEscape(token), "REDACTED").Replace(msg)
	if redacted == msg {
		return err
	}
	return errors.New(redacted)
}

func (c *Conn) readLoop() {
	var err error
	defer func() {
		c.err = err
		close(c.events)
		close(c.done)
		_ = c.ws.CloseNow()
	}()
	for {
		var data []byte
		_, data, err = c.ws.Read(context.Background())
		if err != nil {
			return
		}
		var probe struct {
			Echo json.RawMessage `json:"echo"`
		}
		if json.Unmarshal(data, &probe) == nil && len(probe.Echo) > 0 && !bytes.Equal(probe.Echo, []byte("null")) {
			c.deliver(data)
			continue
		}
		select {
		case c.events <- data:
		default:
			c.dropped.Add(1)
		}
	}
}

func (c *Conn) deliver(data []byte) {
	var resp obResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return
	}
	echo := string(resp.Echo)
	var s string
	if json.Unmarshal(resp.Echo, &s) == nil {
		echo = s
	}
	c.mu.Lock()
	ch, ok := c.pending[echo]
	delete(c.pending, echo)
	c.mu.Unlock()
	if ok {
		ch <- resp
	}
}

func (c *Conn) pingLoop() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), defaultCallTimeout)
		err := c.ws.Ping(ctx)
		cancel()
		if err != nil {
			_ = c.ws.CloseNow()
			return
		}
	}
}

// Call sends an action and waits for its response, by default for at most
// 10s. It returns the response data, or an error if the connection fails, the
// wait ends, or the server reports a failure. Errors wrapping errNotSent mean
// the request was never written; after any other error the server may have
// acted on it.
func (c *Conn) Call(ctx context.Context, action string, params any) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
	if params == nil {
		params = map[string]any{}
	}
	echo := strconv.FormatUint(c.seq.Add(1), 10)
	req, err := json.Marshal(map[string]any{"action": action, "params": params, "echo": echo})
	if err != nil {
		return nil, fmt.Errorf("napcat: encode %s: %w", action, err)
	}

	ch := make(chan obResponse, 1)
	c.mu.Lock()
	c.pending[echo] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, echo)
		c.mu.Unlock()
	}()

	select {
	case <-c.done:
		return nil, fmt.Errorf("napcat: %s: %w: connection closed", action, errNotSent)
	default:
	}
	if err := c.ws.Write(ctx, websocket.MessageText, req); err != nil {
		return nil, fmt.Errorf("napcat: send %s: %w: %w", action, errNotSent, err)
	}
	var resp obResponse
	select {
	case resp = <-ch:
	case <-c.done:
		// The read loop hands over a response before it closes done.
		select {
		case resp = <-ch:
		default:
			return nil, fmt.Errorf("napcat: %s: connection closed before the response", action)
		}
	case <-ctx.Done():
		select {
		case resp = <-ch:
		default:
			return nil, fmt.Errorf("napcat: %s: no response: %w", action, ctx.Err())
		}
	}
	if resp.Status != "ok" || resp.RetCode != 0 {
		return nil, &callError{action: action, status: resp.Status, retCode: resp.RetCode, message: resp.Message, wording: resp.Wording}
	}
	return resp.Data, nil
}

// Events delivers pushed messages (events, meta events) in arrival order. When
// the buffer of 256 is full new messages are dropped and counted. The channel
// is closed after the connection ends.
func (c *Conn) Events() <-chan []byte { return c.events }

// Done is closed when the connection has ended.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err returns why the connection ended, once Done is closed.
func (c *Conn) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// Dropped returns the number of pushed messages dropped because Events was full.
func (c *Conn) Dropped() int64 { return c.dropped.Load() }

// Close closes the connection with a normal closure and waits for the read
// loop to end. Closing an already ended connection returns nil.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		err := c.ws.Close(websocket.StatusNormalClosure, "")
		if errors.Is(err, net.ErrClosed) {
			err = nil
		}
		c.closeErr = err
	})
	<-c.done
	return c.closeErr
}
