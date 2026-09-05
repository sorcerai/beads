package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/uow"
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
