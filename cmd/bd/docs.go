package main

import (
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
)

// docsCmd is the root of the bd docs family — the beads-native living
// documentation system. Design spec: docs/superpowers/specs/2026-07-04-bd-docs-design.md.
// Tier 1 (update) is deterministic and rides the post-close hook + sweep;
// Tier 2 (regen) is an LLM pass run by the resident agent session.
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
