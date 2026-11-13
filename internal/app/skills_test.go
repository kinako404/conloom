package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/config"
	"github.com/kinako404/conloom/internal/skills"
)

type fakeNow struct{}

func (fakeNow) Spec() skills.Spec { return skills.Spec{Name: "now", Description: "现在的时间"} }
func (fakeNow) Call(context.Context, skills.Invocation, json.RawMessage) (string, error) {
	return "2026-11-13 星期五 16:30（Asia/Shanghai）", nil
}

// skillsConfig loads serveTestConfig with bot_main enabling skills and, when
// dir is set, a skills directory.
func skillsConfig(t *testing.T, botSkills, dir string) *config.Config {
	t.Helper()
	tmp := t.TempDir()
	yml := strings.ReplaceAll(serveTestConfig, "DATA_DIR", strconv.Quote(filepath.Join(tmp, "data")))
	yml = strings.Replace(yml, "  - id: bot_main\n", "  - id: bot_main\n    skills: "+botSkills+"\n", 1)
	if dir != "" {
		yml += "skills:\n  dir: " + strconv.Quote(dir) + "\n"
	}
	path := filepath.Join(tmp, "conloom.yaml")
	if err := os.WriteFile(path, []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := cfg.ValidateServe(); err != nil {
		t.Fatalf("ValidateServe: %v", err)
	}
	return cfg
}

func TestNew_ChecksEnabledSkills(t *testing.T) {
	if got := builtinTools(); len(got) != 0 {
		t.Fatalf("builtinTools = %d tools, want none until the tools are wired", len(got))
	}

	a, err := New(skillsConfig(t, "[]", ""), nil, Options{})
	if err != nil {
		t.Fatalf("New without skills: %v", err)
	}
	_ = a.Close()

	_, err = New(skillsConfig(t, "[time, memory]", ""), nil, Options{})
	if !errors.Is(err, skills.ErrInvalid) {
		t.Fatalf("New with skills whose tools are missing = %v, want skills.ErrInvalid", err)
	}
	for _, want := range []string{"bots[0].skills", `skill "time" uses unknown tool "now"`, `"remember"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}

	a, err = New(skillsConfig(t, "[time]", ""), nil, Options{Tools: []skills.Tool{fakeNow{}}})
	if err != nil {
		t.Fatalf("New with the time skill and a now tool: %v", err)
	}
	_ = a.Close()

	if _, err := New(skillsConfig(t, "[nope]", ""), nil, Options{Tools: []skills.Tool{fakeNow{}}}); !errors.Is(err, skills.ErrInvalid) ||
		!strings.Contains(err.Error(), `unknown skill "nope"`) {
		t.Errorf("unknown skill: err = %v", err)
	}
}

func TestNew_LoadsSkillsDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "clock"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: clock\ndescription: 报时\ntools: [now]\nconversations: [private]\n---\n- 问时间时调用 now。\n"
	if err := os.WriteFile(filepath.Join(dir, "clock", "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := New(skillsConfig(t, "[clock]", dir), nil, Options{Tools: []skills.Tool{fakeNow{}}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = a.Close()

	if err := os.WriteFile(filepath.Join(dir, "clock", "SKILL.md"), []byte("no frontmatter"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(skillsConfig(t, "[]", dir), nil, Options{}); !errors.Is(err, skills.ErrInvalid) {
		t.Errorf("invalid skill file: err = %v, want skills.ErrInvalid even when no bot enables it", err)
	}
}
