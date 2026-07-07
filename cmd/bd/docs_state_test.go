package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDocsStateRoundtrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, ".docs-state")

	// Missing file: ok=false.
	if _, ok := readDocsState(p); ok {
		t.Fatal("expected ok=false for missing state file")
	}

	want := docsState{
		RegenWatermark: time.Date(2026, 7, 4, 5, 0, 0, 0, time.UTC),
		RegenStarted:   time.Date(2026, 7, 4, 5, 30, 0, 0, time.UTC),
	}
	if err := writeDocsState(p, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, ok := readDocsState(p)
	if !ok || !got.RegenWatermark.Equal(want.RegenWatermark) || !got.RegenStarted.Equal(want.RegenStarted) {
		t.Fatalf("roundtrip mismatch: ok=%v got=%+v want=%+v", ok, got, want)
	}

	// RegenStarted zero value ("no regen in flight") must round-trip as zero too.
	zeroStarted := docsState{RegenWatermark: want.RegenWatermark}
	if err := writeDocsState(p, zeroStarted); err != nil {
		t.Fatalf("write zero RegenStarted: %v", err)
	}
	got2, ok := readDocsState(p)
	if !ok || !got2.RegenStarted.IsZero() {
		t.Fatalf("zero RegenStarted did not round-trip as zero: %+v", got2)
	}
	want = zeroStarted

	// Deterministic bytes: two writes of the same state are identical.
	b1, _ := os.ReadFile(p)
	if err := writeDocsState(p, want); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	b2, _ := os.ReadFile(p)
	if string(b1) != string(b2) {
		t.Fatal("state file bytes not deterministic")
	}

	// Corrupt file: ok=false.
	if err := os.WriteFile(p, []byte("not: [valid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readDocsState(p); ok {
		t.Fatal("expected ok=false for corrupt state file")
	}
}

// TestDocsStateTolerantOfOldDirtyLine covers F1: state files written before
// the stored dirty counter was removed still have a "dirty: N" line. That
// line must be ignored, not treated as corruption.
func TestDocsStateTolerantOfOldDirtyLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, ".docs-state")
	old := "regen_watermark: 2026-07-01T00:00:00Z\ndirty: 12\n"
	if err := os.WriteFile(p, []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	st, ok := readDocsState(p)
	if !ok {
		t.Fatal("old-format state file with a dirty: line must still parse")
	}
	if !st.RegenStarted.IsZero() {
		t.Fatalf("old-format file has no regen_started — must default to zero, got %v", st.RegenStarted)
	}
}
