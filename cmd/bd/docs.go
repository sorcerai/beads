package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
)

// docsCmd is the root of the bd docs family — the beads-native living
// documentation system, with deterministic updates and agent-assisted regeneration.
var docsCmd = &cobra.Command{
	Use:   "docs",
	Short: "Living repo documentation driven by closed issues",
}

// docsDirName is the wiki directory name (config docs.dir, default "wiki").
func docsDirName() string {
	if v := strings.TrimSpace(config.GetString("docs.dir")); v != "" {
		return v
	}
	return "wiki"
}

func validateDocsDir(repoRoot, docsDir string) error {
	clean := filepath.Clean(docsDir)
	if docsDir == "" || clean == "." || clean != docsDir || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("docs.dir must be a clean relative path inside the repository")
	}

	root, err := filepath.EvalSymlinks(repoRoot)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	current := root
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		resolved, err := filepath.EvalSymlinks(current)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return fmt.Errorf("resolve docs.dir: %w", err)
		}
		if !pathWithin(root, resolved) {
			return fmt.Errorf("docs.dir resolves outside the repository")
		}
		current = resolved
	}
	docsRoot := filepath.Join(root, clean)
	if err := filepath.WalkDir(docsRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("docs.dir contains symlink %q", path)
		}
		return nil
	}); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("validate docs.dir: %w", err)
	}

	return nil
}

func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func validatedDocsDir(repoRoot string) (string, error) {
	docsDir := docsDirName()
	if err := validateDocsDir(repoRoot, docsDir); err != nil {
		return "", err
	}
	return docsDir, nil
}

// docsRegenThreshold is the dirty-count that triggers the regen nudge
// (config docs.regen-threshold, default 10).
func docsRegenThreshold() int {
	if v := strings.TrimSpace(config.GetString("docs.regen-threshold")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 10
}

func init() {
	rootCmd.AddCommand(docsCmd)
}
