package tools

import (
	"encoding/json"
	"testing"
)

func TestIntArg(t *testing.T) {
	for _, c := range []struct {
		args string
		want int
		ok   bool
	}{
		{`{"n":20}`, 20, true},
		{`{"n":20.0}`, 20, true},
		{`{"n":"20"}`, 20, true},
		{`{"n":" 20 "}`, 20, true},
		{`{"n":"20.0"}`, 20, true},
		{`{"n":2e1}`, 20, true},
		{`{"n":-3}`, -3, true},
		{`{"n":"-3"}`, -3, true},
		{`{"n":0}`, 0, true},
		{`{"n":"20分钟"}`, 0, false},
		{`{"n":20.5}`, 0, false},
		{`{"n":"20.5"}`, 0, false},
		{`{"n":""}`, 0, false},
		{`{"n":"NaN"}`, 0, false},
		{`{"n":"0x14"}`, 0, false},
		{`{"n":1e300}`, 0, false},
		{`{"n":null}`, 0, false},
		{`{"n":true}`, 0, false},
		{`{"n":[20]}`, 0, false},
		{`{"m":20}`, 0, false},
		{`[20]`, 0, false},
		{`not json`, 0, false},
	} {
		got, ok := intArg(json.RawMessage(c.args), "n")
		if got != c.want || ok != c.ok {
			t.Errorf("intArg(%s) = %d, %v, want %d, %v", c.args, got, ok, c.want, c.ok)
		}
	}
}
