package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// docsState is the committed regen watermark + dirty counter for bd docs.
// It lives at <docs.dir>/.docs-state so it travels with the wiki via git
// (deliberately the opposite choice from the machine-local post-close ledger:
// "docs current through X" must be shared, or every machine re-regenerates).
type docsState struct {
	RegenWatermark time.Time // docs are current through closes <= this instant
	Dirty          int       // closes recorded since the last regen
}

func docsStatePath(repoRoot, docsDir string) string {
	return filepath.Join(repoRoot, docsDir, ".docs-state")
}

// readDocsState parses the state file. ok=false means missing or corrupt —
// callers treat both as "no state yet" (advisory contract: never fail a close
// over a bad state file).
func readDocsState(path string) (docsState, bool) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is <docs.dir>/.docs-state, constructed by us.
	if err != nil {
		return docsState{}, false
	}
	var st docsState
	sawWatermark := false
	for _, line := range strings.Split(string(data), "\n") {
		k, v, found := strings.Cut(strings.TrimSpace(line), ":")
		if !found {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "regen_watermark":
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return docsState{}, false
			}
			st.RegenWatermark = t
			sawWatermark = true
		case "dirty":
			n, err := strconv.Atoi(v)
			if err != nil {
				return docsState{}, false
			}
			st.Dirty = n
		}
	}
	if !sawWatermark {
		return docsState{}, false
	}
	return st, true
}

// writeDocsState writes the state deterministically (fixed key order, RFC3339
// UTC, trailing newline) so it commits and diffs cleanly.
func writeDocsState(path string, st docsState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	body := fmt.Sprintf("regen_watermark: %s\ndirty: %d\n",
		st.RegenWatermark.UTC().Format(time.RFC3339), st.Dirty)
	return os.WriteFile(path, []byte(body), 0o600)
}
