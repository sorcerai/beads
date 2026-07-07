package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

// renderDocsEntry renders one wiki/log entry for a closed issue.
//
// Deterministic by contract: same issue data => same bytes (UTC RFC3339
// timestamps, sorted lists, fixed field order). The files section derives
// from local git state via bd explain and MAY differ between machines; the
// caller therefore uses file-existence (not byte equality) for idempotence.
func renderDocsEntry(issue *types.Issue, parentID string, deps []string, files []string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: %s\n\n", issue.ID, issue.Title)

	epic := parentID
	if epic == "" {
		epic = "-"
	}
	closed := "-"
	if issue.ClosedAt != nil {
		closed = issue.ClosedAt.UTC().Format(time.RFC3339)
	}
	sortedDeps := append([]string(nil), deps...)
	sort.Strings(sortedDeps)
	depsStr := "-"
	if len(sortedDeps) > 0 {
		depsStr = strings.Join(sortedDeps, ", ")
	}

	fmt.Fprintf(&b, "- type: %s\n", issue.IssueType)
	fmt.Fprintf(&b, "- priority: P%d\n", issue.Priority)
	fmt.Fprintf(&b, "- epic: %s\n", epic)
	fmt.Fprintf(&b, "- created: %s\n", issue.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- closed: %s\n", closed)
	fmt.Fprintf(&b, "- close_reason: %s\n", strings.TrimSpace(issue.CloseReason))
	fmt.Fprintf(&b, "- deps: %s\n", depsStr)

	if d := strings.TrimSpace(issue.Description); d != "" {
		fmt.Fprintf(&b, "\n## Description\n\n%s\n", d)
	}

	b.WriteString("\n## Touched files (derived from local git; may vary by checkout)\n\n")
	if len(files) == 0 {
		b.WriteString("no associated files found (weak issue↔commit association)\n")
	} else {
		sortedFiles := append([]string(nil), files...)
		sort.Strings(sortedFiles)
		for _, f := range sortedFiles {
			fmt.Fprintf(&b, "- %s\n", f)
		}
	}
	return []byte(b.String())
}

// docsEntryPath is <docsDir>/log/<issueID>.md with path separators neutered.
func docsEntryPath(repoRoot, docsDir, issueID string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(issueID)
	return filepath.Join(repoRoot, docsDir, "log", safe+".md")
}
