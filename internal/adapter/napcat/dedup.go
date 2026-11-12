package napcat

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Dedup is a local, append-only record of outbox actions already sent to the
// platform, so a re-delivered action is ACKed instead of being sent twice.
// Each line is "action_id\t<unix seconds>\t<platform_message_id>".
type Dedup struct {
	mu   sync.Mutex
	f    dedupFile
	size int64
	now  func() time.Time
	sent map[string]string
}

// maxDedupLine is the longest line loaded from the log; longer lines are
// skipped as malformed.
const maxDedupLine = 1 << 20

// dedupFile is the append handle of the log; tests substitute a failing one.
type dedupFile interface {
	Write(p []byte) (int, error)
	Sync() error
	Truncate(size int64) error
	Close() error
}

// syncDir fsyncs dir so a rename inside it survives power loss. Windows
// cannot fsync a directory and is skipped.
var syncDir = func(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}

type dedupEntry struct {
	actionID, platformMsgID string
	at                      int64
}

// OpenDedup loads the log at path, drops entries older than retention and
// rewrites the file with the remaining entries before appending to it.
func OpenDedup(path string, retention time.Duration, now func() time.Time) (*Dedup, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("napcat: dedup dir: %w", err)
	}
	entries, err := loadDedup(path)
	if err != nil {
		return nil, err
	}

	cutoff := now().Add(-retention)
	sent := make(map[string]string, len(entries))
	var kept []dedupEntry
	for _, e := range entries {
		if time.Unix(e.at, 0).Before(cutoff) {
			continue
		}
		kept = append(kept, e)
		sent[e.actionID] = e.platformMsgID
	}
	if err := rewriteDedup(path, kept); err != nil {
		return nil, err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("napcat: open dedup log: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("napcat: open dedup log: %w", err)
	}
	return &Dedup{f: f, size: st.Size(), now: now, sent: sent}, nil
}

// loadDedup reads the log, skipping malformed lines and lines longer than
// maxDedupLine.
func loadDedup(path string) ([]dedupEntry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("napcat: read dedup log: %w", err)
	}
	defer f.Close()

	var entries []dedupEntry
	r := bufio.NewReaderSize(f, maxDedupLine)
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
		} else if e, ok := parseDedupLine(line); ok {
			entries = append(entries, e)
		}
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, fmt.Errorf("napcat: read dedup log: %w", err)
		}
	}
}

func parseDedupLine(line []byte) (dedupEntry, bool) {
	parts := strings.SplitN(strings.TrimRight(string(line), "\r\n"), "\t", 3)
	if len(parts) != 3 || parts[0] == "" {
		return dedupEntry{}, false
	}
	at, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return dedupEntry{}, false
	}
	return dedupEntry{actionID: parts[0], at: at, platformMsgID: parts[2]}, true
}

func rewriteDedup(path string, entries []dedupEntry) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("napcat: compact dedup log: %w", err)
	}
	defer os.Remove(tmp.Name())

	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		w.WriteString(formatDedupLine(e.actionID, e.at, e.platformMsgID))
	}
	err = w.Flush()
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), path)
	}
	if err == nil {
		err = syncDir(filepath.Dir(path))
	}
	if err != nil {
		return fmt.Errorf("napcat: compact dedup log: %w", err)
	}
	return nil
}

func formatDedupLine(actionID string, at int64, platformMsgID string) string {
	return actionID + "\t" + strconv.FormatInt(at, 10) + "\t" + platformMsgID + "\n"
}

func (d *Dedup) Seen(actionID string) (platformMsgID string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	platformMsgID, ok = d.sent[actionID]
	return platformMsgID, ok
}

// Mark records actionID as sent and fsyncs the log before returning. If
// writing fails, the action is still remembered for the life of the process
// and any partial line is truncated away so the next record stays intact.
func (d *Dedup) Mark(actionID, platformMsgID string) error {
	if actionID == "" || strings.ContainsAny(actionID, "\t\r\n") || strings.ContainsAny(platformMsgID, "\t\r\n") {
		return fmt.Errorf("napcat: invalid dedup entry %q/%q", actionID, platformMsgID)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sent[actionID] = platformMsgID
	if d.f == nil {
		return errors.New("napcat: dedup log closed")
	}
	n, err := d.f.Write([]byte(formatDedupLine(actionID, d.now().Unix(), platformMsgID)))
	if err != nil {
		if terr := d.f.Truncate(d.size); terr != nil {
			return fmt.Errorf("napcat: write dedup log: %w (truncating the partial line also failed: %v)", err, terr)
		}
		return fmt.Errorf("napcat: write dedup log: %w", err)
	}
	d.size += int64(n)
	if err := d.f.Sync(); err != nil {
		return fmt.Errorf("napcat: sync dedup log: %w", err)
	}
	return nil
}

func (d *Dedup) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return nil
	}
	err := d.f.Close()
	d.f = nil
	return err
}
