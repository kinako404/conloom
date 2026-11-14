package web

import (
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/kinako404/conloom/internal/memory"
)

// assets returns the embedded files with one of exts, keyed by path.
func assets(t *testing.T, exts ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		for _, ext := range exts {
			if path.Ext(p) == ext {
				b, err := fs.ReadFile(FS, p)
				if err != nil {
					return err
				}
				out[p] = string(b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAssets_PlannedFilesPresent(t *testing.T) {
	for _, name := range []string{
		"index.html", "style.css", "app.js",
		"lib/api.js", "lib/router.js", "lib/dom.js", "lib/chart.js", "lib/format.js",
		"views/login.js", "views/overview.js", "views/usage.js", "views/conversations.js",
		"views/conversation.js", "views/tasks.js", "views/audit.js", "views/config.js",
	} {
		if _, err := fs.Stat(FS, name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAssets_IndexPresentNoInlineScript(t *testing.T) {
	b, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(b)
	scripts := regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script\s*>`).FindAllStringSubmatch(html, -1)
	if len(scripts) == 0 {
		t.Fatal("index.html loads no script")
	}
	if n := strings.Count(strings.ToLower(html), "<script"); n != len(scripts) {
		t.Fatalf("index.html has %d <script tags but %d complete elements", n, len(scripts))
	}
	src := regexp.MustCompile(`(?i)\ssrc\s*=\s*"[^"]+"`)
	for _, s := range scripts {
		if !src.MatchString(s[1]) {
			t.Errorf("script without src: %q", s[0])
		}
		if strings.TrimSpace(s[2]) != "" {
			t.Errorf("inline script code: %q", s[0])
		}
	}
	for _, bad := range []struct{ name, re string }{
		{"style attribute", `(?i)\sstyle\s*=`},
		{"event handler attribute", `(?i)\son[a-z]+\s*=`},
		{"style element", `(?i)<style\b`},
	} {
		if m := regexp.MustCompile(bad.re).FindString(html); m != "" {
			t.Errorf("index.html: %s %q", bad.name, m)
		}
	}
}

func TestAssets_NoExternalOrUnsafeAPIs(t *testing.T) {
	files := assets(t, ".js", ".html", ".css")
	if len(files) < 3 {
		t.Fatalf("only %d assets embedded", len(files))
	}
	forbidden := []string{
		"http://", "https://",
		"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write",
		"eval(", "new Function",
	}
	for name, text := range files {
		for _, f := range forbidden {
			if strings.Contains(text, f) {
				t.Errorf("%s contains %q", name, f)
			}
		}
	}
}

var importRe = regexp.MustCompile(`(?s)\b(?:import|export)\s+(?:[^'";]*?\s+from\s+)?['"]([^'"]+)['"]|\bimport\(\s*['"]([^'"]+)['"]\s*\)`)

func TestAssets_ImportsResolve(t *testing.T) {
	files := assets(t, ".js")
	imports := 0
	for name, text := range files {
		for _, m := range importRe.FindAllStringSubmatch(text, -1) {
			spec := m[1] + m[2]
			imports++
			if !strings.HasPrefix(spec, "./") && !strings.HasPrefix(spec, "../") {
				t.Errorf("%s imports %q, want a relative path", name, spec)
				continue
			}
			target := path.Join(path.Dir(name), spec)
			if _, err := fs.Stat(FS, target); err != nil {
				t.Errorf("%s imports %q: %s is not embedded", name, spec, target)
			}
		}
	}
	if imports == 0 {
		t.Fatal("no imports found")
	}
	html, err := fs.ReadFile(FS, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?i)\s(?:src|href)\s*=\s*"([^"]+)"`).FindAllStringSubmatch(string(html), -1) {
		if strings.HasPrefix(m[1], "data:") || strings.HasPrefix(m[1], "#") {
			continue
		}
		if _, err := fs.Stat(FS, path.Clean(m[1])); err != nil {
			t.Errorf("index.html references %q, which is not embedded", m[1])
		}
	}
}

func TestAssets_InitialAffinityMatchesMemory(t *testing.T) {
	js := assets(t, ".js")["views/conversation.js"]
	m := regexp.MustCompile(`const INITIAL_AFFINITY = (\d+);`).FindStringSubmatch(js)
	if m == nil {
		t.Fatal("views/conversation.js has no INITIAL_AFFINITY")
	}
	if want := strconv.Itoa(memory.InitialAffinity); m[1] != want {
		t.Errorf("INITIAL_AFFINITY = %s, want memory.InitialAffinity %s", m[1], want)
	}
}
