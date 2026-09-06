//go:build cgo

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAgyResponse is what BD_CODEMAP_FAKE_AGY hands back for every batch. It
// covers the whole go-mini fixture, so a batch that asks about one file gets
// one usable item and two the parser drops as "not in this batch" — which is
// also the assertion that a path outside the batch never reaches the store.
const fakeAgyResponse = "```json\n" + `[
  {"path":"a/a.go","summary":"Package a's two exported no-op functions.","layer":"domain","tags":["fixture"]},
  {"path":"b/b.go","summary":"Package b prints a's function value.","layer":"cli"},
  {"path":"b/b_test.go","summary":"External test exercising b.Run.","layer":"test"}
]` + "\n```\n"

// runCodemapEnv is runCodemap with extra environment, which is how the fake
// model is wired in.
func runCodemapEnv(t *testing.T, bd, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	out, err := runBDEnvRaw(t, bd, dir, "", append(bdEnv(dir), extraEnv...), append([]string{"codemap"}, args...)...)
	if err != nil {
		t.Fatalf("bd codemap %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

// TestCodemapSummariesEndToEnd runs the summarizer pass against a canned model
// response: the flag is honored on both verbs, --max-files bounds the work,
// written summaries reach `show` and the derived cache, and a file edited after
// its summary goes stale and comes back.
func TestCodemapSummariesEndToEnd(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "cm")
	writeGoMini(t, dir)
	fake := filepath.Join(t.TempDir(), "agy.json")
	if err := os.WriteFile(fake, []byte(fakeAgyResponse), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"BD_CODEMAP_FAKE_AGY=" + fake}

	// A build WITHOUT --summaries must not call the model at all: the fake
	// points at a path that does not exist, so a call would fail the command.
	runCodemapEnv(t, bd, dir, []string{"BD_CODEMAP_FAKE_AGY=" + filepath.Join(dir, "nope.json")}, "build")

	// --max-files bounds the pass to the first candidate, a/a.go.
	out := runCodemapEnv(t, bd, dir, env, "build", "--summaries", "--max-files", "1")
	// The model label in the progress line is the one recorded on the row.
	if !strings.Contains(out, "Summarizing 1 files in 1 batches via agy (model gemini-3.1-pro)") || !strings.Contains(out, "1 written") {
		t.Fatalf("build --summaries --max-files 1: %s", out)
	}

	shown := runCodemap(t, bd, dir, "show", "a/a.go")
	// The layer renders through the category style, which upper-cases it.
	if !strings.Contains(shown, "Package a's two exported no-op functions.") || !strings.Contains(strings.ToLower(shown), "layer domain") {
		t.Fatalf("show after summaries: %s", shown)
	}
	entry := readCodemapCache(t, beadsDir).Files["a/a.go"]
	if entry.Summary == "" || entry.Layer != "domain" || entry.Stale {
		t.Fatalf("cache entry: %+v", entry)
	}
	if other := readCodemapCache(t, beadsDir).Files["b/b.go"]; other.Summary != "" {
		t.Fatalf("--max-files 1 summarized b/b.go too: %+v", other)
	}

	// refresh --summaries picks up the two files the cap left behind, even
	// though nothing has been committed since the build.
	out = runCodemapEnv(t, bd, dir, env, "refresh", "--summaries")
	if !strings.Contains(out, "Summarizing 2 files") || !strings.Contains(out, "2 written") {
		t.Fatalf("refresh --summaries: %s", out)
	}

	// Editing a summarized file makes its summary stale...
	appendFile(t, dir, "a/a.go", "\nfunc C() {}\n")
	gitCommitAll(t, dir, "change a", "a/a.go")
	runCodemap(t, bd, dir, "refresh")
	if stale := runCodemap(t, bd, dir, "stale"); !strings.Contains(stale, "1 stale summaries") {
		t.Fatalf("stale after edit: %s", stale)
	}

	// ...and another pass clears it.
	out = runCodemapEnv(t, bd, dir, env, "refresh", "--summaries")
	if !strings.Contains(out, "Summarizing 1 files") || !strings.Contains(out, "1 written") {
		t.Fatalf("second refresh --summaries: %s", out)
	}
	if stale := runCodemap(t, bd, dir, "stale"); !strings.Contains(stale, "0 stale summaries") {
		t.Fatalf("stale after resummarizing: %s", stale)
	}
	if entry := readCodemapCache(t, beadsDir).Files["a/a.go"]; entry.Stale || entry.Summary == "" {
		t.Fatalf("cache entry after resummarizing: %+v", entry)
	}
}

// TestCodemapSummariesNothingToDo: a second pass over a fully summarized map
// says so and exits 0 rather than reporting zero written as a failure.
func TestCodemapSummariesNothingToDo(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cm")
	writeGoMini(t, dir)
	fake := filepath.Join(t.TempDir(), "agy.json")
	if err := os.WriteFile(fake, []byte(fakeAgyResponse), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"BD_CODEMAP_FAKE_AGY=" + fake}

	runCodemapEnv(t, bd, dir, env, "build", "--summaries")
	out := runCodemapEnv(t, bd, dir, env, "refresh", "--summaries")
	if !strings.Contains(out, "Nothing to summarize") {
		t.Fatalf("second pass: %s", out)
	}
}
