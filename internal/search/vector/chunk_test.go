package vector

import (
	"strings"
	"testing"
	"time"

	"github.com/kinako404/conloom/internal/skills/tools"
)

func lines(spec ...any) []tools.TranscriptLine {
	var out []tools.TranscriptLine
	for i := 0; i < len(spec); i += 2 {
		out = append(out, tools.TranscriptLine{Seq: int64(len(out) + 1), At: at(spec[i].(int)), Text: spec[i+1].(string)})
	}
	return out
}

func sizes(chunks []*chunk) []int {
	var out []int
	for _, c := range chunks {
		out = append(out, len(c.lines))
	}
	return out
}

func TestSplit(t *testing.T) {
	var spec []any
	for i := range 10 {
		spec = append(spec, i, "短句")
	}
	if got := sizes(split(lines(spec...))); len(got) != 2 || got[0] != 8 || got[1] != 2 {
		t.Errorf("10 lines split into %v, want [8 2]", got)
	}

	if got := sizes(split(lines(0, "a", 30, "b", 61, "c", 62, "d"))); len(got) != 2 || got[0] != 2 || got[1] != 2 {
		t.Errorf("a 31-minute pause split into %v, want [2 2] (30 minutes still joins)", got)
	}

	long := strings.Repeat("长", 150)
	got := split(lines(0, long, 1, long, 2, long, 3, "x"))
	if s := sizes(got); len(s) != 2 || s[0] != 2 || s[1] != 2 {
		t.Errorf("long lines split into %v, want [2 2] (at most about 400 runes)", s)
	}
	if text := got[0].text(); text != long+"\n"+long || got[0].runes != 301 {
		t.Errorf("chunk text = %q (%d runes)", text, got[0].runes)
	}
	if split(nil) != nil {
		t.Error("split(nil) != nil")
	}
}

func TestChunkClosed(t *testing.T) {
	c := split(lines(0, "a", 1, "b"))[0]
	if c.closedAt(at(31)) {
		t.Error("closed 30 minutes after its last line, want open")
	}
	if !c.closedAt(at(31).Add(time.Second)) {
		t.Error("open more than 30 minutes after its last line, want closed")
	}
	var spec []any
	for i := range 8 {
		spec = append(spec, i, "x")
	}
	if full := split(lines(spec...))[0]; !full.closedAt(at(8)) {
		t.Error("a chunk of 8 lines is open, want closed")
	}
	rec := c.record(groupKey, "m1", []float32{1})
	if rec.StartSeq != 1 || rec.EndSeq != 2 || !rec.StartAt.Equal(at(0)) || !rec.EndAt.Equal(at(1)) || len(rec.TextHash) != 64 || rec.Model != "m1" {
		t.Errorf("record = %+v", rec)
	}
}
