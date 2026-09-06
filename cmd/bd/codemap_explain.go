package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/codemapops"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/git"
)

// codemapIsCurrentWorkspace reports whether dir is the workspace THIS process's
// store is bound to.
//
// `bd explain` can be pointed at another workspace, and everything else it
// reads from one it does not live in goes through a subprocess (see
// execShowIssueJSONIn) precisely because the store opened here describes the
// current directory and nothing else. The code-map roles are no different, so
// they are consulted only when the two are the same repository; a foreign
// workspace keeps the behavior it had before this file existed.
func codemapIsCurrentWorkspace(dir string) bool {
	if dir == "" {
		return true // the CLI's own default: no --workspace means right here
	}
	root := git.GetRepoRoot()
	if root == "" {
		return false
	}
	return resolvedPath(dir) == resolvedPath(root)
}

// resolvedPath is the comparable spelling of a directory: absolute, with
// symlinks followed, because a temp directory on macOS is reached as both
// /var/... and /private/var/... and a string compare would call one repository
// two.
func resolvedPath(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// codemapLinkedFiles is what `bd codemap link` (and the commit and branch
// observers) recorded for one issue, keyed by path with the explain status
// each one gets. A workspace with no repository, no store or no rows answers
// nothing: this enriches an explain, it never fails one.
func codemapLinkedFiles(ctx context.Context, issueID string) map[string]string {
	files, err := openIssueFiles()
	if err != nil {
		debug.Logf("explain: issue-files unavailable: %v\n", err)
		return nil
	}
	rows, err := files.ByIssue(ctx, issueID)
	if err != nil {
		debug.Logf("explain: linked files for %s: %v\n", issueID, err)
		return nil
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.Path] = "linked:" + string(row.Source)
	}
	return out
}

// codemapExplainContexts reads the map's context for each path, and reports
// whether the repository is indexed at all.
//
// A path the map does not hold is absent from the answer rather than an error:
// an explain lists whatever git and the links found, and most of those files
// (a README, a workflow file) are not code-map nodes.
func codemapExplainContexts(ctx context.Context, paths []string) (map[string]codemapops.FileContext, bool) {
	if len(paths) == 0 {
		return nil, false
	}
	_, repoID, err := codemapRepoRoot()
	if err != nil {
		debug.Logf("explain: no repo: %v\n", err)
		return nil, false
	}
	reader, err := openCodeMapReader()
	if err != nil {
		debug.Logf("explain: code-map reader unavailable: %v\n", err)
		return nil, false
	}
	out := make(map[string]codemapops.FileContext, len(paths))
	indexed := false
	for _, path := range paths {
		fc, err := reader.FileContext(ctx, repoID, path)
		switch {
		case err == nil:
			out[path] = fc
			indexed = true
		case errors.Is(err, beadserrors.ErrNotFound):
			// The repository IS indexed — the reader refuses every path with
			// ErrNotIndexed when it is not — this file simply is not a node.
			indexed = true
		default:
			var ni *codemapops.ErrNotIndexed
			if !errors.As(err, &ni) {
				debug.Logf("explain: context for %s: %v\n", path, err)
			}
			return nil, false
		}
	}
	return out, indexed
}

// fillExplainFromCodeMap is the code map's answer to the same question
// knowledge-graph.json answers: what is this file, which layer is it in, and
// what does it sit between.
func fillExplainFromCodeMap(f *ExplainFile, fc codemapops.FileContext) {
	f.Summary = fc.Node.Summary
	f.Layer = fc.Node.Layer
	seen := map[string]bool{}
	// Paths in full, not base names: the JSON-graph branch shortens a file to
	// its base because a knowledge-graph node carries a display name, and two
	// `handler.go`s under different packages would be one line here.
	add := func(refs []codemapops.NodeRef, relation string) {
		for _, ref := range refs {
			seen[fmt.Sprintf("%s (%s)", ref.Path, relation)] = true
		}
	}
	add(fc.Imports, "imports")
	add(fc.Importers, "imported by")
	add(fc.Tests, "tested by")
	for conn := range seen {
		f.Connections = append(f.Connections, conn)
	}
	sort.Strings(f.Connections)
}
