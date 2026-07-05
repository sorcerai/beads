package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

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
		if os.Getenv("BD_NO_DOCS") == "1" {
			return
		}
		repoRoot := findRepoRootForArch()
		if repoRoot == "" {
			return
		}
		docsDir := docsDirName()

		entries := make([]docsUpdateEntry, 0, len(args))
		for _, id := range args {
			issue, err := store.GetIssue(rootCtx, id)
			if err != nil || issue == nil {
				debug.Logf("docs update: %s: %v\n", id, err)
				continue
			}
			parentID, deps := docsIssueLinks(rootCtx, issue)
			files := docsIssueFiles(rootCtx, repoRoot, id)
			entries = append(entries, docsUpdateEntry{issue: issue, parentID: parentID, deps: deps, files: files})
		}
		// BD_DOCS_RUNNING scopes to the nudge only (reentrancy guard, F3): a
		// headless regen (--exec) sets this while it runs so its own bd calls
		// don't re-trigger a nested regen. But entries must still be written —
		// skipping the write here meant the agent's own closes during --exec
		// got no entry, and since the post-close fired-ledger already marks
		// the hook as run, the sweep never replays it: a permanent loss, not
		// a deferral. Only the stderr nudge (which would otherwise talk back
		// at the very session already mid-regen) is suppressed.
		runDocsUpdateCore(repoRoot, docsDir, entries, os.Getenv("BD_DOCS_RUNNING") == "1")
	},
}

// docsUpdateEntry bundles one already-resolved issue plus the links/files
// docsUpdateCmd.Run gathered for it via store, so runDocsUpdateCore stays
// store-free and unit-testable without a Dolt store.
type docsUpdateEntry struct {
	issue    *types.Issue
	parentID string
	deps     []string
	files    []string
}

// runDocsUpdateCore is the testable heart of Tier 1: writes entries for
// already-resolved issues, then — unless suppressNudge (BD_DOCS_RUNNING,
// F3) — prints the regen nudge derived from the current inbox size.
func runDocsUpdateCore(repoRoot, docsDir string, entries []docsUpdateEntry, suppressNudge bool) {
	statePath := docsStatePath(repoRoot, docsDir)
	st, ok := readDocsState(statePath)
	if !ok {
		debug.Logf("docs update: no %s — repo not opted in (run 'bd docs init')\n", statePath)
		return
	}

	wrote := 0
	epicClosed := false
	for _, e := range entries {
		if !docsIssueEligible(e.issue, st) {
			docsNoteOfflineSkip(repoRoot, docsDir, e.issue, st)
			continue
		}
		if writeDocsEntryForIssue(context.Background(), repoRoot, docsDir, e.issue, e.parentID, e.deps, e.files) {
			wrote++
			if e.issue.IssueType == types.TypeEpic {
				epicClosed = true
			}
		}
	}
	if wrote == 0 {
		return
	}
	compactDocsInbox(repoRoot, docsDir)
	if suppressNudge {
		return
	}
	count := docsInboxCount(repoRoot, docsDir)
	if count >= docsRegenThreshold() || epicClosed {
		fmt.Fprintf(os.Stderr, "wiki: %d closes since last regen — run 'bd docs regen'\n", count)
	}
}

