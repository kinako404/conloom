package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

func TestBuiltinTools(t *testing.T) {
	var names []string
	for _, tool := range builtinTools(toolDeps{}) {
		names = append(names, tool.Spec().Name)
	}
	if !slices.Equal(names, []string{"remember", "recall", "get_affinity", "now"}) {
		t.Fatalf("builtinTools = %v", names)
	}
	for _, c := range []struct {
		text, prefix string
		want         bool
	}{
		{"/记住 我喜欢草莓", "/", true},
		{" /help", "/", true},
		{"/草莓派真好吃", "/", false},
		{"记住我喜欢草莓", "/", false},
		{"!help", "!", true},
	} {
		if got := isCommand(c.text, c.prefix); got != c.want {
			t.Errorf("isCommand(%q, %q) = %v, want %v", c.text, c.prefix, got, c.want)
		}
	}
}

func TestNew_ChecksEnabledSkills(t *testing.T) {
	a, err := New(skillsConfig(t, "[]", ""), nil, Options{})
	if err != nil {
		t.Fatalf("New without skills: %v", err)
	}
	_ = a.Close()

	a, err = New(skillsConfig(t, "[time, memory]", ""), nil, Options{})
	if err != nil {
		t.Fatalf("New with the built-in skills and tools: %v", err)
	}
	_ = a.Close()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "weather"), 0o755); err != nil {
		t.Fatal(err)
	}
	skill := "---\nname: weather\ndescription: 天气\ntools: [weather, now]\nconversations: [private]\n---\n- 问天气时调用 weather。\n"
	if err := os.WriteFile(filepath.Join(dir, "weather", "SKILL.md"), []byte(skill), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = New(skillsConfig(t, "[time, weather]", dir), nil, Options{})
	if !errors.Is(err, skills.ErrInvalid) {
		t.Fatalf("New with a skill whose tool is missing = %v, want skills.ErrInvalid", err)
	}
	for _, want := range []string{"bots[0].skills", `skill "weather" uses unknown tool "weather"`} {
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
