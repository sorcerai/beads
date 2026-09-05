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

// TestCodemapQueriesAndLink covers the Task 16 read verbs plus the manual
// issue-to-file link they surface.
func TestCodemapQueriesAndLink(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cq")
	writeGoMini(t, dir)
	runCodemap(t, bd, dir, "build")

	id := createIssue(t, bd, dir, "Touch b")

	if out := runCodemap(t, bd, dir, "link", id, "b/b.go", "a/a.go"); !strings.Contains(out, "2 files") {
		t.Fatalf("link: %s", out)
	}
	if out := runCodemap(t, bd, dir, "files", id); !strings.Contains(out, "a/a.go") || !strings.Contains(out, "manual") {
		t.Fatalf("files: %s", out)
	}
	if out := runCodemap(t, bd, dir, "who", "b/b.go"); !strings.Contains(out, id) {
		t.Fatalf("who: %s", out)
	}

	show := runCodemap(t, bd, dir, "show", "b/b.go")
	if !strings.Contains(show, "imports") || !strings.Contains(show, "example.com/mini/a") || !strings.Contains(show, id) {
		t.Fatalf("show: %s", show)
	}

	deps := runCodemap(t, bd, dir, "deps", "a/a.go", "--reverse")
	if !strings.Contains(deps, "b/b.go") {
		t.Fatalf("reverse deps: %s", deps)
	}

	if out := runCodemap(t, bd, dir, "stale"); !strings.Contains(out, "0 stale") {
		t.Fatalf("stale: %s", out)
	}

	if out, err := runCodemapErr(t, bd, dir, "link", id, "../etc/passwd"); err == nil {
		t.Fatalf("escaping path accepted: %s", out)
	}
}
