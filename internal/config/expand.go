package config

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Expand replaces ${VAR} and ${VAR:-default} references in s and turns $$ into
// a literal $. The default applies when VAR is unset or empty. A VAR without a
// default that lookup cannot find expands to "" and is reported in missing.
func Expand(s string, lookup func(string) (string, bool)) (out string, missing []string) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '$' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		switch s[i+1] {
		case '$':
			b.WriteByte('$')
			i++
		case '{':
			end := strings.IndexByte(s[i+2:], '}')
			if end < 0 {
				b.WriteString(s[i:])
				return b.String(), missing
			}
			ref := s[i+2 : i+2+end]
			i += 2 + end
			name, def, hasDef := strings.Cut(ref, ":-")
			v, ok := lookup(name)
			switch {
			case hasDef && v == "":
				b.WriteString(def)
			case ok:
				b.WriteString(v)
			default:
				missing = append(missing, name)
			}
		default:
			b.WriteByte('$')
		}
	}
	return b.String(), missing
}

// expandNode expands every scalar in the tree in place and returns the names of
// referenced variables that were unset, without duplicates, in order of first use.
// Plain scalars whose value changed are re-resolved so that, for example, an
// expanded number decodes into an int field; a non-empty value that would
// resolve to null (such as "null" or "~") stays a string.
func expandNode(n *yaml.Node, lookup func(string) (string, bool)) []string {
	var missing []string
	seen := map[string]bool{}
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.ScalarNode {
			v, m := Expand(n.Value, lookup)
			if v != n.Value {
				n.Value = v
				if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle|yaml.TaggedStyle) == 0 && n.Tag == "!!str" {
					n.Tag = ""
					if v != "" && n.ShortTag() == "!!null" {
						n.Tag = "!!str"
					}
				}
			}
			for _, name := range m {
				if !seen[name] {
					seen[name] = true
					missing = append(missing, name)
				}
			}
		}
		for _, c := range n.Content {
			walk(c)
		}
	}
	walk(n)
	return missing
}

// readDotEnv parses KEY=VALUE lines from path. A missing file yields an empty map.
// Blank lines and # comments are skipped; an "export " prefix and matching
// surrounding quotes are removed.
func readDotEnv(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	vars := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("%s:%d: expected KEY=VALUE", path, lineNo)
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		}
		vars[key] = val
	}
	return vars, sc.Err()
}
