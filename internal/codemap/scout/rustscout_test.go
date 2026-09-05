package scout

import (
	"os/exec"
	"testing"

	"github.com/steveyegge/beads/codemapops"
)

func TestRustScoutMini(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed")
	}
	root := copyFixture(t, "rust-mini")
	g, err := (RustScout{}).Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	has := func(kind, src, dst string) bool {
		for _, e := range g.Edges {
			if string(e.Kind) == kind && e.Src == src && e.Dst == dst {
				return true
			}
		}
		return false
	}
	if !has("imports", "cli", "core") || !has("imports", "cli/src/main.rs", "cli/src/args.rs") ||
		!has("imports", "cli/src/main.rs", "core/src/util.rs") || !has("contains", "core", "core/src/util.rs") {
		t.Errorf("edges: %+v", g.Edges)
	}
	if g.Dropped != 1 {
		t.Errorf("unresolved use must be counted, not guessed: dropped=%d", g.Dropped)
	}
	var util codemapops.Node
	for _, n := range g.Nodes {
		if n.Path == "core/src/util.rs" {
			util = n
		}
	}
	if util.PackagePath != "core" || util.BlobHash == "" || util.Exported != 1 {
		t.Errorf("file metadata wrong: %+v", util)
	}
	if !(RustScout{}).Detect(root) || (RustScout{}).Detect(t.TempDir()) {
		t.Error("Detect should key off Cargo.toml")
	}
}

// TestRustScoutIncrementalScope pins both spellings of `only`: a crate name and
// a repo-relative directory as PackageDirsFor produces.
func TestRustScoutIncrementalScope(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo not installed")
	}
	root := copyFixture(t, "rust-mini")
	for _, only := range [][]string{{"cli"}, PackageDirsFor([]string{"cli/src/main.rs"})} {
		g, err := (RustScout{}).Scan(root, only)
		if err != nil {
			t.Fatal(err)
		}
		if err := g.Validate(); err != nil {
			t.Fatalf("scope %v: %v", only, err)
		}
		crates := 0
		for _, n := range g.Nodes {
			if n.Kind == codemapops.NodePackage {
				crates++
				if n.Path != "cli" {
					t.Errorf("scope %v pulled in crate %s", only, n.Path)
				}
			}
		}
		if crates != 1 {
			t.Errorf("scope %v: %d crates, want 1", only, crates)
		}
		if len(g.ExternalPackages) != 1 || g.ExternalPackages[0] != "core" {
			t.Errorf("scope %v: unscanned dependency must be external, got %v", only, g.ExternalPackages)
		}
		// cli/src/lost.rs holds the one genuinely unresolvable path. main.rs's
		// `use core::util` resolves in a full scan, so scoping it out must make
		// it out of scope, not a drop.
		if g.Dropped != 1 {
			t.Errorf("scope %v: dropped=%d, want 1 (only lost.rs)", only, g.Dropped)
		}
	}
}
