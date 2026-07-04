package main

import (
	"context"
	"os"
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
