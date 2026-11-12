package app_test

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const modulePath = "github.com/kinako404/conloom"

// corePackages must stay independent of any platform adapter and model vendor.
var corePackages = []string{
	"core", "gateway", "router", "contextbuilder", "persona", "policy", "review", "outbox", "store", "admission",
}

var vendorNames = regexp.MustCompile(`(?i)onebot|napcat|qwen|deepseek|grok`)

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func TestArch_CoreHasNoPlatformOrModelNames(t *testing.T) {
	root := moduleRoot(t)
	for _, pkg := range corePackages {
		dir := filepath.Join(root, "internal", pkg)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(data), "\n") {
				if m := vendorNames.FindString(line); m != "" {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%d mentions %q", rel, i+1, m)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan internal/%s: %v", pkg, err)
		}
	}

	args := []string{"list", "-deps"}
	for _, pkg := range corePackages {
		args = append(args, modulePath+"/internal/"+pkg+"/...")
	}
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}
	deps := strings.Fields(string(out))
	if len(deps) == 0 {
		t.Fatal("go list -deps returned no packages")
	}
	for _, dep := range deps {
		if strings.HasPrefix(dep, modulePath+"/internal/adapter") {
			t.Errorf("a core package depends on %s", dep)
		}
	}
}
