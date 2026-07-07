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
	st := docsState{RegenWatermark: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}
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

// TestWriteDocsEntryOverwritesOnReclose covers F4: a reopen + reclose (new
// ClosedAt) must overwrite the existing entry so every machine converges on
// the same Dolt-derived decision, instead of freezing the first close forever.
func TestWriteDocsEntryOverwritesOnReclose(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	iss := testIssueForEntry()

	if wrote := writeDocsEntryForIssue(context.Background(), repo, "wiki", iss, "", nil, []string{"a.go"}); !wrote {
		t.Fatal("first write should write")
	}
	p := docsEntryPath(repo, "wiki", iss.ID)

	// Same ClosedAt, different files: still a no-op (existing behavior).
	if wrote := writeDocsEntryForIssue(context.Background(), repo, "wiki", iss, "", nil, []string{"different.go"}); wrote {
		t.Fatal("unchanged ClosedAt must stay a no-op")
	}

	// Reopen + reclose: ClosedAt moves forward. Must overwrite.
	recloseTime := iss.ClosedAt.Add(24 * time.Hour)
	reclosed := *iss
	reclosed.ClosedAt = &recloseTime
	reclosed.CloseReason = "done: fixed for real this time"
	if wrote := writeDocsEntryForIssue(context.Background(), repo, "wiki", &reclosed, "", nil, []string{"a.go"}); !wrote {
		t.Fatal("differing ClosedAt (reopen+reclose) must overwrite, not no-op")
	}
	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("entry missing after overwrite: %v", err)
	}
	if !strings.Contains(string(after), recloseTime.UTC().Format(time.RFC3339)) {
		t.Fatalf("overwritten entry doesn't reflect the new closed time:\n%s", after)
	}
	if !strings.Contains(string(after), "fixed for real this time") {
		t.Fatalf("overwritten entry doesn't reflect the new close reason:\n%s", after)
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

// TestDocsUpdateRunNoDocsWritesNothing covers F9(a): BD_NO_DOCS=1 must return
// before any store access or entry write. store is left nil here — if the
// guard didn't hold, the first store.GetIssue call would panic, so a clean
// return (and an empty inbox) proves the guard fired.
func TestDocsUpdateRunNoDocsWritesNothing(t *testing.T) {
	repo := docsInitTestRepo(t)
	if err := runDocsInit(repo, "wiki"); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Setenv("BD_NO_DOCS", "1")
	docsUpdateCmd.Run(docsUpdateCmd, []string{"bx-1"})
	if docsInboxCount(repo, "wiki") != 0 {
		t.Fatal("BD_NO_DOCS=1 must write nothing")
	}
}

// TestDocsUpdateCoreGuardScope covers F9(b) and F9(d) together: the
// BD_DOCS_RUNNING scope suppresses only the stderr nudge, never the entry
// write, and an epic close nudges immediately even one entry below threshold.
func TestDocsUpdateCoreGuardScope(t *testing.T) {
	t.Parallel()

	// (b) suppressNudge=true: entry written, no nudge — even for an epic
	// close, which would otherwise nudge unconditionally (proves suppression
	// actually took effect rather than just not having met the threshold).
	repo := docsTestRepo(t)
	epic := testIssueForEntry()
	epic.ID = "bx-epic"
	epic.Status = types.StatusClosed
	epic.IssueType = types.TypeEpic
	stderr := captureStderr(t, func() {
		runDocsUpdateCore(repo, "wiki", []docsUpdateEntry{{issue: epic}}, true)
	})
	if _, err := os.Stat(docsEntryPath(repo, "wiki", epic.ID)); err != nil {
		t.Fatalf("entry not written despite BD_DOCS_RUNNING scope: %v", err)
	}
	if stderr != "" {
		t.Fatalf("nudge must be suppressed under BD_DOCS_RUNNING, got %q", stderr)
	}

	// (d) suppressNudge=false, epic close, inbox count (1) well below the
	// default threshold (10): nudge must still fire immediately.
	repo2 := docsTestRepo(t)
	epic2 := testIssueForEntry()
	epic2.ID = "bx-epic2"
	epic2.Status = types.StatusClosed
	epic2.IssueType = types.TypeEpic
	stderr2 := captureStderr(t, func() {
		runDocsUpdateCore(repo2, "wiki", []docsUpdateEntry{{issue: epic2}}, false)
	})
	if !strings.Contains(stderr2, "run 'bd docs regen'") {
		t.Fatalf("epic close below threshold must nudge immediately, got %q", stderr2)
	}
}

// TestDocsUpdateCoreNudgeAtThreshold covers F9(c): the nudge fires once
// the inbox count reaches docsRegenThreshold(), reporting that count.
func TestDocsUpdateCoreNudgeAtThreshold(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	threshold := docsRegenThreshold()

	// Seed threshold-1 pre-existing entries directly (not via the core, to
	// avoid nudging prematurely while seeding).
	for i := 0; i < threshold-1; i++ {
		iss := testIssueForEntry()
		iss.ID = fmt.Sprintf("bx-seed-%02d", i)
		if !writeDocsEntryForIssue(context.Background(), repo, "wiki", iss, "", nil, nil) {
			t.Fatalf("seed write %d failed", i)
		}
	}

	last := testIssueForEntry()
	last.ID = "bx-last"
	last.Status = types.StatusClosed
	stderr := captureStderr(t, func() {
		runDocsUpdateCore(repo, "wiki", []docsUpdateEntry{{issue: last}}, false)
	})
	want := fmt.Sprintf("wiki: %d closes since last regen — run 'bd docs regen'", threshold)
	if !strings.Contains(stderr, want) {
		t.Fatalf("nudge at threshold = %q, want to contain %q", stderr, want)
	}
}

// TestDocsNoteOfflineSkip covers F8: an issue closed before the watermark
// and never recorded gets one visible stderr line; a replay of an
// already-recorded entry stays silent.
func TestDocsNoteOfflineSkip(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	st, _ := readDocsState(docsStatePath(repo, "wiki"))

	iss := testIssueForEntry()
	iss.ID = "bx-neverrecorded"
	iss.Status = types.StatusClosed
	old := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // before the watermark
	iss.ClosedAt = &old

	stderr := captureStderr(t, func() {
		docsNoteOfflineSkip(repo, "wiki", iss, st)
	})
	if !strings.Contains(stderr, iss.ID) || !strings.Contains(stderr, "never recorded") {
		t.Fatalf("expected a visible skip line naming %s, got %q", iss.ID, stderr)
	}

	// Already recorded (existing entry file): must stay silent.
	recorded := testIssueForEntry()
	recorded.ID = "bx-replay"
	recorded.Status = types.StatusClosed
	recorded.ClosedAt = &old
	if !writeDocsEntryForIssue(context.Background(), repo, "wiki", recorded, "", nil, nil) {
		t.Fatal("seed write failed")
	}
	stderr2 := captureStderr(t, func() {
		docsNoteOfflineSkip(repo, "wiki", recorded, st)
	})
	if stderr2 != "" {
		t.Fatalf("replay of an already-recorded entry must stay silent, got %q", stderr2)
	}
}

// TestDocsWireHookMarkerDetection covers F5: detection keys on the exact
// managed marker line, not a loose "bd docs update" substring — a stray
// comment mentioning that phrase must not be mistaken for an already-wired hook.
func TestDocsWireHookMarkerDetection(t *testing.T) {
	repo := docsInitTestRepo(t)
	hookPath := filepath.Join(repo, ".beads", "hooks", "post-close")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o750); err != nil {
		t.Fatal(err)
	}
	preseeded := "#!/usr/bin/env sh\nset -eu\n# note: someone should wire bd docs update here later\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(preseeded), 0o755); err != nil {
		t.Fatal(err)
	}

	created, wired, err := wireDocsHook(repo)
	if err != nil {
		t.Fatalf("wireDocsHook: %v", err)
	}
	if created || !wired {
		t.Fatalf("a stray 'bd docs update' comment must not count as wired: created=%v wired=%v", created, wired)
	}
	hook, _ := os.ReadFile(hookPath)
	if strings.Count(string(hook), docsHookMarker) != 1 {
		t.Fatalf("managed marker must be spliced in exactly once:\n%s", hook)
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
