//go:build cgo

package main

import (
	"os"
	"strings"
	"testing"
)

// TestClaimPrintsCodeAndFilesFlagRecords covers the Task 19 half that lives on
// `bd update`: --files records manual links, --claim prints the CODE section,
// and --quiet suppresses that block.
func TestClaimPrintsCodeAndFilesFlagRecords(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cm")
	writeGoMini(t, dir)
	runCodemap(t, bd, dir, "build")

	id := createIssue(t, bd, dir, "claim")
	out := mustRunBD(t, bd, dir, "update", id, "--files", "a/a.go,b/b.go", "--claim")
	if !strings.Contains(out, "CODE") || !strings.Contains(out, "a/a.go") {
		t.Fatalf("claim output: %s", out)
	}

	if files := runCodemap(t, bd, dir, "files", id); !strings.Contains(files, "manual") {
		t.Fatalf("--files must record manual source: %s", files)
	}

	if q := mustRunBD(t, bd, dir, "update", id, "--claim", "--quiet"); strings.Contains(q, "CODE") {
		t.Error("--quiet must suppress the CODE block")
	}

	// JSON mode with --claim carries the same context under a "code" key.
	js := mustRunBD(t, bd, dir, "update", id, "--claim", "--json")
	var arr []struct {
		ID   string `json:"id"`
		Code struct {
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"code"`
	}
	mustJSONArray(t, js, &arr)
	if len(arr) == 0 || arr[0].ID != id || len(arr[0].Code.Files) == 0 {
		t.Fatalf("claim --json: %s", js)
	}
}
