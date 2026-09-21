package web

import (
	"io/fs"
	"os/exec"
	"testing"
)

// TestEmbed_CompilesWithoutBuild is mostly a compile-time proof: if the embed
// pattern matched nothing on an unbuilt tree the package would not build. At
// runtime it checks the tree is either a real build or just the placeholder.
func TestEmbed_CompilesWithoutBuild(t *testing.T) {
	entries, err := fs.ReadDir(Dist(), ".")
	if err != nil {
		t.Fatalf("ReadDir(Dist()): %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if names["index.html"] {
		return
	}
	if len(entries) != 1 || !names[".gitkeep"] {
		t.Fatalf("unbuilt dist holds %v, want only .gitkeep (or a build with index.html)", names)
	}
}

func TestEmbed_GitkeepTracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	out, err := exec.Command("git", "-C", "..", "ls-files", "web/dist/.gitkeep").Output()
	if err != nil {
		t.Skipf("git ls-files: %v (not a checkout?)", err)
	}
	if string(out) != "web/dist/.gitkeep\n" {
		t.Fatalf("git ls-files web/dist/.gitkeep = %q, want the path (is the gitignore exception missing?)", out)
	}
	if err := exec.Command("git", "-C", "..", "check-ignore", "-q", "web/dist/assets/x.js").Run(); err != nil {
		t.Fatalf("git check-ignore web/dist/assets/x.js: %v, want ignored (exit 0)", err)
	}
}
