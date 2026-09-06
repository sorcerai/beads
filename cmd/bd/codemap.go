package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/types"
)

var codemapCmd = &cobra.Command{
	Use:           "codemap",
	Short:         "Code map: files, imports, importers, and the issues touching them",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.AddCommand(codemapCmd)
}

// openCodeMapIndexer, openCodeMapReader and openIssueFiles hand back the
// code-map roles for whichever route this invocation is on, each through its
// OWN capability accessor — the store's for the direct route and the
// provider's for the proxied one. Same two-step shape as openMemories.
func openCodeMapIndexer() (codemapops.Indexer, error) {
	if usesProxiedServer() {
		src, err := codemapProvider()
		if err != nil {
			return nil, err
		}
		s, ok := src.(uow.CodeMapIndexerSource)
		if !ok {
			return nil, fmt.Errorf("proxied-server provider %T does not offer the code-map indexer", src)
		}
		return s.CodeMapIndexer()
	}
	if err := ensureDirectMode("codemap"); err != nil {
		return nil, err
	}
	return store.CodeMapIndexer()
}

func openCodeMapReader() (codemapops.Reader, error) {
	if usesProxiedServer() {
		src, err := codemapProvider()
		if err != nil {
			return nil, err
		}
		s, ok := src.(uow.CodeMapReaderSource)
		if !ok {
			return nil, fmt.Errorf("proxied-server provider %T does not offer the code-map reader", src)
		}
		return s.CodeMapReader()
	}
	if err := ensureDirectMode("codemap"); err != nil {
		return nil, err
	}
	return store.CodeMapReader()
}

func openIssueFiles() (codemapops.IssueFiles, error) {
	if usesProxiedServer() {
		src, err := codemapProvider()
		if err != nil {
			return nil, err
		}
		s, ok := src.(uow.IssueFilesSource)
		if !ok {
			return nil, fmt.Errorf("proxied-server provider %T does not offer the issue-files surface", src)
		}
		return s.IssueFiles()
	}
	if err := ensureDirectMode("codemap"); err != nil {
		return nil, err
	}
	return store.IssueFiles()
}

func codemapProvider() (uow.UnitOfWorkProvider, error) {
	if uowProvider == nil {
		return nil, errors.New("proxied-server UOW provider not initialized")
	}
	return uowProvider, nil
}

// codemapRepoRoot resolves the repository the code map describes. GetRepoRoot
// reports "not a repository" as an empty string, so the error is ours to make.
func codemapRepoRoot() (root, repoID string, err error) {
	root = git.GetRepoRoot()
	if root == "" {
		return "", "", errors.New("not a git repository (the code map is keyed by repository)")
	}
	repoID, err = beads.ComputeRepoIDForPath(root)
	if err != nil {
		return "", "", fmt.Errorf("computing repo id: %w", err)
	}
	return root, repoID, nil
}

// codemapHeadSHA reads the commit the working tree is on. A repository with no
// commits yet has nothing to index against, and says so.
func codemapHeadSHA(root string) (string, error) {
	sha, err := runGitAt(root, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("reading HEAD: %w (commit something first)", err)
	}
	return sha, nil
}

// runGitAt runs one git command in root and returns its trimmed stdout.
func runGitAt(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(string(out)), nil
}

// codemapBeadsDir locates the .beads directory the derived cache lives in.
func codemapBeadsDir() (string, error) {
	dir := beads.FindBeadsDir()
	if dir == "" {
		return "", errors.New("no beads directory found (run: bd init)")
	}
	return dir, nil
}

// notIndexedHint turns the reader's "never built" sentinel into the one-line
// error with the fix in it, and leaves every other error alone.
func notIndexedHint(err error) error {
	var ni *codemapops.ErrNotIndexed
	if errors.As(err, &ni) {
		return errors.New("code map not built for this repository — run: bd codemap build")
	}
	return err
}

// issueCodeContextFor is the best-effort read behind every CODE surface. A
// workspace with no repository, no map and no linked files is the ordinary
// case for most of bd's users, so every failure here is a debug line and a nil
// result: surfacing code context must never turn a successful `bd show` or
// `bd update --claim` into an error.
func issueCodeContextFor(ctx context.Context, issueID string) *codemapops.IssueCodeContext {
	root, repoID, err := codemapRepoRoot()
	if err != nil {
		debug.Logf("codemap: no repo for %s: %v\n", issueID, err)
		return nil
	}
	files, err := openIssueFiles()
	if err != nil {
		debug.Logf("codemap: issue-files unavailable: %v\n", err)
		return nil
	}
	cc, err := files.IssueCodeContext(ctx, issueID, repoID)
	if err != nil {
		debug.Logf("codemap: context for %s: %v\n", issueID, err)
		return nil
	}
	appendBranchInferredFiles(ctx, root, repoID, issueID, &cc)
	if len(cc.Files) == 0 {
		return nil
	}
	return &cc
}

