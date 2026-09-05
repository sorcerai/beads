package codemapops

import (
	"testing"
	"time"

	"github.com/steveyegge/beads/codemapops"
)

func TestNodeIDIsDeterministicAndKindScoped(t *testing.T) {
	a := NodeID("r1", codemapops.NodeFile, "a/b.go")
	if a != NodeID("r1", codemapops.NodeFile, "a/b.go") {
		t.Fatal("not deterministic")
	}
	if a == NodeID("r1", codemapops.NodePackage, "a/b.go") || a == NodeID("r2", codemapops.NodeFile, "a/b.go") {
		t.Fatal("id collides across kind or repo")
	}
	if len(a) != 64 {
		t.Fatalf("want 64 hex chars, got %d", len(a))
	}
}

func TestStaleOf(t *testing.T) {
	if StaleOf("aaa", "aaa", "x") {
		t.Fatal("same blob is not stale")
	}
	if !StaleOf("bbb", "aaa", "x") {
		t.Fatal("changed blob is stale")
	}
	if StaleOf("bbb", "", "") {
		t.Fatal("no summary is unsummarized, not stale")
	}
}

func TestRankFanIn(t *testing.T) {
	got := RankFanIn(map[string]int{"b": 2, "a": 2, "c": 5, "d": 1}, 3)
	want := []string{"c", "a", "b"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestMergeRecord(t *testing.T) {
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	req := codemapops.RecordRequest{IssueID: "i1", RepoID: "r1", Source: codemapops.SourceHook}
	fresh := MergeRecord(nil, req, "a.go", now)
	if fresh.Touches != 1 || fresh.FirstSeen != now || fresh.Source != codemapops.SourceHook {
		t.Fatalf("fresh row wrong: %+v", fresh)
	}
	req.Source, req.CommitSHA = codemapops.SourceCommit, "0123456789012345678901234567890123456789"
	later := now.Add(time.Hour)
	merged := MergeRecord(&fresh, req, "a.go", later)
	if merged.Touches != 2 || merged.FirstSeen != now || merged.LastSeen != later {
		t.Fatalf("merge wrong: %+v", merged)
	}
	if merged.Source != codemapops.SourceCommit || !merged.CommitSHA.Valid {
		t.Fatal("commit evidence must upgrade a hook observation")
	}
	req.Source, req.CommitSHA = codemapops.SourceHook, ""
	again := MergeRecord(&merged, req, "a.go", later.Add(time.Hour))
	if again.Source != codemapops.SourceCommit {
		t.Fatal("hook observation must not downgrade commit evidence")
	}
}
