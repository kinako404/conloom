package napcat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestDedup_PersistsAcrossReopenAndExpires(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.log")
	clock := &fakeClock{t: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)}
	retention := 24 * time.Hour

	d, err := OpenDedup(path, retention, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Seen("act_1"); ok {
		t.Fatal("fresh log reports act_1 as seen")
	}
	if err := d.Mark("act_1", "7001"); err != nil {
		t.Fatal(err)
	}
	if id, ok := d.Seen("act_1"); !ok || id != "7001" {
		t.Fatalf("Seen after Mark = %q/%v", id, ok)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	clock.t = clock.t.Add(2 * time.Hour)
	d, err = OpenDedup(path, retention, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if id, ok := d.Seen("act_1"); !ok || id != "7001" {
		t.Fatalf("Seen after reopen = %q/%v", id, ok)
	}
	if err := d.Mark("act_2", "7002"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	clock.t = clock.t.Add(23 * time.Hour)
	d, err = OpenDedup(path, retention, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Seen("act_1"); ok {
		t.Error("act_1 should have expired")
	}
	if id, ok := d.Seen("act_2"); !ok || id != "7002" {
		t.Errorf("act_2 should survive: %q/%v", id, ok)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "act_1") {
		t.Errorf("expired entry not compacted away: %q", data)
	}
	wantLine := "act_2\t1794477600\t7002\n"
	if string(data) != wantLine {
		t.Errorf("file = %q, want %q", data, wantLine)
	}
}

func TestDedup_SkipsMalformedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.log")
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	content := "garbage\nact_ok\t1794468600\t7001\nact_bad\tnotanumber\t7002\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDedup(path, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if id, ok := d.Seen("act_ok"); !ok || id != "7001" {
		t.Errorf("act_ok = %q/%v", id, ok)
	}
	if _, ok := d.Seen("act_bad"); ok {
		t.Error("malformed line loaded")
	}
}

func TestDedup_SkipsOverlongLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.log")
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	long := strings.Repeat("7", 100<<10)
	huge := strings.Repeat("y", 2<<20)
	content := "act_long\t1794468600\t" + long + "\n" +
		"act_huge\t1794468600\t" + huge + "\n" +
		"act_ok\t1794468600\t7001\n" +
		"act_tail\t1794468600\t" + huge
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	d, err := OpenDedup(path, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenDedup: %v", err)
	}
	defer d.Close()
	if id, ok := d.Seen("act_ok"); !ok || id != "7001" {
		t.Errorf("act_ok = %q/%v, want 7001", id, ok)
	}
	if id, ok := d.Seen("act_long"); !ok || id != long {
		t.Errorf("act_long (a 100 KiB line) loaded = %v, want loaded", ok)
	}
	for _, id := range []string{"act_huge", "act_tail"} {
		if _, ok := d.Seen(id); ok {
			t.Errorf("%s (a line over 1 MiB) loaded, want skipped", id)
		}
	}
}

// failingFile writes half of the next failWrites writes and then fails.
type failingFile struct {
	dedupFile
	failWrites int
}

func (f *failingFile) Write(p []byte) (int, error) {
	if f.failWrites > 0 {
		f.failWrites--
		n, _ := f.dedupFile.Write(p[:len(p)/2])
		return n, errors.New("no space left on device")
	}
	return f.dedupFile.Write(p)
}

func TestDedup_FailedAppendLeavesNoPartialLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sent.log")
	now := time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC)
	d, err := OpenDedup(path, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Mark("act_1", "7001"); err != nil {
		t.Fatal(err)
	}
	d.f = &failingFile{dedupFile: d.f, failWrites: 1}
	if err := d.Mark("act_2", "7002"); err == nil {
		t.Fatal("Mark with a failing write succeeded")
	}
	if _, ok := d.Seen("act_2"); !ok {
		t.Error("act_2 not remembered in memory after a failed write; a redelivery in this process would resend it")
	}
	if err := d.Mark("act_3", "7003"); err != nil {
		t.Fatal(err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := "act_1\t1794470400\t7001\nact_3\t1794470400\t7003\n"; string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	d, err = OpenDedup(path, time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if id, ok := d.Seen("act_3"); !ok || id != "7003" {
		t.Errorf("act_3 after reopen = %q/%v, want 7003", id, ok)
	}
}

func TestDedup_CompactionSyncsDirectory(t *testing.T) {
	var synced []string
	orig := syncDir
	syncDir = func(dir string) error {
		synced = append(synced, dir)
		return orig(dir)
	}
	t.Cleanup(func() { syncDir = orig })

	dir := t.TempDir()
	d, err := OpenDedup(filepath.Join(dir, "sent.log"), time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if len(synced) != 1 || synced[0] != dir {
		t.Errorf("synced directories = %q, want [%q]", synced, dir)
	}
}

func TestDedup_RejectsInvalidActionID(t *testing.T) {
	d, err := OpenDedup(filepath.Join(t.TempDir(), "sent.log"), time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	for _, id := range []string{"", "a\tb", "a\nb"} {
		if err := d.Mark(id, "1"); err == nil {
			t.Errorf("Mark(%q) succeeded", id)
		}
	}
}