// docsNoteOfflineSkip prints one visible stderr line (F8) when an issue is
// excluded from the wiki log solely because it closed before the regen
// watermark AND was never recorded here — e.g. a close synced in from
// another machine after this machine's own regen already advanced past it.
// That closure will never be written by Tier 1, unlike an existing-file
// replay (the ordinary post-close-hook re-fire case), which stays silent.
func docsNoteOfflineSkip(repoRoot, docsDir string, issue *types.Issue, st docsState) {
	if issue == nil || issue.Status != types.StatusClosed || issue.ClosedAt == nil {
		return
	}
	if issue.ClosedAt.After(st.RegenWatermark) {
		return // eligible; not the watermark-skip case
	}
	if _, err := os.Stat(docsEntryPath(repoRoot, docsDir, issue.ID)); err == nil {
		return // already recorded — silent replay
	}
	fmt.Fprintf(os.Stderr, "wiki: skipping %s (closed before last regen and never recorded) — recover with 'bd docs log --write --since <ts>'\n", issue.ID)
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

// writeDocsEntryForIssue writes the entry. Idempotence check (F4): if the
// entry already exists, compare its recorded closed timestamp against the
// issue's current ClosedAt — identical means this exact close event was
// already recorded (no-op, even if the files section would render
// differently: that section derives from local git state and may differ
// across machines). Different means the issue was reopened and reclosed
// since; overwrite so every machine converges on the same Dolt-derived
// decision instead of freezing the first close forever.
func writeDocsEntryForIssue(_ context.Context, repoRoot, docsDir string, issue *types.Issue, parentID string, deps, files []string) bool {
	p := docsEntryPath(repoRoot, docsDir, issue.ID)
	if data, err := os.ReadFile(p); err == nil { // #nosec G304 -- p is <docsDir>/log/<issueID>.md, constructed by docsEntryPath.
		_, _, existingClosed := parseDocsEntryHeader(string(data))
		if docsClosedTimesMatch(existingClosed, issue.ClosedAt) {
			return false
		}
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

// docsClosedTimesMatch compares a parsed entry's closed timestamp against an
// issue's current ClosedAt, nil-safe: renderDocsEntry writes "-" for a nil
// ClosedAt, which parseDocsEntryHeader reads back as the zero time.Time.
func docsClosedTimesMatch(existingClosed time.Time, issueClosedAt *time.Time) bool {
	if issueClosedAt == nil {
		return existingClosed.IsZero()
	}
	return existingClosed.Equal(issueClosedAt.UTC())
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

// docsInboxCount is the dirty count (F1): the number of log/*.md entries,
// excluding backlog.md. Deriving it from the filesystem instead of a stored
// counter removes the unsynchronized read-modify-write race between
// concurrent 'bd docs update' invocations — the inbox itself is the counter.
func docsInboxCount(repoRoot, docsDir string) int {
	entries, err := os.ReadDir(filepath.Join(repoRoot, docsDir, "log"))
	if err != nil {
		return 0
	}
	n := 0
	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || name == "backlog.md" || !strings.HasSuffix(name, ".md") {
			continue
		}
		n++
	}
	return n
}

// docsInboxCap is the entry-file ceiling for <docsDir>/log/ before the oldest
// overflow gets digested into backlog.md. Backstop only: Tier 2 regen is the
// normal way the inbox drains; this exists for repos where regen lags.
const docsInboxCap = 200

// docsInboxEntry is one parsed log/ file, just enough to sort and digest it.
type docsInboxEntry struct {
	path   string
	id     string
	title  string
	closed time.Time
}

// compactDocsInbox keeps wiki/log/ bounded: once more than docsInboxCap
// entries accumulate, the oldest overflow (by closed timestamp, then ID) is
// appended as one digest line each to log/backlog.md and the original files
// removed. Advisory: any error just debug-logs and returns.
func compactDocsInbox(repoRoot, docsDir string) {
	logDir := filepath.Join(repoRoot, docsDir, "log")
	dirEntries, err := os.ReadDir(logDir)
	if err != nil {
		debug.Logf("docs compact: readdir %s: %v\n", logDir, err)
		return
	}

	var all []docsInboxEntry
	for _, de := range dirEntries {
		name := de.Name()
		if de.IsDir() || name == "backlog.md" || !strings.HasSuffix(name, ".md") {
			continue
		}
		p := filepath.Join(logDir, name)
		data, err := os.ReadFile(p) // #nosec G304 -- p is under <docsDir>/log/, just listed by ReadDir.
		if err != nil {
			continue
		}
		id, title, closed := parseDocsEntryHeader(string(data))
		all = append(all, docsInboxEntry{path: p, id: id, title: title, closed: closed})
	}

	overflow := len(all) - docsInboxCap
	if overflow <= 0 {
		return
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].closed.Equal(all[j].closed) {
			return all[i].closed.Before(all[j].closed)
		}
		return all[i].id < all[j].id
	})
	oldest := all[:overflow]

	var digest strings.Builder
	for _, e := range oldest {
		fmt.Fprintf(&digest, "- %s: %s (closed %s)\n", e.id, e.title, e.closed.UTC().Format(time.RFC3339))
	}
	backlogPath := filepath.Join(logDir, "backlog.md")
	f, err := os.OpenFile(backlogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- backlogPath is <docsDir>/log/backlog.md, constructed by us.
	if err != nil {
		debug.Logf("docs compact: open %s: %v\n", backlogPath, err)
		return
	}
	_, writeErr := f.WriteString(digest.String())
	closeErr := f.Close()
	if writeErr != nil {
		debug.Logf("docs compact: write %s: %v\n", backlogPath, writeErr)
		return
	}
	if closeErr != nil {
		debug.Logf("docs compact: close %s: %v\n", backlogPath, closeErr)
		return
	}

	for _, e := range oldest {
		if err := os.Remove(e.path); err != nil {
			debug.Logf("docs compact: remove %s: %v\n", e.path, err)
		}
	}
}

// parseDocsEntryHeader pulls the id/title from a renderDocsEntry "# id: title"
// header line and the timestamp from its "- closed: <RFC3339>" line. Missing
// or unparseable fields degrade to zero values (advisory: worst case the
// entry sorts as oldest and gets digested with a blank title).
func parseDocsEntryHeader(content string) (id, title string, closed time.Time) {
	lines := strings.Split(content, "\n")
	if len(lines) > 0 {
		if rest, ok := strings.CutPrefix(lines[0], "# "); ok {
			if i, t, ok := strings.Cut(rest, ": "); ok {
				id, title = i, t
			}
		}
	}
	for _, line := range lines {
		if rest, ok := strings.CutPrefix(line, "- closed: "); ok {
			if t, err := time.Parse(time.RFC3339, strings.TrimSpace(rest)); err == nil {
				closed = t
			}
			break
		}
	}
	return id, title, closed
}

func init() {
	docsCmd.AddCommand(docsUpdateCmd)
}
