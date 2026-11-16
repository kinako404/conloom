// Package dedup is the local record adapters keep of outbox actions they have
// already sent, so an action delivered again is ACKed instead of resent.
package dedup

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Log is a local, append-only record of outbox actions already sent to the
// platform, so a re-delivered action is ACKed instead of being sent twice.
// Each line is "action_id\t<unix seconds>\t<platform_message_id>". Entries
// older than the retention are dropped when the log is opened and when it is
// compacted, which Mark does once the log holds twice the entries the last
// compaction kept (and at least minCompact).
type Log struct {
	mu        sync.Mutex
	path      string
	retention time.Duration
	// f is nil after Close, or after a compaction could not reopen the
	// file; then the next Mark reopens it.
	f      logFile
	closed bool
	size   int64
	now    func() time.Time
	sent   map[string]entry

	minCompact, compactAt int
}

// maxLine is the longest line loaded from the log; longer lines are
// skipped as malformed.
const maxLine = 1 << 20

// defaultMinCompact is the fewest entries that make Mark compact the log.
const defaultMinCompact = 1024

// logFile is the append handle of the log; tests substitute a failing one.
type logFile interface {
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

type entry struct {
	actionID, platformMsgID string
	at                      int64
}

// Open loads the log at path, drops entries older than retention and
// rewrites the file with the remaining entries before appending to it.
func Open(path string, retention time.Duration, now func() time.Time) (*Log, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("dedup: create directory: %w", err)
	}
	entries, err := load(path)
	if err != nil {
		return nil, err
	}

	cutoff := now().Add(-retention)
	sent := make(map[string]entry, len(entries))
	var kept []entry
	for _, e := range entries {
		if time.Unix(e.at, 0).Before(cutoff) {
			continue
		}
		kept = append(kept, e)
		sent[e.actionID] = e
	}
	if err := rewrite(path, kept); err != nil {
		return nil, err
	}

	d := &Log{path: path, retention: retention, now: now, sent: sent, minCompact: defaultMinCompact}
	if err := d.reopen(); err != nil {
		return nil, err
	}
	d.compactAt = max(d.minCompact, 2*len(sent))
	return d, nil
}

// reopen opens the file for appending.
func (d *Log) reopen() error {
	f, err := os.OpenFile(d.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("dedup: open log: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("dedup: open log: %w", err)
	}
	d.f, d.size = f, st.Size()
	return nil
}

// load reads the log, skipping malformed lines and lines longer than
// maxLine.
func load(path string) ([]entry, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dedup: read log: %w", err)
	}
	defer f.Close()

	var entries []entry
	r := bufio.NewReaderSize(f, maxLine)
	for {
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			for errors.Is(err, bufio.ErrBufferFull) {
				_, err = r.ReadSlice('\n')
			}
		} else if e, ok := parseLine(line); ok {
			entries = append(entries, e)
		}
		if errors.Is(err, io.EOF) {
			return entries, nil
		}
		if err != nil {
			return nil, fmt.Errorf("dedup: read log: %w", err)
		}
	}
}

func parseLine(line []byte) (entry, bool) {
	parts := strings.SplitN(strings.TrimRight(string(line), "\r\n"), "\t", 3)
	if len(parts) != 3 || parts[0] == "" {
		return entry{}, false
	}
	at, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return entry{}, false
	}
	return entry{actionID: parts[0], at: at, platformMsgID: parts[2]}, true
}

func rewrite(path string, entries []entry) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("dedup: compact log: %w", err)
	}
	defer os.Remove(tmp.Name())

	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		w.WriteString(formatLine(e.actionID, e.at, e.platformMsgID))
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
		return fmt.Errorf("dedup: compact log: %w", err)
	}
	return nil
}

func formatLine(actionID string, at int64, platformMsgID string) string {
	return actionID + "\t" + strconv.FormatInt(at, 10) + "\t" + platformMsgID + "\n"
}

func (d *Log) Seen(actionID string) (platformMsgID string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	e, ok := d.sent[actionID]
	return e.platformMsgID, ok
}

// Mark records actionID as sent and fsyncs the log before returning. If
// writing fails, the action is still remembered for the life of the process
// and any partial line is truncated away so the next record stays intact.
// Once the record is written, Mark may compact the log; a failed compaction
// is not reported, since the record is already on disk.
func (d *Log) Mark(actionID, platformMsgID string) error {
	if actionID == "" || strings.ContainsAny(actionID, "\t\r\n") || strings.ContainsAny(platformMsgID, "\t\r\n") {
		return fmt.Errorf("dedup: invalid entry %q/%q", actionID, platformMsgID)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	at := d.now().Unix()
	d.sent[actionID] = entry{actionID: actionID, platformMsgID: platformMsgID, at: at}
	if d.closed {
		return errors.New("dedup: log closed")
	}
	if d.f == nil {
		if err := d.reopen(); err != nil {
			return err
		}
	}
	n, err := d.f.Write([]byte(formatLine(actionID, at, platformMsgID)))
	if err != nil {
		if terr := d.f.Truncate(d.size); terr != nil {
			return fmt.Errorf("dedup: write log: %w (truncating the partial line also failed: %v)", err, terr)
		}
		return fmt.Errorf("dedup: write log: %w", err)
	}
	d.size += int64(n)
	if err := d.f.Sync(); err != nil {
		return fmt.Errorf("dedup: sync log: %w", err)
	}
	if len(d.sent) >= d.compactAt {
		d.compact()
	}
	return nil
}

// compact forgets entries older than the retention and rewrites the file
// with the rest. The append handle is closed first, as Windows cannot
// replace an open file, and opened again afterwards; if that fails, the next
// Mark retries. A rewrite that fails leaves the old file, which still holds
// every entry.
func (d *Log) compact() {
	cutoff := d.now().Add(-d.retention)
	kept := make([]entry, 0, len(d.sent))
	for id, e := range d.sent {
		if time.Unix(e.at, 0).Before(cutoff) {
			delete(d.sent, id)
			continue
		}
		kept = append(kept, e)
	}
	slices.SortFunc(kept, func(a, b entry) int {
		return cmp.Or(cmp.Compare(a.at, b.at), strings.Compare(a.actionID, b.actionID))
	})
	d.compactAt = max(d.minCompact, 2*len(kept))
	_ = d.f.Close()
	d.f = nil
	_ = rewrite(d.path, kept)
	_ = d.reopen()
}

func (d *Log) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	if d.f == nil {
		return nil
	}
	err := d.f.Close()
	d.f = nil
	return err
}
