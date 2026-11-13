package usage

import (
	"cmp"
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/kinako404/conloom/internal/obs"
	"github.com/kinako404/conloom/internal/store"
)

const writeTimeout = 10 * time.Second

// Sink stores batches of model calls; *sqlite.DB is one.
type Sink interface {
	InsertModelCalls(ctx context.Context, calls []store.ModelCall) error
}

// RecorderOptions tune a Recorder. Zero values use the defaults; nil Metrics
// count nothing and a nil Logger discards.
type RecorderOptions struct {
	Buffer  int           // default 4096
	Batch   int           // default 200
	Flush   time.Duration // default 2s
	Metrics *obs.Metrics
	Logger  *slog.Logger
}

// Recorder queues model calls and writes them to its Sink in the background,
// a batch at a time: when Batch calls are queued, and every Flush otherwise.
// A failed write is logged, counted (UsageWriteErrors) and its calls lost.
type Recorder struct {
	sink    Sink
	o       RecorderOptions
	ch      chan store.ModelCall
	closing chan context.Context
	done    chan struct{}

	mu     sync.RWMutex
	closed bool
	once   sync.Once
}

// NewRecorder returns a Recorder writing to sink and starts its writer.
func NewRecorder(sink Sink, o RecorderOptions) *Recorder {
	o.Buffer = cmp.Or(o.Buffer, 4096)
	o.Batch = cmp.Or(o.Batch, 200)
	o.Flush = cmp.Or(o.Flush, 2*time.Second)
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	r := &Recorder{sink: sink, o: o, ch: make(chan store.ModelCall, o.Buffer),
		closing: make(chan context.Context, 1), done: make(chan struct{})}
	go r.write()
	return r
}

// Record queues c without blocking. When the queue is full, or after Close,
// c is dropped and counted (UsageDropped). A nil Recorder records nothing.
func (r *Recorder) Record(c store.ModelCall) {
	if r == nil {
		return
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !r.closed {
		select {
		case r.ch <- c:
			return
		default:
		}
	}
	if r.o.Metrics != nil {
		r.o.Metrics.UsageDropped.Inc()
	}
}

// Close stops accepting calls and writes the queued ones, giving up when ctx
// is done (it then returns ctx's error). Later calls only wait, as long as
// their ctx allows, for that write to end.
func (r *Recorder) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.closing <- ctx
	})
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Recorder) write() {
	defer close(r.done)
	t := time.NewTicker(r.o.Flush)
	defer t.Stop()
	var batch []store.ModelCall
	flush := func(ctx context.Context) {
		if len(batch) > 0 {
			r.flush(ctx, batch)
			batch = nil
		}
	}
	for {
		select {
		case c := <-r.ch:
			if batch = append(batch, c); len(batch) >= r.o.Batch {
				flush(context.Background())
			}
		case <-t.C:
			flush(context.Background())
		case ctx := <-r.closing:
			for ctx.Err() == nil {
				select {
				case c := <-r.ch:
					if batch = append(batch, c); len(batch) >= r.o.Batch {
						flush(ctx)
					}
				default:
					flush(ctx)
					return
				}
			}
			return
		}
	}
}

func (r *Recorder) flush(ctx context.Context, batch []store.ModelCall) {
	ctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := r.sink.InsertModelCalls(ctx, batch); err != nil {
		if r.o.Metrics != nil {
			r.o.Metrics.UsageWriteErrors.Inc()
		}
		r.o.Logger.Warn("write model calls failed", "calls", len(batch), "error", err)
	}
}
