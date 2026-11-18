package contextbuilder

import (
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/autonomy"
)

func TestStripTranscriptPrefix_OuterSection(t *testing.T) {
	const outerHeading = "【外在状态（别人看得出来，可以在动作和外表描写里自然带出；不说数字，不提状态条、工具或系统）】"
	status := &autonomy.StateStatus{Activity: "看书", StartedAt: at(20, 10), UntilAt: at(20, 30)}
	bars := []autonomy.StateBar{
		{Name: "心情", Description: "高=开心放松，低=低落烦躁", Value: 65},
		{Name: "肚子", Description: "高=很饱", Value: 80, Outer: true, MaxStep: 40},
	}
	full := autonomy.RenderState(nil, status, bars, nil, at(20, 10), shanghai)
	outerOnly := autonomy.RenderState(nil, nil, bars[1:], nil, at(20, 10), shanghai)
	if !strings.Contains(full, outerHeading+"\n肚子：80/100（很高，单次最多 ±40）") || !strings.HasPrefix(outerOnly, "【你的状态") {
		t.Fatalf("test blocks lack the outer section:\n%s\n%s", full, outerOnly)
	}
	stepped := autonomy.RenderState(nil, nil, []autonomy.StateBar{{Name: "心情", Value: 65, MaxStep: 20}, bars[1]}, nil,
		at(20, 10), shanghai)

	cases := []struct{ in, want string }{
		{full + "\n好呀", "好呀"},
		{full, ""},
		{outerOnly + "\n好呀", "好呀"},
		{stepped + "\n好呀", "好呀"},
		{outerHeading + "\n肚子：80/100（很高，单次最多 ±40）\n好呀", "好呀"},
		{outerHeading + "\n肚子：80/100（很高，单次最多 ±40）\n脸红：0/100（很低）\n[20:15] 织语：嗯嗯", "嗯嗯"},
		{"肚子：80/100\n好呀", "肚子：80/100\n好呀"},
		{"心情：偏高（单次最多 ±20）\n好呀", "心情：偏高（单次最多 ±20）\n好呀"},
		{"好呀\n" + full, "好呀\n" + full},
	}
	for _, c := range cases {
		if got := StripTranscriptPrefix(c.in, "织语"); got != c.want {
			t.Errorf("StripTranscriptPrefix(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
