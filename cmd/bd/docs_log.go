package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/timeparsing"
	"github.com/steveyegge/beads/internal/types"
)

// docsLogCmd renders closed issues since a date straight from Dolt —
// regeneration-on-demand for history the inbox no longer holds (compacted
// into backlog.md or already consumed by a regen).
var docsLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Render closed issues since a date from Dolt (regeneration-on-demand)",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		since, _ := cmd.Flags().GetString("since")
		if since == "" {
			FatalErrorRespectJSON("--since is required (RFC3339 or YYYY-MM-DD)")
		}
		sinceTime, err := timeparsing.ParseRelativeTime(since, time.Now())
		if err != nil {
			FatalErrorRespectJSON("parsing --since: %v", err)
		}
		repoRoot := findRepoRootForArch()
		if repoRoot == "" {
			FatalErrorRespectJSON("not in a git repository")
		}
		write, _ := cmd.Flags().GetBool("write")

		statusClosed := types.StatusClosed
		issues, err := store.SearchIssues(rootCtx, "", types.IssueFilter{
			Status:      &statusClosed,
			ClosedAfter: &sinceTime,
		})
		if err != nil {
			FatalErrorRespectJSON("querying closed issues: %v", err)
		}

		docsDir := docsDirName()
		rendered := make([]string, 0, len(issues))
		for _, issue := range issues {
			parentID, deps := docsIssueLinks(rootCtx, issue)
			files := docsIssueFiles(rootCtx, repoRoot, issue.ID)
			rendered = append(rendered, string(renderDocsEntry(issue, parentID, deps, files)))
			if write {
				// existence-idempotent, same as Tier 1 — does NOT bump dirty:
				// this is a read/materialize path, not a new close event.
				writeDocsEntryForIssue(rootCtx, repoRoot, docsDir, issue, parentID, deps, files)
			}
		}
		fmt.Print(strings.Join(rendered, "\n---\n"))
	},
}

func init() {
	docsLogCmd.Flags().String("since", "", "Show closed issues since this date (RFC3339 or YYYY-MM-DD)")
	docsLogCmd.Flags().Bool("write", false, "Also (re-)materialize entries into log/ (existence-idempotent, does not bump dirty)")
	docsCmd.AddCommand(docsLogCmd)
}
