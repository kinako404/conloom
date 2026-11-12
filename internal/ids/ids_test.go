package ids

import (
	"strings"
	"testing"
)

func TestNew_PrefixAndUniqueness(t *testing.T) {
	const prefix = "task"
	const n = 10000
	seen := make(map[string]struct{}, n)
	prev := ""
	for i := 0; i < n; i++ {
		id := New(prefix)
		if !strings.HasPrefix(id, prefix+"_") {
			t.Fatalf("id %q lacks prefix", id)
		}
		if len(id) != len(prefix+"_")+26 {
			t.Fatalf("id %q has length %d", id, len(id))
		}
		body := id[len(prefix)+1:]
		for _, c := range body {
			if !strings.ContainsRune(crockford, c) {
				t.Fatalf("id %q has non-Crockford char %q", id, c)
			}
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = struct{}{}
		if prev != "" && body[:10] < prev[:10] {
			t.Fatalf("time part went backwards: %q after %q", body, prev)
		}
		prev = body
	}
}

func TestEncodeKnownValue(t *testing.T) {
	var b [16]byte
	if got := encode(b); got != strings.Repeat("0", 26) {
		t.Errorf("zero = %q", got)
	}
	for i := range b {
		b[i] = 0xff
	}
	if got := encode(b); got != "7"+strings.Repeat("Z", 25) {
		t.Errorf("max = %q", got)
	}
}
