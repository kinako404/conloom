package skills

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type fakeTool struct{ name string }

func (f fakeTool) Spec() Spec { return Spec{Name: f.name, Description: "fake " + f.name} }
func (f fakeTool) Call(context.Context, Invocation, json.RawMessage) (string, error) {
	return "ok", nil
}

func fakeTools(names ...string) []Tool {
	out := make([]Tool, len(names))
	for i, n := range names {
		out[i] = fakeTool{n}
	}
	return out
}

const memoryBody = "- 用户明确让你记住一件关于他自己的事时，调用 remember。联系方式、证件号、别人的隐私不要记。\n" +
	"- 用户问到以前聊过、但眼前的对话和你记得的事里都没有的内容时，先调用 recall 查一查，再回答；查不到就老实说不记得，不要编。\n" +
	"- 用户问你对他的好感、你们有多熟时，调用 get_affinity。\n" +
	"- 调用之后，用你自己的口吻自然地回复，不要提到工具、系统或查询过程。"

const timeBody = "- 用户问今天几号、星期几、现在几点，或者回答需要知道\"今天\"时，调用 now。\n" +
	"- 用你自己的口吻自然地说出来，不要照抄格式。"

func TestLoad_Builtins(t *testing.T) {
	got, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if names := slices.Sorted(func(yield func(string) bool) {
		for n := range got {
			if !yield(n) {
				return
			}
		}
	}); !slices.Equal(names, []string{"memory", "time"}) {
		t.Fatalf("builtin skills = %v", names)
	}
	mem := got["memory"]
	if mem.Body != memoryBody || !slices.Equal(mem.Tools, []string{"remember", "recall", "get_affinity"}) ||
		!slices.Equal(mem.Conversations, []string{"private", "group"}) || mem.Description == "" || mem.Source != "builtin" {
		t.Errorf("memory = %+v", mem)
	}
	tm := got["time"]
	if tm.Body != timeBody || !slices.Equal(tm.Tools, []string{"now"}) || !slices.Equal(tm.Conversations, []string{"private", "group"}) {
		t.Errorf("time = %+v", tm)
	}
}

func skillFile(name, extra, body string) string {
	return "---\nname: " + name + "\ndescription: 测试技能\ntools: [now]\nconversations: [private]\n" + extra + "---\n" + body + "\n"
}

func writeSkill(t *testing.T, dir, sub, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sub, "SKILL.md"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoad_DirOverridesAndAdds(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "time", skillFile("time", "", "- 只在被问到时间时调用 now。"))
	writeSkill(t, dir, "weather", "\ufeff---\r\nname: weather\r\ndescription: 天气\r\ntools: [now]\r\nconversations: [group, private]\r\n---\r\n\r\n- 问天气时先调用 now。\r\n")
	if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("not a skill"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 3 || got["memory"].Body != memoryBody {
		t.Fatalf("skills = %v", got)
	}
	if tm := got["time"]; tm.Body != "- 只在被问到时间时调用 now。" || !slices.Equal(tm.Conversations, []string{"private"}) ||
		tm.Source != filepath.Join(dir, "time", "SKILL.md") {
		t.Errorf("overridden time = %+v", tm)
	}
	if w := got["weather"]; w.Body != "- 问天气时先调用 now。" || w.Description != "天气" || !slices.Equal(w.Conversations, []string{"group", "private"}) {
		t.Errorf("weather = %+v", w)
	}
}

func TestLoad_Invalid(t *testing.T) {
	cases := []struct {
		name, sub, content, want string
	}{
		{"no frontmatter", "a", "name: a\n", "frontmatter"},
		{"unclosed frontmatter", "a", "---\nname: a\n", "frontmatter"},
		{"bad yaml", "a", "---\nname: [\n---\nbody\n", "frontmatter"},
		{"unknown field", "a", skillFile("a", "kind: http\n", "body"), "kind"},
		{"name differs from directory", "b", skillFile("a", "", "body"), `name "a" must match its directory "b"`},
		{"bad name", "A b", skillFile("A b", "", "body"), "name"},
		{"no description", "a", "---\nname: a\ntools: [now]\nconversations: [private]\n---\nbody\n", "description must not be empty"},
		{"no tools", "a", "---\nname: a\ndescription: d\nconversations: [private]\n---\nbody\n", "tools must not be empty"},
		{"duplicate tool", "a", "---\nname: a\ndescription: d\ntools: [now, now]\nconversations: [private]\n---\nbody\n", `duplicate tool "now"`},
		{"no conversations", "a", "---\nname: a\ndescription: d\ntools: [now]\n---\nbody\n", "conversations must not be empty"},
		{"bad conversation", "a", "---\nname: a\ndescription: d\ntools: [now]\nconversations: [channel]\n---\nbody\n", `conversations: "channel" is not private or group`},
		{"empty body", "a", skillFile("a", "", "  "), "body must not be empty"},
		{"long body", "a", skillFile("a", "", strings.Repeat("长", MaxBodyRunes+1)), "body must be at most 600 characters, got 601"},
		{"missing SKILL.md", "a", "", "SKILL.md"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.content == "" {
				if err := os.MkdirAll(filepath.Join(dir, c.sub), 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				writeSkill(t, dir, c.sub, c.content)
			}
			_, err := Load(dir)
			if !errors.Is(err, ErrInvalid) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("Load = %v, want ErrInvalid containing %q", err, c.want)
			}
		})
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing dir: err = %v, want ErrInvalid", err)
	}
	dir := t.TempDir()
	writeSkill(t, dir, "a", skillFile("a", "", strings.Repeat("长", MaxBodyRunes)))
	if _, err := Load(dir); err != nil {
		t.Errorf("body of exactly %d runes: %v", MaxBodyRunes, err)
	}
}

