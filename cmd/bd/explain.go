package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/uimd"
)

var explainCmd = &cobra.Command{
	Use:   "explain <issue-id>",
	Short: "Explain changes and design context for a specific issue",
	Long: `Explain parses the issue details from beads (Description, Design,
Acceptance Criteria, Notes, Comments) and intersects them with associated
code changes from git history, git status, and .understand-anything/knowledge-graph.json.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		issueID := args[0]
		workspaces, _ := cmd.Flags().GetStringArray("workspace")
		globs, _ := cmd.Flags().GetStringArray("workspace-glob")
		project, _ := cmd.Flags().GetString("project")
		ctx := rootCtx

		wDirs := resolveWorkspaces(workspaces, globs)
		var targetDir string

		if project != "" {
			for _, d := range wDirs {
				if workspaceName(d) == project {
					targetDir = d
					break
				}
			}
		}

		if targetDir == "" && len(wDirs) > 1 {
			for _, d := range wDirs {
				testResp, err := explainIssueInWorkspace(ctx, d, issueID)
				if err == nil && len(testResp.Files) > 0 {
					targetDir = d
					break
				}
			}
		}

		if targetDir == "" {
			if len(wDirs) > 0 {
				targetDir = wDirs[0]
			} else {
				targetDir = ""
			}
		}

		resp, err := explainIssueInWorkspace(ctx, targetDir, issueID)
		if err != nil {
			FatalErrorRespectJSON("failed to explain issue: %v", err)
		}

		if jsonOutput {
			outputJSON(resp)
			return
		}

		// Text output
		renderExplainText(resp)
	},
}

func renderExplainText(resp ExplainResponse) {
	if resp.IssueDetails != nil {
		issue := &resp.IssueDetails.Issue
		// Draw issue header
		fmt.Printf("%s\n", formatIssueHeader(issue))
		fmt.Printf("%s\n", formatIssueMetadata(issue))

		if issue.Description != "" {
			fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESCRIPTION"), uimd.RenderMarkdown(issue.Description))
		}
		if issue.Design != "" {
			fmt.Printf("\n%s\n%s\n", ui.RenderBold("DESIGN"), uimd.RenderMarkdown(issue.Design))
		}
		if issue.AcceptanceCriteria != "" {
			fmt.Printf("\n%s\n%s\n", ui.RenderBold("ACCEPTANCE CRITERIA"), uimd.RenderMarkdown(issue.AcceptanceCriteria))
		}
		if issue.Notes != "" {
			fmt.Printf("\n%s\n%s\n", ui.RenderBold("NOTES"), uimd.RenderMarkdown(issue.Notes))
		}
		if len(resp.IssueDetails.Comments) > 0 {
			fmt.Printf("\n%s\n", ui.RenderBold("COMMENTS"))
			for _, c := range resp.IssueDetails.Comments {
				author := c.Author
				if author == "" {
					author = "system"
				}
				fmt.Printf("  %s (%s):\n", ui.RenderBold(author), c.CreatedAt.Local().Format("2006-01-02 15:04"))
				lines := strings.Split(c.Text, "\n")
				for _, line := range lines {
					fmt.Printf("    %s\n", line)
				}
				fmt.Println()
			}
		}
		if issue.CloseReason != "" {
			fmt.Printf("\n%s\n  %s\n", ui.RenderPass("CLOSE REASON:"), issue.CloseReason)
		}
	} else {
		fmt.Printf("%s %s\n\n", ui.RenderWarn("○"), ui.RenderBold("Issue "+resp.IssueID+" not found in beads database"))
	}

	fmt.Println(ui.RenderBold("ASSOCIATED CODE & WORKSPACE"))
	if !resp.HasGraph {
		fmt.Printf("  %s No .understand-anything/knowledge-graph.json found.\n", ui.RenderWarn("⚠"))
	}

	if len(resp.Files) == 0 {
		fmt.Printf("  No associated files or changes found in the git history for %s.\n", resp.IssueID)
		return
	}

	for _, f := range resp.Files {
		statusIcon := "○"
		statusColored := f.Status
		switch strings.ToLower(f.Status) {
		case "modified":
			statusIcon = ui.RenderWarn("◑")
			statusColored = ui.RenderWarn("modified")
		case "added":
			statusIcon = ui.RenderPass("●")
			statusColored = ui.RenderPass("added")
		case "deleted":
			statusIcon = ui.RenderFail("❄")
			statusColored = ui.RenderFail("deleted")
		}

		fmt.Printf("  %s %s [%s]", statusIcon, ui.RenderAccent(f.Path), statusColored)
		meta := []string{}
		if f.Layer != "" {
			meta = append(meta, fmt.Sprintf("Layer: %s", f.Layer))
		}
		if f.Complexity != "" {
			meta = append(meta, fmt.Sprintf("Complexity: %s", f.Complexity))
		}
		if len(f.Tags) > 0 {
			meta = append(meta, fmt.Sprintf("Tags: %s", strings.Join(f.Tags, ", ")))
		}
		if len(meta) > 0 {
			fmt.Printf(" · %s", strings.Join(meta, " · "))
		}
		fmt.Println()

		if f.Summary != "" {
			fmt.Printf("    %s\n", ui.RenderMuted(f.Summary))
		}

		if len(f.Connections) > 0 {
			fmt.Printf("    %s %s\n", ui.RenderMuted("Connections:"), strings.Join(f.Connections, ", "))
		}

		if f.DiffPreview != "" {
			fmt.Printf("    %s\n", ui.RenderBold("Git Diff Preview:"))
			lines := strings.Split(f.DiffPreview, "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
					fmt.Printf("      %s\n", ui.RenderPass(line))
				} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
					fmt.Printf("      %s\n", ui.RenderFail(line))
				} else if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "diff") || strings.HasPrefix(line, "index") {
					fmt.Printf("      %s\n", ui.RenderAccent(line))
				} else {
					fmt.Printf("      %s\n", line)
				}
			}
		}
		fmt.Println()
	}
}

func init() {
	explainCmd.Flags().StringArray("workspace", nil, "Workspace directory to include; repeatable (default: process CWD)")
	explainCmd.Flags().StringArray("workspace-glob", nil, "Glob for workspace dirs; repeatable")
	explainCmd.Flags().String("project", "", "Scope to a single project slug")
	rootCmd.AddCommand(explainCmd)
}
