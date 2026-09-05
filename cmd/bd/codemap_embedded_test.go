//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCodemapBuildStatusRefresh covers the Task 15 verbs end to end: a whole
// repository scan, the freshness report, and an incremental rescan that touches
// only the package whose files changed.
func TestCodemapBuildStatusRefresh(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cm")
	writeGoMini(t, dir)

	out := runCodemap(t, bd, dir, "build", "--json")
	var res struct {
		NodesUpserted int    `json:"nodes_upserted"`
		EdgesWritten  int    `json:"edges_written"`
		HeadSHA       string `json:"head_sha"`
	}
	mustCodemapJSON(t, out, &res)
	// SIX nodes, not the five the fixture has source files for: packages a and
	// b plus a/a.go, b/b.go and b/b_test.go, and then an EMPTY package node for
	// example.com/mini/gen. The go scout excludes gen/gen.pb.go as generated
	// but still emits the package that contained it, which
	// TestGoScoutMini never noticed because it asserts the five nodes it wants
	// are present rather than pinning the total.
	if res.NodesUpserted != 6 || res.EdgesWritten < 4 || res.HeadSHA == "" {
		t.Fatalf("build: %+v", res)
	}
	if _, err := os.Stat(filepath.Join(beadsDir, "codemap.cache.json")); err != nil {
		t.Fatalf("cache not written: %v", err)
	}

	status := runCodemap(t, bd, dir, "status")
	if !strings.Contains(status, "up to date") {
		t.Fatalf("status: %s", status)
	}

	appendFile(t, dir, "b/b.go", "\nfunc Extra() {}\n")
	gitCommitAll(t, dir, "change b", "b/b.go")

	status = runCodemap(t, bd, dir, "status")
	if !strings.Contains(status, "behind HEAD") {
		t.Fatalf("status after commit: %s", status)
	}

	out = runCodemap(t, bd, dir, "refresh", "--json")
	var inc struct {
		Only          []string `json:"only"`
		NodesUpserted int      `json:"nodes_upserted"`
	}
	mustCodemapJSON(t, out, &inc)
	if len(inc.Only) != 1 || inc.Only[0] != "example.com/mini/b" {
		t.Fatalf("refresh should rescan only package b: %+v", inc)
	}
	if inc.NodesUpserted == 0 {
		t.Fatalf("refresh upserted nothing: %+v", inc)
	}

	if status = runCodemap(t, bd, dir, "status"); !strings.Contains(status, "up to date") {
		t.Fatalf("status after refresh: %s", status)
	}
}
