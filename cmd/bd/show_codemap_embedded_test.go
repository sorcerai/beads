//go:build cgo

package main

import (
	"encoding/json"
	"os"
	"os/exec"
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
			Indexed bool `json:"indexed"`
			Files   []struct {
				Path string `json:"path"`
			} `json:"files"`
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

// TestShowBranchInferredFiles covers spec §9.4's branch source: with the issue
// id in the branch name, the files that branch changed join the CODE section
// even though nothing recorded them. They are computed per call and never
// written — a branch is a guess that dies with the branch — so the only place
// they can be observed is a rendering.
func TestShowBranchInferredFiles(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cm")
	writeGoMini(t, dir)
	runCodemap(t, bd, dir, "build")

	id := createIssue(t, bd, dir, "branch inference")
	gitCheckoutNew(t, dir, "feat/"+id)
	appendFile(t, dir, "b/b.go", "\nfunc OnBranch() {}\n")
	gitCommitAll(t, dir, "work on the branch", "b/b.go")

	out := mustRunBD(t, bd, dir, "show", id)
	for _, want := range []string{"CODE", "b/b.go", "◑"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}

	var arr []struct {
		Code struct {
			Files []struct {
				Path   string `json:"path"`
				Source string `json:"source"`
			} `json:"files"`
		} `json:"code"`
	}
	mustJSONArray(t, mustRunBD(t, bd, dir, "show", id, "--json"), &arr)
	if len(arr) != 1 || len(arr[0].Code.Files) != 1 {
		t.Fatalf("want exactly the one inferred file, got %+v", arr)
	}
	if arr[0].Code.Files[0].Path != "b/b.go" || arr[0].Code.Files[0].Source != "branch" {
		t.Errorf("inferred file = %+v, want b/b.go from the branch source", arr[0].Code.Files[0])
	}

	// An issue whose id is NOT in the branch name infers nothing, so the
	// inference cannot leak the branch's files onto every issue in the repo.
	other := createIssue(t, bd, dir, "not this branch")
	if plain := mustRunBD(t, bd, dir, "show", other); strings.Contains(plain, "CODE") {
		t.Errorf("an issue the branch does not name must infer no files:\n%s", plain)
	}
}

// gitCheckoutNew creates and switches to a branch.
func gitCheckoutNew(t *testing.T, dir, branch string) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "-q", "-b", branch)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b %s: %v\n%s", branch, err, out)
	}
}
