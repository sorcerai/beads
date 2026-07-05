package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// stateAndEntrySetup seeds a repo dir with an opted-in wiki.
func docsTestRepo(t *testing.T) (repoRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	st := docsState{RegenWatermark: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), Dirty: 0}
	if err := writeDocsState(docsStatePath(repoRoot, "wiki"), st); err != nil {
		t.Fatal(err)
	}
	return repoRoot
}

func TestWriteDocsEntryIdempotent(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	iss := testIssueForEntry() // from docs_entry_test.go

	if wrote := writeDocsEntryForIssue(context.Background(), repo, "wiki", iss, "", nil, []string{"a.go"}); !wrote {
		t.Fatal("first write should write")
	}
	p := docsEntryPath(repo, "wiki", iss.ID)
	before, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("entry not written: %v", err)
	}
	// Second call: file exists -> no rewrite even with different files list.
	if wrote := writeDocsEntryForIssue(context.Background(), repo, "wiki", iss, "", nil, []string{"different.go"}); wrote {
		t.Fatal("second write must be a no-op (existence idempotence)")
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("existing entry was rewritten")
	}
}

func TestDocsUpdateSkipsPreWatermarkClose(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	iss := testIssueForEntry()
	iss.Status = types.StatusClosed
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // before the 2026-07-01 watermark
	iss.ClosedAt = &old

	st, _ := readDocsState(docsStatePath(repo, "wiki"))
	if docsIssueEligible(iss, st) {
		t.Fatal("issue closed before regen watermark must be skipped (already consumed)")
	}
}

func TestDocsRegenThresholdDefault(t *testing.T) {
	if got := docsRegenThreshold(); got != 10 {
		t.Fatalf("default threshold = %d, want 10", got)
	}
}

// docsInitTestRepo isolates wireDocsHook's beads.FindBeadsDir() (which walks
// from BEADS_DIR/cwd, not the repoRoot argument) so it can't wander off into
// this checkout's own .beads dir. Not t.Parallel(): Setenv/Chdir forbid it.
func docsInitTestRepo(t *testing.T) (repoRoot string) {
	t.Helper()
	repoRoot = t.TempDir()
	t.Setenv("BEADS_DIR", "")
	t.Chdir(repoRoot)
	return repoRoot
}

func TestDocsInitIdempotent(t *testing.T) {
	repo := docsInitTestRepo(t)
	if err := runDocsInit(repo, "wiki"); err != nil {
		t.Fatalf("first init: %v", err)
	}
	st1, ok := readDocsState(docsStatePath(repo, "wiki"))
	if !ok {
		t.Fatal("state not created")
	}
	hook, err := os.ReadFile(filepath.Join(repo, ".beads", "hooks", "post-close"))
	if err != nil || !strings.Contains(string(hook), "bd docs update") {
		t.Fatalf("hook not wired: %v\n%s", err, hook)
	}
	if strings.Contains(string(hook), "pipefail") {
		t.Fatal("hook must be POSIX sh — no pipefail")
	}
	// Second init: nothing resets, docs block not duplicated.
	if err := runDocsInit(repo, "wiki"); err != nil {
		t.Fatalf("second init: %v", err)
	}
	st2, _ := readDocsState(docsStatePath(repo, "wiki"))
	if !st1.RegenWatermark.Equal(st2.RegenWatermark) {
		t.Fatal("re-init must not reset the watermark")
	}
	hook2, _ := os.ReadFile(filepath.Join(repo, ".beads", "hooks", "post-close"))
	if strings.Count(string(hook2), "bd docs update") != 1 {
		t.Fatal("docs block duplicated on re-init")
	}
}