// appendBranchInferredFiles is spec §9.4's branch source. When the checked-out
// branch NAMES the issue, the files that branch changed are part of the answer
// to "what code does this issue touch" even though nothing has recorded them.
//
// It is computed here and never persisted: a branch is a guess that expires the
// moment the branch is deleted or rebased, and a Record call would leave that
// guess in the store as an observation. Recorded links always win — a path
// already in cc.Files keeps the source it earned.
//
// The branch test and the base resolution are `bd explain`'s (serve_board.go),
// so the two surfaces infer the same set from the same repository.
func appendBranchInferredFiles(ctx context.Context, root, repoID, issueID string, cc *codemapops.IssueCodeContext) {
	branch, err := runGitAt(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || !strings.Contains(strings.ToLower(branch), strings.ToLower(issueID)) {
		return
	}
	base := ""
	for _, candidate := range []string{"origin/main", "main", "origin/master", "master", "HEAD~1"} {
		if _, err := runGitAt(root, "diff", "--name-only", candidate+"...HEAD"); err == nil {
			base = candidate
			break
		}
	}
	if base == "" {
		return
	}
	out, err := runGitAt(root, "diff", "--name-only", base+"...HEAD")
	if err != nil {
		return
	}
	seen := make(map[string]struct{}, len(cc.Files))
	for _, f := range cc.Files {
		seen[f.Path] = struct{}{}
	}
	var reader codemapops.Reader
	for _, path := range strings.Split(out, "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		f := codemapops.IssueCodeFile{IssueFile: codemapops.IssueFile{Path: path, Source: codemapops.SourceBranch}}
		if reader == nil {
			// Opened lazily and once: a branch with no new paths must not pay
			// for a reader, and an unbuildable one is a nil Context, not a
			// dropped file.
			if r, rerr := openCodeMapReader(); rerr == nil {
				reader = r
			} else {
				debug.Logf("codemap: no reader for branch files: %v\n", rerr)
			}
		}
		if reader != nil {
			// An unindexed repository and a path the map has never seen are
			// both ordinary here: the file is still part of the answer, it just
			// has no context to show.
			if fc, ferr := reader.FileContext(ctx, repoID, path); ferr == nil {
				f.Context = &fc
			} else if !errors.Is(ferr, storage.ErrNotFound) {
				debug.Logf("codemap: file context for %s: %v\n", path, ferr)
			}
		}
		cc.Files = append(cc.Files, f)
	}
}

// printIssueCodeSection appends the CODE block to a text-mode issue rendering.
// quiet suppresses the block and nothing else.
func printIssueCodeSection(ctx context.Context, issueID string, quiet bool) {
	if quiet {
		return
	}
	if cc := issueCodeContextFor(ctx, issueID); cc != nil {
		renderIssueCodeContext(os.Stdout, *cc)
	}
}

// recordIssueFilesManual is `bd update --files`: the same manual linkage
// `bd codemap link` writes, on an issue the caller is already updating.
func recordIssueFilesManual(ctx context.Context, issueID string, paths []string) error {
	_, repoID, err := codemapRepoRoot()
	if err != nil {
		return err
	}
	files, err := openIssueFiles()
	if err != nil {
		return err
	}
	if _, err := files.Record(ctx, codemapops.RecordRequest{
		IssueID: issueID, RepoID: repoID, Paths: paths, Source: codemapops.SourceManual,
	}); err != nil {
		return err
	}
	commandDidWrite.Store(true)
	return nil
}

// showDetailsJSON is the `bd show --json` payload with the code context
// attached. types.IssueDetails is embedded rather than copied so the shape the
// reader role produces stays the contract; `code` is omitted entirely when the
// issue has no linked files.
type showDetailsJSON struct {
	*types.IssueDetails
	Code *codemapops.IssueCodeContext `json:"code,omitempty"`
}

// issueCodeJSON is the same attachment for `bd update --claim --json`, where
// the payload element is the issue itself rather than its detail view.
type issueCodeJSON struct {
	*types.Issue
	Code *codemapops.IssueCodeContext `json:"code,omitempty"`
}

func withIssueCodeJSON(ctx context.Context, details *types.IssueDetails) any {
	if details == nil {
		return details
	}
	return showDetailsJSON{IssueDetails: details, Code: issueCodeContextFor(ctx, details.ID)}
}

// withIssueCodeList attaches the code context to each issue in a
// `bd update --json` payload. Without --claim the payload is unchanged: the
// context answers "what code did I just take over", which is a question only
// a claim asks.
func withIssueCodeList(ctx context.Context, issues []*types.Issue, claim bool) any {
	if !claim {
		return issues
	}
	out := make([]issueCodeJSON, 0, len(issues))
	for _, iss := range issues {
		out = append(out, issueCodeJSON{Issue: iss, Code: issueCodeContextFor(ctx, iss.ID)})
	}
	return out
}

// recordUpdateFiles applies `bd update --files` to every id that updated. A
// failure here is a warning, not a failed update: the issue write is already
// committed, and losing the exit code would misreport what happened.
func recordUpdateFiles(ctx context.Context, cmd *cobra.Command, ids []string) {
	paths, _ := cmd.Flags().GetStringSlice("files")
	if len(paths) == 0 {
		return
	}
	for _, id := range ids {
		if err := recordIssueFilesManual(ctx, id, paths); err != nil {
			fmt.Fprintf(os.Stderr, "warning: recording --files on %s: %v\n", id, err)
		}
	}
}
