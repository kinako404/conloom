package panel

import (
	"net/http/httptest"
	"testing"

	"github.com/kinako404/conloom/internal/store/sqlite"
)

var _ Store = (*sqlite.DB)(nil)

func TestPageLimit(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  int
		bad   bool
	}{
		{"", 50, false},
		{"limit=1", 1, false},
		{"limit=200", 200, false},
		{"limit=500", 200, false},
		{"limit=0", 0, true},
		{"limit=-3", 0, true},
		{"limit=x", 0, true},
	} {
		got, err := pageLimit(httptest.NewRequest("GET", "/panel/api/tasks?"+tc.query, nil))
		if (err != nil) != tc.bad || got != tc.want {
			t.Errorf("pageLimit(%q) = %d, %v; want %d, bad=%v", tc.query, got, err, tc.want, tc.bad)
		}
	}
}