// TestDocsInitSplicesBeforeExistingExit0 covers the case 'bd arch init' (or
// anything else) seeded a hook first: the docs block must land BEFORE a
// trailing "exit 0", since sh's exit terminates the script immediately —
// appending after it would silently make Tier 1 dead code.
func TestDocsInitSplicesBeforeExistingExit0(t *testing.T) {
	repo := docsInitTestRepo(t)
	hookPath := filepath.Join(repo, ".beads", "hooks", "post-close")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o750); err != nil {
		t.Fatal(err)
	}
	preseeded := "#!/usr/bin/env sh\nset -eu\necho hi\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(preseeded), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := runDocsInit(repo, "wiki"); err != nil {
		t.Fatalf("init: %v", err)
	}
	hook, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("hook missing: %v", err)
	}
	content := string(hook)
	markerIdx := strings.Index(content, "bd docs update")
	exitIdx := strings.LastIndex(content, "exit 0")
	if markerIdx == -1 || exitIdx == -1 || markerIdx > exitIdx {
		t.Fatalf("docs block must precede the trailing exit 0:\n%s", content)
	}

	// Re-init: still not duplicated.
	if err := runDocsInit(repo, "wiki"); err != nil {
		t.Fatalf("second init: %v", err)
	}
	hook2, _ := os.ReadFile(hookPath)
	if strings.Count(string(hook2), "bd docs update") != 1 {
		t.Fatal("docs block duplicated on re-init")
	}
}

func TestCompactDocsInbox(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	base := time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 205; i++ {
		iss := testIssueForEntry()
		iss.ID = fmt.Sprintf("bx-%03d", i)
		c := base.Add(time.Duration(i) * time.Minute)
		iss.ClosedAt = &c
		if !writeDocsEntryForIssue(context.Background(), repo, "wiki", iss, "", nil, nil) {
			t.Fatalf("seed write %d failed", i)
		}
	}
	compactDocsInbox(repo, "wiki")
	entries, _ := filepath.Glob(filepath.Join(repo, "wiki", "log", "bx-*.md"))
	if len(entries) != 200 {
		t.Fatalf("want 200 entries after compaction, got %d", len(entries))
	}
	backlog, err := os.ReadFile(filepath.Join(repo, "wiki", "log", "backlog.md"))
	if err != nil {
		t.Fatalf("backlog.md missing: %v", err)
	}
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("bx-%03d", i)
		if !strings.Contains(string(backlog), id) {
			t.Errorf("oldest %s not in backlog", id)
		}
		if _, err := os.Stat(docsEntryPath(repo, "wiki", id)); err == nil {
			t.Errorf("oldest %s still in log/", id)
		}
	}
}

// TestDocsStalenessOnWiki covers checkMarkdownStaleness + docsWikiMarkdownFiles
// together: a dangling backtick ref is flagged, a real one is not, and log/
// entries (which are per-issue records, not wiki pages) are excluded from the
// scan entirely.
func TestDocsStalenessOnWiki(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	wikiDir := filepath.Join(repo, "wiki")
	if err := os.MkdirAll(filepath.Join(wikiDir, "log"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mdPath := filepath.Join(wikiDir, "architecture.md")
	content := "See `cmd/nonexistent/thing.go` and `README.md`.\n"
	if err := os.WriteFile(mdPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	// log/ entries are per-issue records, not wiki pages — must be excluded.
	logEntry := filepath.Join(wikiDir, "log", "bx-1.md")
	if err := os.WriteFile(logEntry, []byte("`cmd/nonexistent/other.go`"), 0o644); err != nil {
		t.Fatal(err)
	}

	files := docsWikiMarkdownFiles(repo, "wiki")
	if len(files) != 1 || files[0] != mdPath {
		t.Fatalf("docsWikiMarkdownFiles = %v, want [%s]", files, mdPath)
	}

	findings := checkMarkdownStaleness(repo, files)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %d: %v", len(findings), findings)
	}
	if !strings.Contains(findings[0], "cmd/nonexistent/thing.go") {
		t.Fatalf("finding doesn't name the dangling ref: %v", findings)
	}
}