func testRegistry(t *testing.T, tools ...string) *Registry {
	t.Helper()
	skills, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	extra := Skill{Name: "groupnote", Description: "d", Tools: []string{"now", "remember"}, Conversations: []string{"group"}, Body: "- 群里用。"}
	skills["groupnote"] = extra
	r, err := NewRegistry(skills, fakeTools(tools...))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return r
}

func TestRegistry_CheckEnabledSkills(t *testing.T) {
	r := testRegistry(t, "now")
	if err := r.Check(nil); err != nil {
		t.Errorf("Check(nil): %v", err)
	}
	if err := r.Check([]string{"time"}); err != nil {
		t.Errorf("Check(time): %v", err)
	}
	err := r.Check([]string{"time", "memory", "nope"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Check = %v, want ErrInvalid", err)
	}
	for _, want := range []string{`skill "memory" uses unknown tool "remember"`, `"recall"`, `"get_affinity"`, `unknown skill "nope"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check error %q lacks %q", err, want)
		}
	}

	if _, err := NewRegistry(nil, append(fakeTools("now"), fakeTool{"now"})); !errors.Is(err, ErrInvalid) {
		t.Errorf("duplicate tool: err = %v", err)
	}
	if _, err := NewRegistry(nil, fakeTools("")); !errors.Is(err, ErrInvalid) {
		t.Errorf("unnamed tool: err = %v", err)
	}
}

func TestRegistry_For(t *testing.T) {
	r := testRegistry(t, "now", "remember", "recall", "get_affinity")
	ts := r.For([]string{"time", "groupnote", "memory"}, "private")
	wantSection := "【技能】你可以在需要时调用下面的工具；不需要时直接回复。\n" + timeBody + "\n" + memoryBody
	if ts.Section != wantSection {
		t.Errorf("private section =\n%s\nwant\n%s", ts.Section, wantSection)
	}
	var names []string
	for _, tool := range ts.Tools {
		names = append(names, tool.Spec().Name)
	}
	if !slices.Equal(names, []string{"now", "remember", "recall", "get_affinity"}) {
		t.Errorf("private tools = %v", names)
	}
	if tool, ok := ts.Lookup("recall"); !ok || tool.Spec().Name != "recall" {
		t.Errorf("Lookup(recall) = %v, %v", tool, ok)
	}

	group := r.For([]string{"time", "groupnote"}, "group")
	if group.Section != "【技能】你可以在需要时调用下面的工具；不需要时直接回复。\n"+timeBody+"\n- 群里用。" || len(group.Tools) != 2 {
		t.Errorf("group toolset = %+v", group)
	}
	if _, ok := group.Lookup("recall"); ok {
		t.Error("group toolset offers a tool of a skill it does not enable")
	}
	if empty := r.For([]string{"groupnote"}, "private"); !reflect.DeepEqual(empty, Toolset{}) || !empty.Empty() {
		t.Errorf("toolset without matching skills = %+v", empty)
	}
	if again := r.For([]string{"time", "groupnote", "memory"}, "private"); again.Section != ts.Section {
		t.Error("section is not stable")
	}
}
