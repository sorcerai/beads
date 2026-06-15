//go:build cgo

package main

// Seam B of bd-6dnrw.45: CLI-level parity between direct (embedded) and
// proxied-server list semantics. One embedded project and one proxied
// project receive the same fixture (creates issued in the same order), and
// every list invocation must produce the same TITLE sequence — IDs differ
// across projects, titles are the stable key.
//
// Runs in the test-proxied-server-cmd CI lane via the ^TestProxiedServer
// naming convention.

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

type parityEnv struct {
	name string
	dir  string
	env  []string
}

func (e parityEnv) run(bd string, args ...string) (string, string, error) {
	cmd := exec.Command(bd, args...)
	cmd.Dir = e.dir
	cmd.Env = e.env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

func (e parityEnv) create(t *testing.T, bd string, args ...string) {
	t.Helper()
	fullArgs := append([]string{"create"}, args...)
	stdout, stderr, err := e.run(bd, fullArgs...)
	if err != nil {
		t.Fatalf("[%s] bd create %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			e.name, strings.Join(args, " "), err, stdout, stderr)
	}
}

func (e parityEnv) listTitles(t *testing.T, bd string, args ...string) []string {
	t.Helper()
	fullArgs := append([]string{"list", "--json"}, args...)
	stdout, stderr, err := e.run(bd, fullArgs...)
	if err != nil {
		t.Fatalf("[%s] bd list --json %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			e.name, strings.Join(args, " "), err, stdout, stderr)
	}
	return parseListTitles(t, e.name, stdout)
}

func parseListTitles(t *testing.T, envName, stdout string) []string {
	t.Helper()
	start := strings.Index(stdout, "[")
	if start < 0 {
		if strings.Contains(stdout, "null") || strings.TrimSpace(stdout) == "" {
			return nil
		}
		t.Fatalf("[%s] no JSON array found in list output:\n%s", envName, stdout)
	}
	var issues []*types.IssueWithCounts
	if err := json.Unmarshal([]byte(stdout[start:]), &issues); err != nil {
		t.Fatalf("[%s] failed to parse list output: %v\nraw: %s", envName, err, stdout[start:])
	}
	titles := make([]string, len(issues))
	for i, issue := range issues {
		titles[i] = issue.Title
	}
	return titles
}

// sleepToNextSecond parks until just past the next wall-clock second.
// created_at is DATETIME (second resolution); per-project creates must land
// in strictly increasing seconds or default-sort tie-breaking (by random
// hash ID) would make the two projects' orderings legitimately diverge.
func sleepToNextSecond() {
	now := time.Now()
	time.Sleep(now.Truncate(time.Second).Add(time.Second + 50*time.Millisecond).Sub(now))
}

func TestProxiedServerListParity(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)

	// Proxied project.
	pp := bdProxiedInit(t, bd, "par")
	proxied := parityEnv{name: "proxied", dir: pp.dir, env: bdProxiedEnv(pp.dir)}

	// Embedded project.
	eDir := t.TempDir()
	initGitRepoAt(t, eDir)
	embedded := parityEnv{name: "embedded", dir: eDir, env: bdEnv(eDir)}
	if stdout, stderr, err := embedded.run(bd, "init", "--quiet", "--prefix", "par",
		"--non-interactive", "--skip-hooks", "--skip-agents"); err != nil {
		t.Fatalf("embedded bd init failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}

	seedParityFixture(t, bd, embedded, proxied)

	// Same list invocation against both projects must yield the same title
	// sequence. Mirrors the seedProxiedListData filter surface.
	cases := []struct {
		name string
		args []string
	}{
		{"all", []string{"--all", "--limit", "0"}},
		{"default", nil},
		{"status_open", []string{"--status", "open", "--limit", "0"}},
		{"type_task", []string{"--type", "task", "--all", "--limit", "0"}},
		{"type_bug", []string{"--type", "bug", "--all", "--limit", "0"}},
		{"priority_1", []string{"--priority", "1", "--all", "--limit", "0"}},
		{"assignee_alice", []string{"--assignee", "alice", "--all", "--limit", "0"}},
		{"label_and", []string{"--label", "backend", "--label", "urgent", "--limit", "0"}},
		{"label_any", []string{"--label-any", "backend,frontend", "--limit", "0"}},
		{"ready", []string{"--ready", "--limit", "0"}},
		{"limit_3", []string{"--limit", "3"}},
		{"limit_boundary", []string{"--limit", "12"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eTitles := embedded.listTitles(t, bd, tc.args...)
			pTitles := proxied.listTitles(t, bd, tc.args...)
			if len(eTitles) == 0 && tc.name != "default" {
				t.Errorf("embedded returned no rows for %v — fixture or filter broken", tc.args)
			}
			assertSameTitleSequence(t, tc.args, eTitles, pTitles)
		})
	}

	// Documented asymmetry: --offset is rejected in direct mode and honored
	// under --proxied-server (list.go FatalError vs SQL OFFSET).
	t.Run("offset_asymmetry", func(t *testing.T) {
		stdout, stderr, err := embedded.run(bd, "list", "--limit", "2", "--offset", "2")
		if err == nil {
			t.Errorf("embedded bd list --offset should be rejected, got:\n%s", stdout)
		}
		if !strings.Contains(stdout+stderr, "--offset is only supported under --proxied-server") {
			t.Errorf("embedded --offset rejection should name the constraint, got:\nstdout: %s\nstderr: %s", stdout, stderr)
		}

		full := proxied.listTitles(t, bd, "--all", "--limit", "0")
		if len(full) < 4 {
			t.Fatalf("need >= 4 rows for the offset window, have %d", len(full))
		}
		page := proxied.listTitles(t, bd, "--all", "--limit", "2", "--offset", "2")
		want := full[2:4]
		if len(page) != 2 || page[0] != want[0] || page[1] != want[1] {
			t.Errorf("proxied --limit 2 --offset 2 = %v, want %v (window of %v)", page, want, full)
		}
	})

	// Same-on-both-sides behavior: truncation hint goes to stderr when more
	// rows matched than --limit allowed.
	t.Run("truncation_hint_parity", func(t *testing.T) {
		for _, e := range []parityEnv{embedded, proxied} {
			stdout, stderr, err := e.run(bd, "list", "--all", "--limit", "2")
			if err != nil {
				t.Fatalf("[%s] bd list --all --limit 2 failed: %v\nstderr:\n%s", e.name, err, stderr)
			}
			if !strings.Contains(stderr, "more results matched") {
				t.Errorf("[%s] expected truncation hint on stderr, got stderr %q", e.name, stderr)
			}
			if strings.Contains(stdout, "more results matched") {
				t.Errorf("[%s] truncation hint leaked into stdout:\n%s", e.name, stdout)
			}
		}
	})
}

func assertSameTitleSequence(t *testing.T, args []string, eTitles, pTitles []string) {
	t.Helper()
	if len(eTitles) != len(pTitles) {
		t.Errorf("list %v: embedded returned %d rows, proxied %d\nembedded: %v\nproxied:  %v",
			args, len(eTitles), len(pTitles), eTitles, pTitles)
		return
	}
	for i := range eTitles {
		if eTitles[i] != pTitles[i] {
			t.Errorf("list %v: title sequences diverge at %d\nembedded: %v\nproxied:  %v",
				args, i, eTitles, pTitles)
			return
		}
	}
}

// seedParityFixture creates the 12-issue seedProxiedListData shape in BOTH
// projects, interleaved so each project's creates land in strictly
// increasing created_at seconds (see sleepToNextSecond).
func seedParityFixture(t *testing.T, bd string, embedded, proxied parityEnv) {
	t.Helper()

	pastDue := time.Now().Add(-48 * time.Hour).Format("2006-01-02")
	fixture := [][]string{
		{"Open bug", "--type", "bug", "--priority", "0", "--assignee", "alice",
			"--description", "This is a bug", "--label", "backend", "--label", "urgent"},
		{"Feature request", "--type", "feature", "--priority", "1", "--assignee", "bob",
			"--label", "frontend"},
		{"Backend task", "--type", "task", "--priority", "2", "--assignee", "alice",
			"--label", "backend"},
		{"Deferred chore", "--type", "chore", "--priority", "3", "--defer", "+7d"},
		{"Epic with deps", "--type", "epic", "--priority", "1", "--label", "planning"},
		{"Architecture decision", "--type", "decision", "--priority", "4", "--label", "pinned-ref"},
		// Parent-child edges ride dotted child IDs of "Epic with deps" in
		// both projects; --parent needs per-project IDs, so child rows are
		// created with --parent below via a per-project lookup.
		{"No desc bug", "--type", "bug", "--priority", "1"},
		{"Overdue task", "--type", "task", "--priority", "1", "--assignee", "alice",
			"--label", "urgent", "--due", pastDue},
		{"Metadata issue", "--type", "feature", "--priority", "1", "--metadata", `{"env":"prod"}`},
		{"Ready task", "--type", "task", "--priority", "0", "--label", "backend"},
	}

	for _, args := range fixture {
		embedded.create(t, bd, args...)
		proxied.create(t, bd, args...)
		sleepToNextSecond()
	}

	// Child rows need the per-project epic ID for --parent.
	for _, e := range []parityEnv{embedded, proxied} {
		epicID := findTitleID(t, bd, e, "Epic with deps")
		e.create(t, bd, "Child task A", "--type", "task", "--priority", "2",
			"--assignee", "bob", "--label", "backend", "--parent", epicID)
	}
	sleepToNextSecond()
	for _, e := range []parityEnv{embedded, proxied} {
		epicID := findTitleID(t, bd, e, "Epic with deps")
		e.create(t, bd, "Child task B", "--type", "task", "--priority", "3",
			"--label", "frontend", "--parent", epicID)
	}
	sleepToNextSecond()

	for _, e := range []parityEnv{embedded, proxied} {
		titles := e.listTitles(t, bd, "--all", "--limit", "0")
		if len(titles) < 12 {
			t.Fatalf("[%s] expected at least 12 seeded issues, got %d: %v", e.name, len(titles), titles)
		}
	}
}

func findTitleID(t *testing.T, bd string, e parityEnv, title string) string {
	t.Helper()
	fullArgs := []string{"list", "--json", "--all", "--limit", "0"}
	stdout, stderr, err := e.run(bd, fullArgs...)
	if err != nil {
		t.Fatalf("[%s] bd list --json failed: %v\nstderr:\n%s", e.name, err, stderr)
	}
	start := strings.Index(stdout, "[")
	if start < 0 {
		t.Fatalf("[%s] no JSON array in list output:\n%s", e.name, stdout)
	}
	var issues []*types.IssueWithCounts
	if err := json.Unmarshal([]byte(stdout[start:]), &issues); err != nil {
		t.Fatalf("[%s] parse list output: %v", e.name, err)
	}
	for _, issue := range issues {
		if issue.Title == title {
			return issue.ID
		}
	}
	t.Fatalf("[%s] no issue titled %q found", e.name, title)
	return ""
}
