package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// docsStatusCmd summarizes bd docs state: opt-in, dirty count, inbox size,
// and wiki-page staleness (reusing the ARCH.md staleness machinery, since a
// backtick-quoted code reference going stale is the same problem in any .md).
var docsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show bd docs state: opt-in, dirty count, inbox size, wiki staleness",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		repoRoot := findRepoRootForArch()
		if repoRoot == "" {
			FatalErrorRespectJSON("not in a git repository")
		}
		printDocsStatus(repoRoot, docsDirName())
	},
}

func printDocsStatus(repoRoot, docsDir string) {
	statePath := docsStatePath(repoRoot, docsDir)
	st, ok := readDocsState(statePath)
	if !ok {
		fmt.Println("bd docs: not opted in (run 'bd docs init')")
		return
	}
	fmt.Printf("docs dir: %s\n", docsDir)
	fmt.Printf("regen watermark: %s\n", st.RegenWatermark.Format(time.RFC3339))

	// Dirty is derived from the inbox itself (F1), not a stored counter.
	inboxCount := docsInboxCount(repoRoot, docsDir)
	fmt.Printf("dirty: %d\n", inboxCount)
	fmt.Printf("inbox entries: %d\n", inboxCount)

	backlogPresent := false
	if _, err := os.Stat(filepath.Join(repoRoot, docsDir, "log", "backlog.md")); err == nil {
		backlogPresent = true
	}
	fmt.Printf("backlog present: %v\n", backlogPresent)

	findings := checkMarkdownStaleness(repoRoot, docsWikiMarkdownFiles(repoRoot, docsDir))
	if len(findings) == 0 {
		fmt.Println("wiki staleness: none")
		return
	}
	fmt.Println("wiki staleness:")
	for _, f := range findings {
		fmt.Printf("  %s\n", f)
	}
}

// docsWikiMarkdownFiles lists every .md page under <docsDir> except log/
// (per-issue entries are records, not narrative pages — staleness doesn't
// apply to them the same way).
func docsWikiMarkdownFiles(repoRoot, docsDir string) []string {
	root := filepath.Join(repoRoot, docsDir)
	var files []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == "log" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// checkMarkdownStaleness mirrors checkArchStaleness (arch_stale.go) but scans
// the given files instead of just ARCH.md, reusing the same extraction +
// resolution helpers rather than duplicating them.
func checkMarkdownStaleness(repoRoot string, mdPaths []string) []string {
	modPath, _ := readGoModulePath(repoRoot)

	var allRefs []archRef
	exists := make(map[string]bool)
	pending := map[string]bool{}
	for _, mdPath := range mdPaths {
		data, err := os.ReadFile(mdPath) // #nosec G304 -- mdPath comes from docsWikiMarkdownFiles' own repo-tree walk.
		if err != nil {
			continue
		}
		refs := extractArchRefs(string(data), modPath)
		for _, ref := range refs {
			switch ref.kind {
			case archRefPath:
				_, statErr := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(ref.rel)))
				exists[ref.token] = statErr == nil
			case archRefIdent:
				if _, statErr := os.Stat(filepath.Join(repoRoot, ref.token)); statErr == nil {
					exists[ref.token] = true
				} else {
					pending[ref.token] = true
				}
			}
		}
		allRefs = append(allRefs, refs...)
	}
	resolveIdentsInGoFiles(repoRoot, pending, exists)

	var findings []string
	for _, ref := range allRefs {
		if !exists[ref.token] {
			findings = append(findings, "stale: references "+ref.token+" which no longer exists")
		}
	}
	return findings
}

func init() {
	docsCmd.AddCommand(docsStatusCmd)
}
