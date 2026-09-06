//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// mustJSONArray decodes the first JSON array in out into v. mustCodemapJSON's
// sibling: that one seeks the first "{", which lands inside the array here.
func mustJSONArray(t *testing.T, out string, v any) {
	t.Helper()
	start := strings.Index(out, "[")
	if start < 0 {
		t.Fatalf("no JSON array in output: %s", out)
	}
	if err := json.Unmarshal([]byte(out[start:]), v); err != nil {
		t.Fatalf("decoding %s: %v", out[start:], err)
	}
}

// mustRunBD runs one bd command in dir through the built binary and returns
// stdout, failing the test on a non-zero exit. runCodemap's sibling for the
// verbs that are not under `bd codemap`.
func mustRunBD(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := runBDRaw(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), err, out, stderr)
	}
	return out
}

// TestShowPrintsCodeSectionAndJSON covers the Task 19 CODE section on
// `bd show`: the text block, the `code` key on the JSON payload, and the
// silence an issue with no linked files still gets.
func TestShowPrintsCodeSectionAndJSON(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cm")
	writeGoMini(t, dir)
	runCodemap(t, bd, dir, "build")

	id := createIssue(t, bd, dir, "show code")
	other := createIssue(t, bd, dir, "sibling")
	runCodemap(t, bd, dir, "link", id, "b/b.go")
	runCodemap(t, bd, dir, "link", other, "b/b.go")

	out := mustRunBD(t, bd, dir, "show", id)
	for _, want := range []string{"CODE", "b/b.go", "example.com/mini/a", "ALSO TOUCHING", other} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}

	js := mustRunBD(t, bd, dir, "show", id, "--json")
	var arr []struct {
		Code struct {
			Indexed bool `json:"Indexed"`
			Files   []struct {
				Path string `json:"Path"`
			} `json:"Files"`
		} `json:"code"`
	}
	mustJSONArray(t, js, &arr)
	if len(arr) == 0 || !arr[0].Code.Indexed || len(arr[0].Code.Files) == 0 || arr[0].Code.Files[0].Path != "b/b.go" {
		t.Fatalf("json: %s", js)
	}

	plain := mustRunBD(t, bd, dir, "show", createIssue(t, bd, dir, "no files"))
	if strings.Contains(plain, "CODE") {
		t.Error("issue without files must not print an empty CODE section")
	}
}
