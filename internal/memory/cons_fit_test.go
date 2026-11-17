package memory

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/router"
	"github.com/kinako404/conloom/internal/store"
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
		e.long.answer(`{"group_summary":"` + strings.Repeat("聊", 2900+400*i) + `","members":[{"ref":"P1","note":"` + strings.Repeat("记", 950+100*i) + `"}]}`)
		e.run(t, false)
		if i == 0 {
			if s, n := utf8.RuneCountInString(e.group(t).Summary), utf8.RuneCountInString(getMember(t, e.st, testPrivateKey, "20001").Note); s != 2900 || n != 950 {
				t.Fatalf("stored %d/%d runes, want the 2900/950 given kept: only the prompt asks for less", s, n)
			}
		}
		system := e.long.request(t, i).Messages[0].Content
		for _, want := range []string{"不超过 2030 字", "不超过 676 字"} {
			if !strings.Contains(system, want) {
				t.Fatalf("run %d: prompt lacks %q:\n%s", i, want, system)
			}
		}
	}
	if n := utf8.RuneCountInString(e.group(t).Summary); n != PrivateSummaryMaxRunes {
		t.Errorf("summary = %d runes, want cut to the policy's %d", n, PrivateSummaryMaxRunes)
	}
	if n := utf8.RuneCountInString(getMember(t, e.st, testPrivateKey, "20001").Note); n != PrivateNoteMaxRunes {
		t.Errorf("note = %d runes, want cut to the policy's %d", n, PrivateNoteMaxRunes)
	}
	logs := e.logs.String()
	if got := strings.Count(logs, "memory consolidation limits reduced"); got != 1 {
		t.Fatalf("clamp warnings = %d, want 1 per conversation:\n%s", got, logs)
	}
	for _, want := range []string{"reserved_output_tokens=4096", "summary_max_chars=3000", "note_max_chars=1000",
		"requested_summary_chars=2030", "requested_note_chars=676"} {
		if !strings.Contains(logs, want) {
			t.Errorf("clamp warning lacks %q:\n%s", want, logs)
		}
	}
}

func TestConsolidate_ShrunkPromptKeepsARememberedTail(t *testing.T) {
	e := newConsEnv(t, consOpts{private: true, reservedOutput: 4096})
	item := RememberMarker + "生日是三月五日"
	note := strings.Repeat("记", PrivateNoteMaxRunes-utf8.RuneCountInString(item)) + item
	putMember(t, e.st, store.MemberMemory{ConversationKey: testPrivateKey, PlatformUserID: "20001", DisplayName: "小明", Note: note})
	e.add(t, e.msg("20001", "小明", "今天弹了一首曲子", consAt(0)), e.botMsg("真好", consAt(1)))
	e.long.answer(`{"group_summary":"弹了一首曲子。","members":[{"ref":"P1","note":"` + note + `"}]}`)
	e.run(t, false)
	if system := e.long.request(t, 0).Messages[0].Content; !strings.Contains(system, "不超过 676 字") {
		t.Fatalf("prompt does not ask for the shrunk note length:\n%s", system)
	}
	if got := getMember(t, e.st, testPrivateKey, "20001").Note; got != note {
		t.Fatalf("note = %d runes ending %q, want the echoed %d-rune note with its remembered tail kept",
			utf8.RuneCountInString(got), string([]rune(got)[max(0, utf8.RuneCountInString(got)-10):]), PrivateNoteMaxRunes)
	}
}

func TestWarnOutputReservation(t *testing.T) {
	cfg := func(reserved int) *config.Config {
		return &config.Config{
			ModelProfiles: map[string]config.ModelProfile{string(router.RoleLong): {Context: config.ContextLimits{ReservedOutputTokens: reserved}}}}
	}
	withMemory := func(on bool) conloomapi.GroupPolicy {
		p := conloomapi.DefaultGroupPolicy()
		p.Memory.Enabled = on
		return p
	}
	const notice4096 = "长模型（long_context）给输出预留的令牌数 4096 低于建议的 8192，记忆整理时摘要和笔记会比上限短"
	for _, c := range []struct {
		name           string
		cfg            *config.Config
		group, private conloomapi.GroupPolicy
		notice         string
	}{
		{"private memory, 4096", cfg(4096), withMemory(false), withMemory(true), notice4096},
		{"group memory, 4096", cfg(4096), withMemory(true), withMemory(false), notice4096},
		{"memory off, 4096", cfg(4096), withMemory(false), withMemory(false), ""},
		{"private memory, 8192", cfg(8192), withMemory(false), withMemory(true), ""},
		{"private memory, unlimited", cfg(0), withMemory(false), withMemory(true), ""},
		{"no long model", &config.Config{}, withMemory(true), withMemory(true), ""},
	} {
		var buf bytes.Buffer
		notice := WarnOutputReservation(c.cfg, c.group, c.private, slog.New(slog.NewTextHandler(&buf, nil)))
		warn := c.notice != ""
		if got := strings.Contains(buf.String(), "level=WARN"); got != warn {
			t.Errorf("%s: warned = %v, want %v: %s", c.name, got, warn, buf.String())
		}
		if warn && !strings.Contains(buf.String(), "recommended=8192") {
			t.Errorf("%s: warning lacks the recommendation: %s", c.name, buf.String())
		}
		if notice != c.notice {
			t.Errorf("%s: notice = %q, want %q", c.name, notice, c.notice)
		}
	}
}
