package napcat

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Dedup is a local, append-only record of outbox actions already sent to the
// platform, so a re-delivered action is ACKed instead of being sent twice.
// Each line is "action_id\t<unix seconds>\t<platform_message_id>".
type Dedup struct {
	mu   sync.Mutex
	f    *os.File
	now  func() time.Time
	sent map[string]string
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
	return &Dedup{f: f, now: now, sent: sent}, nil
}

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
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "\t", 3)
		if len(parts) != 3 || parts[0] == "" {
			continue
		}
		at, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		entries = append(entries, dedupEntry{actionID: parts[0], at: at, platformMsgID: parts[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("napcat: read dedup log: %w", err)
	}
	return entries, nil
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

// Mark records actionID as sent and fsyncs the log before returning.
func (d *Dedup) Mark(actionID, platformMsgID string) error {
	if actionID == "" || strings.ContainsAny(actionID, "\t\r\n") || strings.ContainsAny(platformMsgID, "\t\r\n") {
		return fmt.Errorf("napcat: invalid dedup entry %q/%q", actionID, platformMsgID)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return errors.New("napcat: dedup log closed")
	}
	if _, err := d.f.WriteString(formatDedupLine(actionID, d.now().Unix(), platformMsgID)); err != nil {
		return fmt.Errorf("napcat: write dedup log: %w", err)
	}
	if err := d.f.Sync(); err != nil {
		return fmt.Errorf("napcat: sync dedup log: %w", err)
	}
	d.sent[actionID] = platformMsgID
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
