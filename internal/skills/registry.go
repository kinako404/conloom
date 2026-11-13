package skills

import (
	"fmt"
	"slices"
	"strings"
)

// sectionHeader starts the skill section appended to the system prompt.
const sectionHeader = "【技能】你可以在需要时调用下面的工具；不需要时直接回复。"

// Registry holds the loaded skills and the registered tools, indexed by name.
type Registry struct {
	skills map[string]Skill
	tools  map[string]Tool
}

// NewRegistry indexes tools by Spec().Name; an unnamed or repeated name is
// ErrInvalid. Whether the skills' tools exist is checked by Check, only for
// the skills a bot enables.
func NewRegistry(skills map[string]Skill, tools []Tool) (*Registry, error) {
	r := &Registry{skills: skills, tools: make(map[string]Tool, len(tools))}
	for _, t := range tools {
		name := t.Spec().Name
		if name == "" {
			return nil, fmt.Errorf("%w: a tool has no name", ErrInvalid)
		}
		if _, dup := r.tools[name]; dup {
			return nil, fmt.Errorf("%w: tool %q registered twice", ErrInvalid, name)
		}
		r.tools[name] = t
	}
	return r, nil
}

// Check reports, as ErrInvalid, every name that is not a loaded skill and
// every tool of the named skills that is not registered.
func (r *Registry) Check(names []string) error {
	var errs []string
	for _, name := range names {
		s, ok := r.skills[name]
		if !ok {
			errs = append(errs, fmt.Sprintf("unknown skill %q", name))
			continue
		}
		for _, tool := range s.Tools {
			if _, ok := r.tools[tool]; !ok {
				errs = append(errs, fmt.Sprintf("skill %q uses unknown tool %q", name, tool))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalid, strings.Join(errs, "; "))
	}
	return nil
}

// Toolset is what the model may use in one conversation: the skill section
// for the end of the system prompt and the tools it may call. The zero
// Toolset offers nothing.
type Toolset struct {
	Section string
	Tools   []Tool
}

// Empty reports whether the toolset offers no tools.
func (t Toolset) Empty() bool { return len(t.Tools) == 0 }

// DefaultMaxResultRunes bounds the result of a tool that does not declare
// its own limit.
const DefaultMaxResultRunes = 2000

// ResultLimiter is implemented by a tool that declares the longest result
// it returns, in runes.
type ResultLimiter interface {
	MaxResultRunes() int
}

// MaxResultRunes is the longest result tool may return: its declared limit
// (see ResultLimiter), at most DefaultMaxResultRunes, which also applies when
// it declares none.
func MaxResultRunes(tool Tool) int {
	if l, ok := tool.(ResultLimiter); ok {
		if n := l.MaxResultRunes(); n > 0 && n < DefaultMaxResultRunes {
			return n
		}
	}
	return DefaultMaxResultRunes
}

// MaxResultRunes is the longest result any tool of the toolset may return.
func (t Toolset) MaxResultRunes() int {
	n := 0
	for _, tool := range t.Tools {
		n = max(n, MaxResultRunes(tool))
	}
	return n
}

// Lookup returns the toolset's tool called name.
func (t Toolset) Lookup(name string) (Tool, bool) {
	i := slices.IndexFunc(t.Tools, func(tool Tool) bool { return tool.Spec().Name == name })
	if i < 0 {
		return nil, false
	}
	return t.Tools[i], true
}

// For returns the toolset of the named skills (in order) that apply to
// conversations of convType: their bodies under the section header and their
// tools, each once. Unknown skills and unregistered tools, which Check
// reports at startup, are left out.
func (r *Registry) For(names []string, convType string) Toolset {
	if r == nil {
		return Toolset{}
	}
	var ts Toolset
	bodies := []string{sectionHeader}
	for _, name := range names {
		s, ok := r.skills[name]
		if !ok || !slices.Contains(s.Conversations, convType) {
			continue
		}
		bodies = append(bodies, s.Body)
		for _, tool := range s.Tools {
			t, ok := r.tools[tool]
			if ok && !slices.ContainsFunc(ts.Tools, func(x Tool) bool { return x.Spec().Name == tool }) {
				ts.Tools = append(ts.Tools, t)
			}
		}
	}
	if len(ts.Tools) == 0 {
		return Toolset{}
	}
	ts.Section = strings.Join(bodies, "\n")
	return ts
}
