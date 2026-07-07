package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// TestArchExtractViolations verifies the machine-readable violation lines are
// pulled out of arch-check output: prefixed lines only, trimmed, sorted, deduped.
func TestArchExtractViolations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:   "no violation lines",
			output: "✓ arch-check: all 7 structural invariants hold\nsome other line\n",
			want:   nil,
		},
		{
			name:   "extracts and sorts",
			output: "✗ arch-check: 2 violation(s)\nviolation: [#4] b rule: x -> y\nviolation: [#1] a rule: p -> q\n",
			want:   []string{"violation: [#1] a rule: p -> q", "violation: [#4] b rule: x -> y"},
		},
		{
			name:   "trims indentation and dedupes",
			output: "violation: [#1] a rule: p -> q\n  violation: [#1] a rule: p -> q\n",
			want:   []string{"violation: [#1] a rule: p -> q"},
		},
		{
			name:   "prefix must start the line",
			output: "note about a violation: [#1] not machine-readable\n",
			want:   nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := extractViolations(tt.output)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("extractViolations() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestArchBaselineReadWrite verifies the baseline file round-trips
// deterministically and that reads skip comments and blanks.
func TestArchBaselineReadWrite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		write []string
		want  []string
	}{
		{
			name:  "empty baseline",
			write: nil,
			want:  nil,
		},
		{
			name:  "sorted and deduped on write",
			write: []string{"violation: b", "violation: a", "violation: b"},
			want:  []string{"violation: a", "violation: b"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), ".beads", "arch-baseline")
			if err := writeBaseline(path, tt.write); err != nil {
				t.Fatalf("writeBaseline: %v", err)
			}
			// Deterministic bytes: writing the same set twice is identical.
			first, _ := os.ReadFile(path)
			if err := writeBaseline(path, tt.write); err != nil {
				t.Fatalf("writeBaseline (second): %v", err)
			}
			second, _ := os.ReadFile(path)
			if string(first) != string(second) {
				t.Errorf("writeBaseline is not deterministic:\n%s\nvs\n%s", first, second)
			}
			got, err := readBaseline(path)
			if err != nil {
				t.Fatalf("readBaseline: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("round-trip = %#v, want %#v", got, tt.want)
			}
		})
	}

	t.Run("missing file is not-exist", func(t *testing.T) {
		t.Parallel()
		_, err := readBaseline(filepath.Join(t.TempDir(), "nope"))
		if !os.IsNotExist(err) {
			t.Errorf("expected not-exist error, got %v", err)
		}
	})

	t.Run("read skips comments and blanks", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), "arch-baseline")
		content := "# header\n\nviolation: b\n# mid comment\nviolation: a\nviolation: a\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := readBaseline(path)
		if err != nil {
			t.Fatalf("readBaseline: %v", err)
		}
		want := []string{"violation: a", "violation: b"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("readBaseline = %#v, want %#v", got, want)
		}
	})
}

// TestArchGateFailed pins the exit-code convention at the point the gate
// actually decides pass/fail (the composition, not gateCrashed in isolation):
// exit 1 is "violations found" and is grandfatherable, so an all-grandfathered
// run (no new violations) passes; any other non-zero exit is a crash the
// baseline cannot grandfather and must fail.
func TestArchGateFailed(t *testing.T) {
	t.Parallel()
	runExit := func(code string) error {
		return exec.Command("sh", "-c", "exit "+code).Run()
	}
	tests := []struct {
		name          string
		newViolations []string
		gateErr       error
		want          bool
	}{
		{"clean run, no error", nil, nil, false},
		{"exit 1, all grandfathered → pass", nil, runExit("1"), false},
		{"exit 2, all grandfathered → fail", nil, runExit("2"), true},
		{"new violations always fail", []string{"violation: new"}, nil, true},
		{"crash cannot be run-to-run start failure", nil, exec.Command("/no/such/binary").Run(), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := archGateFailed(tt.newViolations, tt.gateErr); got != tt.want {
				t.Errorf("archGateFailed(%v, %v) = %v, want %v", tt.newViolations, tt.gateErr, got, tt.want)
			}
		})
	}
}

// TestArchDiffBaseline verifies the new-vs-grandfathered split.
func TestArchDiffBaseline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		current   []string
		baseline  []string
		wantNew   []string
		wantGrand int
	}{
		{
			name:      "no baseline entries — everything is new",
			current:   []string{"violation: a", "violation: b"},
			baseline:  nil,
			wantNew:   []string{"violation: a", "violation: b"},
			wantGrand: 0,
		},
		{
			name:      "all grandfathered",
			current:   []string{"violation: a"},
			baseline:  []string{"violation: a", "violation: gone"},
			wantNew:   nil,
			wantGrand: 1,
		},
		{
			name:      "mixed",
			current:   []string{"violation: a", "violation: new"},
			baseline:  []string{"violation: a"},
			wantNew:   []string{"violation: new"},
			wantGrand: 1,
		},
		{
			name:      "clean run",
			current:   nil,
			baseline:  []string{"violation: a"},
			wantNew:   nil,
			wantGrand: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotNew, gotGrand := diffBaseline(tt.current, tt.baseline)
			if !reflect.DeepEqual(gotNew, tt.wantNew) {
				t.Errorf("new = %#v, want %#v", gotNew, tt.wantNew)
			}
			if gotGrand != tt.wantGrand {
				t.Errorf("grandfathered = %d, want %d", gotGrand, tt.wantGrand)
			}
		})
	}
}
