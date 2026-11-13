package skills

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// ErrInvalid wraps every error in a skill file, the skills directory or the
// skills a bot enables: a configuration error.
var ErrInvalid = errors.New("skills: invalid")

// MaxBodyRunes is the longest allowed skill body, in characters.
const MaxBodyRunes = 600

// Skill is a parsed SKILL.md: metadata and the usage notes (Body) given to
// the model. Source is "builtin" or the file's path.
type Skill struct {
	Name          string
	Description   string
	Tools         []string
	Conversations []string
	Body          string
	Source        string
}

//go:embed builtin/*/SKILL.md
var builtinFS embed.FS

const skillFileName = "SKILL.md"

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Load returns the built-in skills, replaced or extended by the skills in dir
// (each <name>/SKILL.md; "" for none). Any invalid file is ErrInvalid.
func Load(dir string) (map[string]Skill, error) {
	out := map[string]Skill{}
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, fmt.Errorf("%w: built-in skills: %w", ErrInvalid, err)
	}
	for _, e := range entries {
		data, err := builtinFS.ReadFile(path.Join("builtin", e.Name(), skillFileName))
		if err != nil {
			return nil, fmt.Errorf("%w: built-in skill %s: %w", ErrInvalid, e.Name(), err)
		}
		s, err := parse(data, e.Name(), "builtin")
		if err != nil {
			return nil, err
		}
		out[s.Name] = s
	}
	if dir == "" {
		return out, nil
	}
	entries, err = os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: skills.dir: %w", ErrInvalid, err)
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		file := filepath.Join(dir, e.Name(), skillFileName)
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
		}
		s, err := parse(data, e.Name(), file)
		if err != nil {
			return nil, err
		}
		out[s.Name] = s
	}
	return out, nil
}

type frontmatter struct {
	Name          string   `yaml:"name"`
	Description   string   `yaml:"description"`
	Tools         []string `yaml:"tools"`
	Conversations []string `yaml:"conversations"`
}

// parse reads a SKILL.md of directory dirName: YAML frontmatter between "---"
// lines, then the body.
func parse(data []byte, dirName, source string) (Skill, error) {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s: %s", ErrInvalid, source, fmt.Sprintf(format, args...))
	}
	text := strings.ReplaceAll(string(bytes.TrimPrefix(data, []byte("\ufeff"))), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return Skill{}, invalid("missing frontmatter: the file must start with a --- line")
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		if head, ok = strings.CutSuffix(rest, "\n---"); !ok {
			return Skill{}, invalid("unclosed frontmatter: missing the closing --- line")
		}
	}
	var fm frontmatter
	dec := yaml.NewDecoder(strings.NewReader(head))
	dec.KnownFields(true)
	if err := dec.Decode(&fm); err != nil {
		return Skill{}, invalid("frontmatter: %v", err)
	}
	var errs []string
	switch {
	case !namePattern.MatchString(fm.Name):
		errs = append(errs, fmt.Sprintf("name %q must be 1-32 lowercase letters, digits, - or _", fm.Name))
	case fm.Name != dirName:
		errs = append(errs, fmt.Sprintf("name %q must match its directory %q", fm.Name, dirName))
	}
	if strings.TrimSpace(fm.Description) == "" {
		errs = append(errs, "description must not be empty")
	}
	if len(fm.Tools) == 0 {
		errs = append(errs, "tools must not be empty")
	}
	for i, tool := range fm.Tools {
		if strings.TrimSpace(tool) == "" {
			errs = append(errs, "tools must not contain an empty name")
		} else if slices.Contains(fm.Tools[:i], tool) {
			errs = append(errs, fmt.Sprintf("duplicate tool %q", tool))
		}
	}
	if len(fm.Conversations) == 0 {
		errs = append(errs, "conversations must not be empty")
	}
	for i, c := range fm.Conversations {
		if c != "private" && c != "group" {
			errs = append(errs, fmt.Sprintf("conversations: %q is not private or group", c))
		} else if slices.Contains(fm.Conversations[:i], c) {
			errs = append(errs, fmt.Sprintf("conversations: duplicate %q", c))
		}
	}
	body = strings.TrimSpace(body)
	if body == "" {
		errs = append(errs, "body must not be empty")
	} else if n := utf8.RuneCountInString(body); n > MaxBodyRunes {
		errs = append(errs, fmt.Sprintf("body must be at most %d characters, got %d", MaxBodyRunes, n))
	}
	if len(errs) > 0 {
		return Skill{}, invalid("%s", strings.Join(errs, "; "))
	}
	return Skill{
		Name: fm.Name, Description: strings.TrimSpace(fm.Description), Tools: fm.Tools,
		Conversations: fm.Conversations, Body: body, Source: source,
	}, nil
}
