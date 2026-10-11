// Package compilefail checks that misuse of the typed APIs is rejected by the Go compiler.
package compilefail

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var wantPattern = regexp.MustCompile(`// want: (.+)`)

func TestMisuseDoesNotCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("invokes the go tool")
	}
	cases, err := filepath.Glob("testdata/*/main.go")
	if err != nil || len(cases) == 0 {
		t.Fatalf("no cases found: %v", err)
	}
	for _, path := range cases {
		directory := filepath.Dir(path)
		t.Run(filepath.Base(directory), func(t *testing.T) {
			t.Parallel()
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			match := wantPattern.FindSubmatch(source)
			if match == nil {
				t.Fatalf("%s has no // want: comment", path)
			}
			output, err := exec.Command("go", "build", "-o", os.DevNull, "./"+directory).CombinedOutput()
			if err == nil {
				t.Fatalf("%s compiled, but it should not", path)
			}
			if !strings.Contains(string(output), strings.TrimSpace(string(match[1]))) {
				t.Fatalf("expected compiler error containing %q, got:\n%s", match[1], output)
			}
		})
	}
}
