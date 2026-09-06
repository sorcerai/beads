package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/codemap/cache"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/ui"
)

func init() {
	codemapCmd.AddCommand(codemapRecordCommitCmd)
}

// issueIDsInMessage pulls this repository's issue ids out of a commit message,
// in first-mention order and without repeats.
//
// The pattern is ANCHORED to the configured prefix on both sides: a word
// boundary before it so "xbd-1" is not an id, and the prefix's own hyphen so a
// sibling project's "bdx-1" is not one either. Nothing here validates that the
// id exists — that is the store's answer, and an id it does not know is
// reported and skipped by the caller.
func issueIDsInMessage(msg, prefix string) []string {
	if prefix == "" {
		return nil
	}
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(prefix) + `-[a-z0-9]+(?:\.[0-9]+)?\b`)
	seen := map[string]bool{}
	var ids []string
	for _, m := range re.FindAllString(msg, -1) {
		if !seen[m] {
			seen[m] = true
			ids = append(ids, m)
		}
	}
	return ids
}

var codemapRecordCommitCmd = &cobra.Command{
	Use:           "record-commit <rev>",
	Short:         "Link the issues a commit names to the files it touched, then refresh the map",
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(_ *cobra.Command, args []string) error {
		// EVERYTHING after the argument parse is wrapped: this verb's whole
		// caller is a git hook, and a code map is never worth failing a commit
		// over. Under BD_GIT_HOOK the failure is one stderr line and exit 0;
		// run by hand it is the same line and a non-zero exit, because a
		// person who typed it wants to know it did not work.
		if err := runRecordCommit(rootCtx, args[0]); err != nil {
			fmt.Fprintf(os.Stderr, "bd codemap record-commit: %v\n", err)
			if os.Getenv("BD_GIT_HOOK") == "1" {
				return nil
			}
			return &exitError{Code: 1}
		}
		return nil
	},
}

func runRecordCommit(ctx context.Context, rev string) error {
	root, repoID, err := codemapRepoRoot()
	if err != nil {
		return err
	}
	// The FULL sha, not the argument: RecordInTx accepts only 40 hex chars,
	// and "HEAD" is what the post-commit hook passes.
	sha, err := runGitAt(root, "rev-parse", rev)
	if err != nil {
		return err
	}
	// Two git calls rather than one: `--pretty=format:%B` followed by
	// `--name-only` puts the message and the path list in one stream with no
	// unambiguous separator, and a commit body containing a blank line then
	// parses as paths. Asking each question on its own is a millisecond and
	// cannot be misread.
	msg, err := runGitAt(root, "log", "-1", "--format=%B", sha)
	if err != nil {
		return err
	}
	paths, err := commitPaths(root, sha)
	if err != nil {
		return err
	}

	ids := issueIDsInMessage(msg, codemapIssuePrefix(ctx))
	var linked []string
	if len(ids) > 0 && len(paths) > 0 {
		files, filesErr := openIssueFiles()
		if filesErr != nil {
			return filesErr
		}
		for _, id := range ids {
			_, recErr := files.Record(ctx, codemapops.RecordRequest{
				IssueID: id, RepoID: repoID, Paths: paths,
				Source: codemapops.SourceCommit, CommitSHA: sha,
			})
			if recErr != nil {
				if errors.Is(recErr, beadserrors.ErrNotFound) {
					fmt.Fprintf(os.Stderr, "Warning: %s is not an issue in this workspace (skipped)\n", id)
					continue
				}
				return fmt.Errorf("recording %s: %w", id, recErr)
			}
			linked = append(linked, id)
		}
	}
	if len(linked) > 0 {
		commandDidWrite.Store(true)
	}

	// The same incremental rescan `bd codemap refresh` runs, so a commit
	// leaves the map describing the tree the commit produced.
	if _, err := refreshCodemap(ctx, root, repoID); err != nil {
		return err
	}
	renderRecordedCommit(os.Stdout, sha, linked, paths)
	return nil
}

