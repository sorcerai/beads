//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecordCommitLinksFilesAndRefreshes covers the Task 17 verb end to end: a
// commit whose message names an issue links that issue to the files the commit
// touched, and the same incremental rescan `bd codemap refresh` runs leaves the
// map up to date afterwards.
func TestRecordCommitLinksFilesAndRefreshes(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rc")
	writeGoMini(t, dir)
	runCodemap(t, bd, dir, "build")

	id := createIssue(t, bd, dir, "commit link")
	appendFile(t, dir, "a/a.go", "\nfunc C() {}\n")
	sha := gitCommitAll(t, dir, "feat: add C ("+id+")", "a/a.go")

	out := runCodemap(t, bd, dir, "record-commit", sha)
	if !strings.Contains(out, id) || !strings.Contains(out, "a/a.go") {
		t.Fatalf("record-commit: %s", out)
	}
	if files := runCodemap(t, bd, dir, "files", id); !strings.Contains(files, "commit") {
		t.Fatalf("source must be commit: %s", files)
	}
	if st := runCodemap(t, bd, dir, "status"); !strings.Contains(st, "up to date") {
		t.Fatalf("map must be refreshed after record-commit: %s", st)
	}
}

// TestRecordCommitSkipsUnknownIDsAndSurvivesGitHook pins the two failure
// contracts: an id that is not an issue here is reported and skipped rather
// than failing the run, and under BD_GIT_HOOK=1 a hard failure (a revision
// that does not exist) still exits 0 so a commit is never blocked.
func TestRecordCommitSkipsUnknownIDsAndSurvivesGitHook(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "rk")
	writeGoMini(t, dir)
	runCodemap(t, bd, dir, "build")

	appendFile(t, dir, "a/a.go", "\nfunc D() {}\n")
	sha := gitCommitAll(t, dir, "feat: add D (rk-nosuch)", "a/a.go")

	stdout, stderr, err := runBDRaw(t, bd, dir, "codemap", "record-commit", sha)
	if err != nil {
		t.Fatalf("an unknown id must be skipped, not fatal: %v\n%s\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "rk-nosuch") {
		t.Fatalf("unknown id must be reported on stderr: %s", stderr)
	}

	// A hard failure under the git hook exits 0: hooks never block a commit.
	out, hookErr := runBDStdin(t, bd, dir, "", "codemap", "record-commit", "definitely-not-a-rev")
	if hookErr == nil {
		t.Fatalf("a bad revision outside a hook must exit non-zero: %s", out)
	}
	cmdEnv := append(bdEnv(dir), "BD_GIT_HOOK=1")
	if out, err := runBDEnvRaw(t, bd, dir, cmdEnv, "codemap", "record-commit", "definitely-not-a-rev"); err != nil {
		t.Fatalf("BD_GIT_HOOK=1 must exit 0: %v\n%s", err, out)
	}
}

// TestHooksInstallBeadsWritesPostCommit pins that post-commit is a managed
// hook: `bd hooks install --beads` writes it, delegating to `bd hooks run`.
func TestHooksInstallBeadsWritesPostCommit(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "hk")
	writeGoMini(t, dir)

	if stdout, stderr, err := runBDRaw(t, bd, dir, "hooks", "install", "--beads"); err != nil {
		t.Fatalf("hooks install --beads: %v\n%s\n%s", err, stdout, stderr)
	}
	hookPath := filepath.Join(beadsDir, "hooks", "post-commit")
	body, err := os.ReadFile(hookPath) // #nosec G304 -- test-controlled path under t.TempDir()
	if err != nil {
		t.Fatalf("reading %s: %v", hookPath, err)
	}
	if !strings.Contains(string(body), "bd hooks run post-commit") {
		t.Fatalf("post-commit hook must delegate to bd hooks run: %s", body)
	}
}
