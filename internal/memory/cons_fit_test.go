package memory

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/pkg/conloomapi"
)

func TestConsFitLimits(t *testing.T) {
	for _, c := range []struct {
		name                  string
		out, summary, note    int
		wantSummary, wantNote int
	}{
		{"private defaults fit 8192", 8192, 3000, 1000, 3000, 1000},
		{"group defaults fit 4096", 4096, 1500, 300, 1500, 300},
		{"unlimited output", 0, 8000, 2000, 8000, 2000},
		{"private defaults scaled for 4096", 4096, 3000, 1000, 2030, 676},
		{"policy maximum scaled for 8192", 8192, 8000, 2000, 4459, 1114},
		{"floors respected", 300, 3000, 1000, conloomapi.MinSummaryMaxChars, conloomapi.MinNoteMaxChars},
	} {
		s, n := consFitLimits(c.out, c.summary, c.note)
		if s != c.wantSummary || n != c.wantNote {
			t.Errorf("%s: consFitLimits(%d, %d, %d) = %d/%d, want %d/%d", c.name, c.out, c.summary, c.note, s, n, c.wantSummary, c.wantNote)
		}
		if c.out > 0 && c.wantSummary > conloomapi.MinSummaryMaxChars {
			if planned, share := consPlannedOutput(1, s, n), int(float64(c.out)*consOutputShare); planned > share {
				t.Errorf("%s: one member plans %d tokens, over %d", c.name, planned, share)
			}
		}
	}
}

func TestConsolidate_ClampsLimitsToTheOutputReservation(t *testing.T) {
	e := newConsEnv(t, consOpts{private: true, reservedOutput: 4096})
	for i := range 2 {
		e.add(t, e.msg("20001", "小明", "我最近在学钢琴", consAt(2*i)), e.botMsg("加油", consAt(2*i+1)))
		e.long.answer(`{"group_summary":"` + strings.Repeat("聊", 2900) + `","members":[{"ref":"P1","note":"` + strings.Repeat("记", 950) + `"}]}`)
		e.run(t, false)
		system := e.long.request(t, i).Messages[0].Content
		for _, want := range []string{"不超过 2030 字", "不超过 676 字"} {
			if !strings.Contains(system, want) {
				t.Fatalf("run %d: prompt lacks %q:\n%s", i, want, system)
			}
		}
	}
	if n := utf8.RuneCountInString(e.group(t).Summary); n != 2030 {
		t.Errorf("summary = %d runes, want cut to the 2030 the prompt asked for", n)
	}
	if n := utf8.RuneCountInString(getMember(t, e.st, testPrivateKey, "20001").Note); n != 676 {
		t.Errorf("note = %d runes, want cut to 676", n)
	}
	logs := e.logs.String()
	if got := strings.Count(logs, "memory consolidation limits reduced"); got != 1 {
		t.Fatalf("clamp warnings = %d, want 1 per conversation:\n%s", got, logs)
	}
	for _, want := range []string{"reserved_output_tokens=4096", "summary_max_chars=3000", "note_max_chars=1000",
		"effective_summary_max_chars=2030", "effective_note_max_chars=676"} {
		if !strings.Contains(logs, want) {
			t.Errorf("clamp warning lacks %q:\n%s", want, logs)
		}
	}
}

func TestWarnOutputReservation(t *testing.T) {
	cfg := func(reserved int, groupOn, privateOn bool) *config.Config {
		c := &config.Config{DefaultGroupPolicy: conloomapi.DefaultGroupPolicy(),
			ModelProfiles: map[string]config.ModelProfile{string(router.RoleLong): {Context: config.ContextLimits{ReservedOutputTokens: reserved}}}}
		c.DefaultGroupPolicy.Memory.Enabled = groupOn
		if privateOn {
			p := conloomapi.DefaultGroupPolicy()
			p.Memory.Enabled = true
			c.DefaultPrivatePolicy = &p
		}
		return c
	}
	for _, c := range []struct {
		name string
		cfg  *config.Config
		warn bool
	}{
		{"private memory, 4096", cfg(4096, false, true), true},
		{"group memory, 4096", cfg(4096, true, false), true},
		{"memory off, 4096", cfg(4096, false, false), false},
		{"private memory, 8192", cfg(8192, false, true), false},
		{"private memory, unlimited", cfg(0, false, true), false},
		{"no long model", &config.Config{DefaultGroupPolicy: conloomapi.DefaultGroupPolicy()}, false},
	} {
		var buf bytes.Buffer
		WarnOutputReservation(c.cfg, slog.New(slog.NewTextHandler(&buf, nil)))
		if got := strings.Contains(buf.String(), "level=WARN"); got != c.warn {
			t.Errorf("%s: warned = %v, want %v: %s", c.name, got, c.warn, buf.String())
		}
		if c.warn && !strings.Contains(buf.String(), "recommended=8192") {
			t.Errorf("%s: warning lacks the recommendation: %s", c.name, buf.String())
		}
	}
}