// commitPaths lists the files one commit touched. A merge commit shows none,
// which is correct: its files are already attributed to the commits it merges.
func commitPaths(root, sha string) ([]string, error) {
	out, err := runGitAt(root, "show", "--name-only", "--pretty=format:", sha)
	if err != nil {
		return nil, err
	}
	return splitGitLines(out), nil
}

// codemapIssuePrefix resolves the prefix `bd create` mints ids with: the
// workspace's config.yaml wins over the database, because in shared-server mode
// the database may belong to another project (the same precedence
// doltStoreProvider.GetIssuePrefix uses).
func codemapIssuePrefix(ctx context.Context) string {
	if p := strings.TrimSpace(config.GetString("issue-prefix")); p != "" {
		return p
	}
	if store != nil {
		if p, err := store.GetConfig(ctx, "issue_prefix"); err == nil && p != "" {
			return p
		}
	}
	return "bd"
}

func renderRecordedCommit(w io.Writer, sha string, linked, paths []string) {
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	if len(linked) == 0 {
		fmt.Fprintf(w, "%s\n", ui.RenderMuted("No issue ids in "+short+"; refreshed the code map only"))
		return
	}
	fmt.Fprintf(w, "%s Linked %s to %d files from %s\n",
		ui.RenderPass("✓"), ui.RenderID(strings.Join(linked, ", ")), len(paths), short)
	for _, p := range paths {
		fmt.Fprintf(w, "  %s\n", p)
	}
}

// =============================================================================
// The managed post-commit git hook
// =============================================================================

// codemapHookTimeoutEnv overrides how long the post-commit hook waits for the
// record-commit subprocess. It accepts a Go duration ("15s") or bare seconds.
const codemapHookTimeoutEnv = "BD_CODEMAP_HOOK_TIMEOUT"

const codemapHookDefaultTimeout = 10 * time.Second

// runPostCommitHook runs chained hooks after a commit, then records that
// commit's issue-to-file links and refreshes the code map.
//
// The codemap half NEVER fails the hook: git has already written the commit by
// the time post-commit runs, so a non-zero exit here only prints noise at the
// end of a successful commit. The CHAINED hook's exit code is propagated,
// exactly as every other managed hook does — installing beads into post-commit
// must not silently disarm a user's existing one.
//
//nolint:unparam // The codemap half always returns 0 by design.
func runPostCommitHook() int {
	if exitCode := runChainedHook("post-commit", nil); exitCode != 0 {
		return exitCode
	}
	if os.Getenv("BD_NO_CODEMAP") == "1" {
		return 0
	}
	// A repository whose map was never built has nothing to refresh, and a
	// commit is the wrong moment to spend a whole-repository scan uninvited.
	beadsDir := beads.FindBeadsDir()
	if beadsDir == "" {
		return 0
	}
	if _, err := os.Stat(filepath.Join(beadsDir, cache.FileName)); err != nil {
		return 0
	}

	ctx, cancel := context.WithTimeout(context.Background(), codemapHookTimeout())
	defer cancel()
	// #nosec G702 -- os.Args[0] is this bd binary re-invoking itself with a
	// fixed subcommand; no argument here is attacker-controlled.
	cmd := exec.CommandContext(ctx, os.Args[0], "codemap", "record-commit", "HEAD")
	if out, err := cmd.CombinedOutput(); err != nil {
		// #nosec G705 -- this is a git hook's stderr on a developer's terminal,
		// not a web response; the "taint" is our own subcommand's diagnostics.
		fmt.Fprintf(os.Stderr, "beads: codemap record-commit: %v: %s\n", err, strings.TrimSpace(string(out)))
	}
	return 0
}

func codemapHookTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv(codemapHookTimeoutEnv))
	if v == "" {
		return codemapHookDefaultTimeout
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return codemapHookDefaultTimeout
}
