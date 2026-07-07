//go:build cgo

// close_sweep_embedded_test.go — integration coverage for the post-close
// reconciliation sweep (beads-qb7.2). The sweep catches closes that happened
// WITHOUT this machine firing its post-close hook (closed on another machine and
// synced in, closed by another client, or closed while the hook was missing) by
// re-firing the hook at the start of `bd ready` / `bd prime`.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSweepMissedClosesOnReady walks the full advisory contract in one flow.
// The dedup source of truth is the fired-ledger, not the watermark, so the
// distinguishing question at each close is "was this ID recorded as handled?":
//
//	first run     -> seed watermark to now, sweep nothing (no history storm)
//	env opt-out   -> --no-hooks close is ledgered, so the sweep must NOT re-fire it
//	genuine miss  -> a close made while the hook was ABSENT is NOT ledgered, so once
//	                 the hook is installed the sweep fires it
//	direct fire   -> a normal close fires once at close and is ledgered, so the
//	                 sweep must NOT re-fire it (no systematic double-fire)
//	idempotent    -> a swept close is ledgered, so a second sweep does not re-fire
func TestSweepMissedClosesOnReady(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "sw")

	marker := filepath.Join(dir, ".post-close-marker-sweep")
	os.Remove(marker)
	hookPath := installMarkerHook(t, beadsDir, marker)
	defer os.Remove(hookPath)

	watermark := filepath.Join(beadsDir, "post-close-watermark")

	runReady := func() {
		c := exec.Command(bd, "ready")
		c.Dir = dir
		c.Env = bdEnv(dir)
		_, _ = c.CombinedOutput() // exit code irrelevant; only hook firing matters
	}
	closeIssue := func(id string, env ...string) {
		t.Helper()
		c := exec.Command(bd, "close", id)
		c.Dir = dir
		c.Env = append(bdEnv(dir), env...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("bd close %s failed: %v\n%s", id, err, out)
		}
	}
	seedPastWatermark := func() {
		t.Helper()
		past := time.Now().Add(-time.Hour).Format(time.RFC3339)
		if err := os.WriteFile(watermark, []byte(past+"\n"), 0o644); err != nil {
			t.Fatalf("seed past watermark: %v", err)
		}
	}
	markerHas := func(id string) bool {
		data, err := os.ReadFile(marker)
		return err == nil && strings.Contains(string(data), id)
	}

	// --- first run: seed watermark, fire nothing (no history storm) ---
	envIssue := bdCreate(t, bd, dir, "Env opt-out close", "--type", "task")
	closeIssue(envIssue.ID, "BD_NO_CLOSE_HOOK=1")
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("hook fired during BD_NO_CLOSE_HOOK=1 close; test premise is broken")
	}
	if _, err := os.Stat(watermark); err == nil {
		t.Fatal("watermark existed before first sweep; fresh init should have none")
	}
	runReady()
	if _, err := os.Stat(watermark); err != nil {
		t.Fatalf("first-run sweep did not seed the watermark: %v", err)
	}

	// --- env opt-out: the --no-hooks close was ledgered, so the sweep must NOT
	// re-fire it, even with the watermark seeded before the close. ---
	os.Remove(marker)
	seedPastWatermark()
	runReady()
	if markerHas(envIssue.ID) {
		t.Errorf("sweep re-fired an env-suppressed (ledgered) close %q", envIssue.ID)
	}

	// --- genuine miss: close made while the hook was ABSENT is not ledgered, so
	// once the hook is reinstalled the sweep must fire it. ---
	if err := os.Remove(hookPath); err != nil {
		t.Fatalf("remove hook for genuine-miss phase: %v", err)
	}
	missIssue := bdCreate(t, bd, dir, "Closed while hook absent", "--type", "task")
	closeIssue(missIssue.ID) // no hook resolves -> nothing recorded
	installMarkerHook(t, beadsDir, marker)
	os.Remove(marker)
	seedPastWatermark()
	runReady()
	if !markerHas(missIssue.ID) {
		t.Errorf("sweep did not fire the genuine miss %q (closed while hook absent)", missIssue.ID)
	}
	if markerHas(envIssue.ID) {
		t.Errorf("sweep re-fired the ledgered env-suppressed close %q alongside the miss", envIssue.ID)
	}

	// --- idempotent: the swept miss is now ledgered; a second sweep must not
	// re-fire it. ---
	os.Remove(marker)
	seedPastWatermark()
	runReady()
	if markerHas(missIssue.ID) {
		t.Errorf("second sweep re-fired an already-swept (ledgered) close %q", missIssue.ID)
	}

	// --- direct fire: a normal close fires once at close and is ledgered, so the
	// sweep must not re-fire it (the systematic double-fire this ledger closes). ---
	os.Remove(marker)
	directIssue := bdCreate(t, bd, dir, "Direct close", "--type", "task")
	closeIssue(directIssue.ID)
	if !markerHas(directIssue.ID) {
		t.Fatalf("direct close did not fire the hook for %q", directIssue.ID)
	}
	os.Remove(marker)
	seedPastWatermark()
	runReady()
	if markerHas(directIssue.ID) {
		t.Errorf("sweep re-fired a direct close %q — double-fire not prevented", directIssue.ID)
	}
}
