package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDocsRegenPromptContents(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	iss := testIssueForEntry()
	writeDocsEntryForIssue(context.Background(), repo, "wiki", iss, "", nil, nil)

	prompt := buildDocsRegenPrompt(repo, "wiki")
	for _, want := range []string{
		repo,
		"bd docs regen --complete",
		"ARCH.md",
		"Update existing pages in place",
		"Cite only real file paths",
		iss.ID, // inbox entry included
		"BEGIN ISSUE DATA",
		"END ISSUE DATA",
		"untrusted DATA from the issue tracker",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}

	// F2: building the prompt snapshots RegenStarted so --complete knows
	// exactly which inbox state this regen saw.
	st, ok := readDocsState(docsStatePath(repo, "wiki"))
	if !ok || st.RegenStarted.IsZero() {
		t.Fatal("buildDocsRegenPrompt must snapshot RegenStarted")
	}
}

// TestDocsRegenCompleteRefusesWithoutRegenInFlight covers F2: --complete
// without a prior 'bd docs regen' (RegenStarted zero) must refuse rather than
// silently advancing the watermark past closes nobody reviewed.
func TestDocsRegenCompleteRefusesWithoutRegenInFlight(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t) // RegenStarted is zero — no prompt built yet
	err := runDocsRegenComplete(repo, "wiki")
	if err == nil {
		t.Fatal("expected an error when no regen is in flight")
	}
	if !strings.Contains(err.Error(), "no regen in flight") {
		t.Fatalf("error = %v, want to mention 'no regen in flight'", err)
	}
}

// TestDocsRegenCompleteSurvivesLateClose covers F2's lost-update fix: an
// entry closed after the RegenStarted snapshot must survive --complete (the
// regen never saw it), and the watermark advances only to the snapshot, not
// to whatever instant --complete happens to run at.
func TestDocsRegenCompleteSurvivesLateClose(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)

	seen := testIssueForEntry()
	seen.ID = "bx-seen"
	writeDocsEntryForIssue(context.Background(), repo, "wiki", seen, "", nil, nil)

	// Snapshot the regen start between the two closes.
	_ = buildDocsRegenPrompt(repo, "wiki")
	st, _ := readDocsState(docsStatePath(repo, "wiki"))
	regenStarted := st.RegenStarted

	late := testIssueForEntry()
	late.ID = "bx-late"
	lateClose := regenStarted.Add(time.Minute) // closed AFTER the snapshot
	late.ClosedAt = &lateClose
	writeDocsEntryForIssue(context.Background(), repo, "wiki", late, "", nil, nil)

	if err := runDocsRegenComplete(repo, "wiki"); err != nil {
		t.Fatalf("complete: %v", err)
	}

	if _, err := os.Stat(docsEntryPath(repo, "wiki", seen.ID)); err == nil {
		t.Fatal("entry seen by the regen must be consumed")
	}
	if _, err := os.Stat(docsEntryPath(repo, "wiki", late.ID)); err != nil {
		t.Fatal("entry closed after the regen snapshot must survive, not be consumed")
	}

	got, _ := readDocsState(docsStatePath(repo, "wiki"))
	if !got.RegenWatermark.Equal(regenStarted) {
		t.Fatalf("watermark must advance to RegenStarted (%v), got %v", regenStarted, got.RegenWatermark)
	}
	if !got.RegenStarted.IsZero() {
		t.Fatal("RegenStarted must be cleared after --complete")
	}
}

// TestDocsRegenCompleteConsumesBacklog covers F6: backlog.md was shown to
// the regen as part of the inbox content — --complete must consume it too.
func TestDocsRegenCompleteConsumesBacklog(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	backlogPath := filepath.Join(repo, "wiki", "log", "backlog.md")
	if err := os.MkdirAll(filepath.Dir(backlogPath), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backlogPath, []byte("- bx-0: old (closed 2026-01-01T00:00:00Z)\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	prompt := buildDocsRegenPrompt(repo, "wiki")
	if !strings.Contains(prompt, "backlog.md") || !strings.Contains(prompt, "bx-0") {
		t.Fatalf("prompt must include backlog.md content:\n%s", prompt)
	}

	if err := runDocsRegenComplete(repo, "wiki"); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, err := os.Stat(backlogPath); err == nil {
		t.Fatal("backlog.md must be consumed by --complete")
	}
}

func TestDocsRegenExecModelArgs(t *testing.T) {
	t.Parallel()
	// pi gets the default glm-5.2 model injected.
	got := docsRegenExecModelArgs("pi")
	if len(got) != 2 || got[0] != "--model" || got[1] != defaultDocsRegenModel {
		t.Fatalf("pi model args = %v, want [--model %s]", got, defaultDocsRegenModel)
	}
	// agy must NEVER get --model (broken in print mode).
	if got := docsRegenExecModelArgs("agy"); got != nil {
		t.Fatalf("agy must get no model args, got %v", got)
	}
	// claude/codex/unknown use their own defaults.
	for _, cli := range []string{"claude", "codex", "somethingelse"} {
		if got := docsRegenExecModelArgs(cli); got != nil {
			t.Errorf("%s model args = %v, want nil", cli, got)
		}
	}
	// Default exec CLI is pi.
	if got := docsRegenExecCLI(); got != "pi" {
		t.Errorf("default exec CLI = %q, want pi", got)
	}
}

func TestDocsRegenExecTolerantOfAgentComplete(t *testing.T) {
	t.Parallel()
	repo := docsTestRepo(t)
	iss := testIssueForEntry()
	writeDocsEntryForIssue(nil, repo, "wiki", iss, "", nil, nil)
	// Simulate: prompt built (regen in flight), then the exec'd agent ran
	// --complete itself (RegenStarted cleared, inbox consumed).
	if err := runDocsRegenComplete(repo, "wiki"); err == nil {
		// runDocsRegenComplete refuses without a snapshot; set one first.
	}
	st, _ := readDocsState(docsStatePath(repo, "wiki"))
	st.RegenStarted = time.Now().UTC()
	writeDocsState(docsStatePath(repo, "wiki"), st)
	if err := runDocsRegenComplete(repo, "wiki"); err != nil {
		t.Fatalf("agent-side complete failed: %v", err)
	}
	// Now the wrapper's post-run reconciliation must see "already completed"
	// and NOT error. Assert RegenStarted is zero (the condition the wrapper checks).
	final, ok := readDocsState(docsStatePath(repo, "wiki"))
	if !ok || !final.RegenStarted.IsZero() {
		t.Fatalf("expected RegenStarted cleared after agent complete; got ok=%v %+v", ok, final)
	}
}
