package scout

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/codemapops"
)

func TestGoScoutMini(t *testing.T) {
	root := copyFixture(t, "go-mini")
	g, err := (GoScout{}).Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	want := map[string]codemapops.NodeKind{
		"example.com/mini/a": codemapops.NodePackage, "example.com/mini/b": codemapops.NodePackage,
		"a/a.go": codemapops.NodeFile, "b/b.go": codemapops.NodeFile, "b/b_test.go": codemapops.NodeFile,
	}
	got := map[string]codemapops.Node{}
	for _, n := range g.Nodes {
		got[n.Path] = n
		if n.Path == "gen/gen.pb.go" || strings.HasPrefix(n.Path, "vendor/") {
			t.Errorf("excluded file indexed: %s", n.Path)
		}
	}
	for p, k := range want {
		if got[p].Kind != k {
			t.Errorf("missing %s %s", k, p)
		}
	}
	if got["a/a.go"].Exported != 2 || !got["b/b_test.go"].IsTest || got["a/a.go"].BlobHash == "" {
		t.Errorf("file metadata wrong: %+v %+v", got["a/a.go"], got["b/b_test.go"])
	}
	edges := map[string]bool{}
	for _, e := range g.Edges {
		edges[string(e.Kind)+" "+e.Src+" "+e.Dst] = true
	}
	for _, e := range []string{
		"imports example.com/mini/b example.com/mini/a", "imports b/b.go example.com/mini/a",
		"contains example.com/mini/a a/a.go", "tests b/b_test.go example.com/mini/b",
	} {
		if !edges[e] {
			t.Errorf("missing edge %q", e)
		}
	}
	if edges["imports b/b.go fmt"] {
		t.Error("stdlib import must not be an edge")
	}

	only, err := (GoScout{}).Scan(root, []string{"example.com/mini/b"})
	if err != nil || len(only.Nodes) != 3 { // package b, b.go, b_test.go
		t.Fatalf("incremental scan: %d nodes %v", len(only.Nodes), err)
	}
	if err := only.Validate(); err != nil {
		t.Fatalf("incremental graph invalid: %v", err)
	}
	if len(only.ExternalPackages) != 1 || only.ExternalPackages[0] != "example.com/mini/a" {
		t.Errorf("imported-but-not-rescanned package must be declared external: %v", only.ExternalPackages)
	}
	if len(only.ScopedPackages) != 1 || only.ScopedPackages[0] != "example.com/mini/b" {
		t.Errorf("scan scope must be reported: %v", only.ScopedPackages)
	}

	// A scope whose directory no longer exists still has to name its package,
	// or the incremental apply has nothing to scope its delete to.
	gone, err := (GoScout{}).Scan(root, []string{"nosuchdir"})
	if err != nil {
		t.Fatal(err)
	}
	if len(gone.ScopedPackages) != 1 || gone.ScopedPackages[0] != "example.com/mini/nosuchdir" {
		t.Errorf("a missing directory must still appear in ScopedPackages: %v", gone.ScopedPackages)
	}
}

// TestGoScoutIncrementalByDirectory pins the handoff from PackageDirsFor, whose
// output is repo-relative directories rather than import paths.
func TestGoScoutIncrementalByDirectory(t *testing.T) {
	root := copyFixture(t, "go-mini")
	g, err := (GoScout{}).Scan(root, PackageDirsFor([]string{"b/b.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("directory scope must scan package b: %d nodes %+v", len(g.Nodes), g.Nodes)
	}
	if err := g.Validate(); err != nil {
		t.Fatal(err)
	}
	empty, err := (GoScout{}).Scan(root, []string{})
	if err != nil || len(empty.Nodes) != 0 {
		t.Fatalf("empty scope must scan nothing: %d nodes %v", len(empty.Nodes), err)
	}
}

func TestGoScoutModulePathAndDetect(t *testing.T) {
	root := copyFixture(t, "go-mini")
	mod, err := ModulePath(root)
	if err != nil || mod != "example.com/mini" {
		t.Fatalf("ModulePath = %q, %v", mod, err)
	}
	if !(GoScout{}).Detect(root) {
		t.Error("Detect should find go.mod")
	}
	if (GoScout{}).Detect(t.TempDir()) {
		t.Error("Detect should reject a dir with no go.mod")
	}
}
