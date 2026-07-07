package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// docsState is the committed regen watermark for bd docs. It lives at
// <docs.dir>/.docs-state so it travels with the wiki via git (deliberately
// the opposite choice from the machine-local post-close ledger: "docs
// current through X" must be shared, or every machine re-regenerates).
//
// There is deliberately no stored dirty counter: that would be a
// read-modify-write race between concurrent 'bd docs update' invocations.
// The inbox directory itself is the counter — see docsInboxCount.
type docsState struct {
	RegenWatermark time.Time // docs are current through closes <= this instant
	RegenStarted   time.Time // set when a regen prompt is built; zero = none in flight
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
		case "regen_started":
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				return docsState{}, false
			}
			st.RegenStarted = t
		case "dirty":
			// Tolerate state files written before F1: the stored counter is
			// gone, the inbox itself is the count now (docsInboxCount).
		}
	}
	if !sawWatermark {
		return docsState{}, false
	}
	return st, true
}

// writeDocsState writes the state deterministically (fixed key order, RFC3339
// UTC, trailing newline) so it commits and diffs cleanly. A zero RegenStarted
// formats/parses round-trip to IsZero()==true, so "no regen in flight" survives.
func writeDocsState(path string, st docsState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	body := fmt.Sprintf("regen_watermark: %s\nregen_started: %s\n",
		st.RegenWatermark.UTC().Format(time.RFC3339), st.RegenStarted.UTC().Format(time.RFC3339))
	return os.WriteFile(path, []byte(body), 0o600)
}
