package codemapops

import "testing"

func TestGraphValidate(t *testing.T) {
	ok := Graph{Lang: "go",
		Nodes: []Node{{Kind: NodePackage, Path: "a", Name: "a", Lang: "go"}, {Kind: NodeFile, Path: "a/x.go", Name: "x.go", PackagePath: "a", Lang: "go"}},
		Edges: []Edge{{Src: "a", Dst: "a/x.go", Kind: EdgeContains, Weight: 1}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid graph rejected: %v", err)
	}
	dangling := ok
	dangling.Edges = append(dangling.Edges, Edge{Src: "a", Dst: "missing", Kind: EdgeImports})
	if err := dangling.Validate(); err == nil {
		t.Fatal("dangling edge accepted")
	}
	dup := ok
	dup.Nodes = append(dup.Nodes, Node{Kind: NodePackage, Path: "a", Name: "a", Lang: "go"})
	if err := dup.Validate(); err == nil {
		t.Fatal("duplicate node accepted")
	}

	// Controller ruling: an edge whose Dst is listed in ExternalPackages is
	// valid even when no node has that path — it names a package an
	// incremental scan imports but did not rescan.
	external := ok
	external.ExternalPackages = []string{"external/pkg"}
	external.Edges = append(external.Edges, Edge{Src: "a", Dst: "external/pkg", Kind: EdgeImports})
	if err := external.Validate(); err != nil {
		t.Fatalf("edge to declared external package rejected: %v", err)
	}
}
