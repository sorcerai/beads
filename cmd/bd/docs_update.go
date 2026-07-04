package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/types"
)

// docsUpdateCmd is Tier 1: called by .beads/hooks/post-close with the closed
// issue IDs. Advisory contract: NEVER fails, never blocks a close — every
// problem is a debug log line and exit 0.
var docsUpdateCmd = &cobra.Command{
	Use:    "update <issue-id>...",
	Short:  "Record closed issues in the wiki log (Tier 1, deterministic)",
	Args:   cobra.MinimumNArgs(1),
	Hidden: true, // plumbing: invoked by the post-close hook, not by hand
	Run: func(cmd *cobra.Command, args []string) {
		if os.Getenv("BD_NO_DOCS") == "1" || os.Getenv("BD_DOCS_RUNNING") == "1" {
			return
		}
		repoRoot := findRepoRootForArch()
		if repoRoot == "" {
			return
		}
		docsDir := docsDirName()
		statePath := docsStatePath(repoRoot, docsDir)
		st, ok := readDocsState(statePath)
		if !ok {
			debug.Logf("docs update: no %s — repo not opted in (run 'bd docs init')\n", statePath)
			return
		}

		wrote := 0
		epicClosed := false
		for _, id := range args {
			issue, err := store.GetIssue(rootCtx, id)
			if err != nil || issue == nil {
				debug.Logf("docs update: %s: %v\n", id, err)
				continue
			}
			if !docsIssueEligible(issue, st) {
				continue
			}
			parentID, deps := docsIssueLinks(rootCtx, issue)
			files := docsIssueFiles(rootCtx, repoRoot, id)
			if writeDocsEntryForIssue(rootCtx, repoRoot, docsDir, issue, parentID, deps, files) {
				wrote++
				if issue.IssueType == types.TypeEpic {
					epicClosed = true
				}
			}
		}
		if wrote == 0 {
			return
		}
		st.Dirty += wrote
		if err := writeDocsState(statePath, st); err != nil {
			debug.Logf("docs update: state write: %v\n", err)
		}
		compactDocsInbox(repoRoot, docsDir) // Task 5; stub as no-op until then
		if st.Dirty >= docsRegenThreshold() || epicClosed {
			fmt.Fprintf(os.Stderr, "wiki: %d closes since last regen — run 'bd docs regen'\n", st.Dirty)
		}
	},
}

// docsIssueEligible: closed, and not already consumed by a past regen.
// nil ClosedAt on a closed issue (legacy rows) is eligible — record it once.
func docsIssueEligible(issue *types.Issue, st docsState) bool {
	if issue == nil || issue.Status != types.StatusClosed {
		return false
	}
	if issue.ClosedAt != nil && !issue.ClosedAt.After(st.RegenWatermark) {
		return false
	}
	return true
}

// writeDocsEntryForIssue writes the entry unless it already exists.
// Existence (not byte equality) is the idempotence check: the files section
// derives from local git state and may differ across machines.
func writeDocsEntryForIssue(_ context.Context, repoRoot, docsDir string, issue *types.Issue, parentID string, deps, files []string) bool {
	p := docsEntryPath(repoRoot, docsDir, issue.ID)
	if _, err := os.Stat(p); err == nil {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		debug.Logf("docs update: mkdir: %v\n", err)
		return false
	}
	if err := os.WriteFile(p, renderDocsEntry(issue, parentID, deps, files), 0o600); err != nil {
		debug.Logf("docs update: write %s: %v\n", p, err)
		return false
	}
	return true
}

// docsIssueLinks extracts the parent epic + non-parent dependency IDs.
// Parent is the target of this issue's parent-child dependency edge, same
// convention as bd show's Parent computation (cmd/bd/show.go): a
// parent-child dependency has IssueID = child, DependsOnID = parent, so
// GetDependenciesWithMetadata(child) returns the parent among the child's
// "depends on" edges. Any other dependency type is recorded as a plain dep.
// Errors degrade to ("", nil) — an entry missing links is acceptable
// (advisory contract), never a reason to fail the close.
func docsIssueLinks(ctx context.Context, issue *types.Issue) (parentID string, deps []string) {
	withMeta, err := store.GetDependenciesWithMetadata(ctx, issue.ID)
	if err != nil {
		return "", nil
	}
	for _, dep := range withMeta {
		if dep.DependencyType == types.DepParentChild {
			parentID = dep.ID
			continue
		}
		deps = append(deps, dep.ID)
	}
	return parentID, deps
}

// docsIssueFiles asks bd explain's engine for associated files.
// Errors or empty results degrade to nil (noted inline by the renderer).
func docsIssueFiles(ctx context.Context, repoRoot, issueID string) []string {
	resp, err := explainIssueInWorkspace(ctx, repoRoot, issueID)
	if err != nil {
		debug.Logf("docs update: explain %s: %v\n", issueID, err)
		return nil
	}
	var out []string
	for _, f := range resp.Files {
		out = append(out, f.Path)
	}
	return out
}

// compactDocsInbox is a temporary no-op; Task 5 replaces it with the
// 200-entry cap -> backlog.md digest backstop.
func compactDocsInbox(repoRoot, docsDir string) {}

func init() {
	docsCmd.AddCommand(docsUpdateCmd)
}
