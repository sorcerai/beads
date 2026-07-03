package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPostCloseWatermarkRoundTrip verifies write→read preserves the instant at
// RFC3339 (second) precision — the granularity the ClosedAfter query compares at.
func TestPostCloseWatermarkRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), postCloseWatermarkName)

	want := time.Now().Add(-3 * time.Minute).Truncate(time.Second)
	if err := writePostCloseWatermark(path, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := readPostCloseWatermark(path)
	if !ok {
		t.Fatal("read reported no watermark after write")
	}
	if !got.Equal(want) {
		t.Errorf("round-trip mismatch: wrote %s, read %s", want.Format(time.RFC3339), got.Format(time.RFC3339))
	}
}

// TestPostCloseWatermarkFirstRun verifies the "no watermark yet" signal that
// makes the sweep seed-and-skip instead of storming through history: a missing,
// empty, or corrupt file all read as ok=false.
func TestPostCloseWatermarkFirstRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing")
	if _, ok := readPostCloseWatermark(missing); ok {
		t.Error("missing file must read as no watermark (ok=false)")
	}

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte("  \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPostCloseWatermark(empty); ok {
		t.Error("empty file must read as no watermark (ok=false)")
	}

	corrupt := filepath.Join(dir, "corrupt")
	if err := os.WriteFile(corrupt, []byte("not-a-timestamp"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := readPostCloseWatermark(corrupt); ok {
		t.Error("corrupt file must read as no watermark (ok=false)")
	}
}

// TestPostCloseWatermarkAdvance verifies the monotonic advance the sweep relies
// on for idempotency: after seeding, a later close moves the watermark forward
// and a re-read sees the advanced value (so the next sweep skips what it swept).
func TestPostCloseWatermarkAdvance(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), postCloseWatermarkName)

	seed := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := writePostCloseWatermark(path, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	advanced := seed.Add(30 * time.Minute)
	if err := writePostCloseWatermark(path, advanced); err != nil {
		t.Fatalf("advance: %v", err)
	}
	got, ok := readPostCloseWatermark(path)
	if !ok {
		t.Fatal("read reported no watermark after advance")
	}
	if !got.Equal(advanced) {
		t.Errorf("watermark did not advance: want %s, got %s", advanced.Format(time.RFC3339), got.Format(time.RFC3339))
	}
	if !got.After(seed) {
		t.Error("advanced watermark must be strictly after the seed")
	}
}

// TestPostCloseLedgerRoundTrip covers append→read→prune, the fired-ledger
// contract the sweep relies on to skip closes this machine already handled.
func TestPostCloseLedgerRoundTrip(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), postCloseLedgerName)
	now := time.Now().Truncate(time.Second)

	// Append across two calls; both batches must read back as a set.
	appendPostCloseFired(path, []string{"be-1", "be-2"}, now.Add(-2*time.Hour))
	appendPostCloseFired(path, []string{"be-3"}, now.Add(-time.Minute))

	set := readPostCloseFiredSet(path)
	for _, id := range []string{"be-1", "be-2", "be-3"} {
		if !set[id] {
			t.Errorf("ledger missing appended id %q; got %v", id, set)
		}
	}
	if set["be-absent"] {
		t.Error("readPostCloseFiredSet reported an id that was never appended")
	}

	// Prune drops entries older than the cutoff, keeps the rest.
	prunePostCloseFired(path, now.Add(-time.Hour))
	set = readPostCloseFiredSet(path)
	if set["be-1"] || set["be-2"] {
		t.Errorf("prune kept entries older than cutoff; got %v", set)
	}
	if !set["be-3"] {
		t.Errorf("prune dropped a within-window entry; got %v", set)
	}
}

// TestPostCloseLedgerEmpty verifies the safe defaults: a missing file reads as an
// empty set, and no-op inputs never create a file.
func TestPostCloseLedgerEmpty(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	missing := filepath.Join(dir, "nope")
	if set := readPostCloseFiredSet(missing); len(set) != 0 {
		t.Errorf("missing ledger must read as empty set, got %v", set)
	}

	// Empty ids and empty path are both no-ops that must not create a file.
	noop := filepath.Join(dir, "noop")
	appendPostCloseFired(noop, nil, time.Now())
	if _, err := os.Stat(noop); err == nil {
		t.Error("appendPostCloseFired with no ids must not create the ledger file")
	}
	appendPostCloseFired("", []string{"be-1"}, time.Now())
}
