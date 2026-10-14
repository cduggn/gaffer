package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendLesson(t *testing.T) {
	path := filepath.Join(t.TempDir(), "standing.md")
	for _, l := range []string{"Name the data shape first.", "Run the tests.", " Name the data shape first. "} {
		if err := AppendLesson(path, l); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := os.ReadFile(path)
	if want := "- Name the data shape first.\n- Run the tests.\n"; string(got) != want {
		t.Fatalf("standing.md = %q, want %q", got, want)
	}
	if err := AppendLesson(path, "two\nlines"); err == nil {
		t.Fatal("multi-line lesson accepted")
	}
}

func TestAgentExpand(t *testing.T) {
	a := Agent{Command: []string{"x", "--add-dir", "{repo}", "--cwd={worktree}"}}
	got := a.Expand("/r", "/w")
	if got[2] != "/r" || got[3] != "--cwd=/w" {
		t.Fatalf("Expand = %q", got)
	}
}
