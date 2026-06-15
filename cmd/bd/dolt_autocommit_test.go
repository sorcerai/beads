package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestFormatDoltAutoCommitMessage(t *testing.T) {
	msg := formatDoltAutoCommitMessage("update", "alice", []string{"bd-2", "bd-1", "bd-2", "", "bd-3"})
	if msg != "bd: update (auto-commit) by alice [bd-1, bd-2, bd-3]" {
		t.Fatalf("unexpected message: %q", msg)
	}

	// Caps IDs (max 5) and sorts
	msg = formatDoltAutoCommitMessage("create", "bob", []string{"z-9", "a-1", "m-3", "b-2", "c-4", "d-5", "e-6"})
	if msg != "bd: create (auto-commit) by bob [a-1, b-2, c-4, d-5, e-6]" {
		t.Fatalf("unexpected capped message: %q", msg)
	}

	// Empty command/actor fallbacks
	msg = formatDoltAutoCommitMessage("", "", nil)
	if msg != "bd: write (auto-commit) by unknown" {
		t.Fatalf("unexpected fallback message: %q", msg)
	}
}

// findTestCommand walks the REAL command tree so the exemption assertions
// break if a covered command is renamed or moved.
func findTestCommand(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	cmd := rootCmd
	for _, name := range path {
		var next *cobra.Command
		for _, c := range cmd.Commands() {
			if c.Name() == name {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("command %q not found under %q", name, cmd.CommandPath())
		}
		cmd = next
	}
	return cmd
}

// TestAutoCommitSweepExempt covers bd-578h9.7: the dirty-working-set sweep
// must not run after inspection commands (bd dolt status would commit the
// dirty state it just displayed) or read-only commands (their store is opened
// read-only, so the sweep's commit fails and turns a successful read fatal).
func TestAutoCommitSweepExempt(t *testing.T) {
	cases := []struct {
		path   []string
		exempt bool
	}{
		{[]string{"dolt", "status"}, true},
		{[]string{"vc", "status"}, true},
		{[]string{"diff"}, true},
		{[]string{"history"}, true},
		{[]string{"list"}, true},  // readOnlyCommands
		{[]string{"ready"}, true}, // readOnlyCommands
		{[]string{"create"}, false},
		{[]string{"update"}, false},
		{[]string{"dolt", "commit"}, false},
		{[]string{"vc", "merge"}, false},
	}
	for _, tc := range cases {
		cmd := findTestCommand(t, tc.path...)
		if got := autoCommitSweepExempt(cmd); got != tc.exempt {
			t.Errorf("autoCommitSweepExempt(%q) = %v, want %v", cmd.CommandPath(), got, tc.exempt)
		}
	}
}

func TestFormatDoltSweepCommitMessage(t *testing.T) {
	msg := formatDoltSweepCommitMessage("update", "alice")
	if !strings.Contains(msg, "sweep") || !strings.Contains(msg, "update") || !strings.Contains(msg, "alice") {
		t.Fatalf("sweep message must name the sweep, the triggering command, and the actor: %q", msg)
	}
	if msg == formatDoltAutoCommitMessage("update", "alice", nil) {
		t.Fatal("sweep commits must be distinguishable from normal auto-commits")
	}
	msg = formatDoltSweepCommitMessage("", "")
	if !strings.Contains(msg, "write") || !strings.Contains(msg, "unknown") {
		t.Fatalf("unexpected fallback message: %q", msg)
	}
}

func TestIsDoltNothingToCommit(t *testing.T) {
	if isDoltNothingToCommit(nil) {
		t.Fatal("nil error should not be treated as nothing-to-commit")
	}
	if !isDoltNothingToCommit(errors.New("nothing to commit")) {
		t.Fatal("expected nothing-to-commit to be detected")
	}
	if !isDoltNothingToCommit(errors.New("No changes to commit")) {
		t.Fatal("expected no-changes-to-commit to be detected")
	}
	if isDoltNothingToCommit(errors.New("permission denied")) {
		t.Fatal("unexpected classification")
	}
}

func TestGetDoltAutoCommitMode_Batch(t *testing.T) {
	old := doltAutoCommit
	defer func() { doltAutoCommit = old }()

	doltAutoCommit = "batch"
	mode, err := getDoltAutoCommitMode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != doltAutoCommitBatch {
		t.Fatalf("expected batch, got %q", mode)
	}

	// Also verify the other modes still work
	doltAutoCommit = "on"
	mode, err = getDoltAutoCommitMode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != doltAutoCommitOn {
		t.Fatalf("expected on, got %q", mode)
	}

	doltAutoCommit = "off"
	mode, err = getDoltAutoCommitMode()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mode != doltAutoCommitOff {
		t.Fatalf("expected off, got %q", mode)
	}

	// Invalid mode
	doltAutoCommit = "invalid"
	_, err = getDoltAutoCommitMode()
	if err == nil {
		t.Fatal("expected error for invalid mode")
	}
}
