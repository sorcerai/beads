package dolt

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// TestWispCreationSatisfiesEventFK is a regression guard for be-2a5.
//
// Background: a stale, pre-`db/provider`-refactor bd binary (ce242a879) failed
// every `bd mol wisp ...` (and thus every patrol-loop pour) with:
//
//	insert issue into wisps: cannot add or update a child row: a foreign key
//	constraint fails (`wisp_events`, CONSTRAINT `fk_wisp_events_issue`
//	FOREIGN KEY (`issue_id`) REFERENCES `wisps` (`id`))
//
// The `fk_wisp_events_issue` foreign key is applied to every database by the
// ignored-migration track (migrations/ignored/0004_add_wisp_aux_fks). Wisp
// creation inserts a parent row into `wisps` and then a "created" event into
// the child `wisp_events`; if the parent insert is not visible to the child
// insert within the same transaction, the FK rejects the child row.
//
// This test pins the invariant the bug violated: with the FK present AND
// enforced, creating an ephemeral wisp (parent + auto "created" event) must
// succeed. It fails if either (a) the ignored migration that adds the FK is
// dropped, or (b) the wisp-creation insert ordering regresses.
func TestWispCreationSatisfiesEventFK(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()

	ctx, cancel := testContext(t)
	defer cancel()

	// 1. The fk_wisp_events_issue foreign key must exist and target wisps(id).
	//    Guards against silently dropping the ignored-migration FK, which would
	//    let the happy-path assertion below pass without exercising the bug.
	var col, refTable, refCol string
	err := store.db.QueryRowContext(ctx, `
		SELECT column_name,
		       COALESCE(referenced_table_name, ''),
		       COALESCE(referenced_column_name, '')
		FROM information_schema.key_column_usage
		WHERE table_schema = DATABASE()
		  AND table_name = 'wisp_events'
		  AND constraint_name = 'fk_wisp_events_issue'`).Scan(&col, &refTable, &refCol)
	if err != nil {
		t.Fatalf("fk_wisp_events_issue not found on wisp_events (ignored migration 0004 not applied?): %v", err)
	}
	if col != "issue_id" || refTable != "wisps" || refCol != "id" {
		t.Fatalf("fk_wisp_events_issue shape mismatch: got %s -> %s(%s), want issue_id -> wisps(id)", col, refTable, refCol)
	}

	// 2. The FK must be enforced, not merely declared: an orphan child row whose
	//    issue_id has no parent in wisps must be rejected. This reproduces the
	//    exact failure class from be-2a5 (a child-row FK violation).
	if _, orphanErr := store.db.ExecContext(ctx, `
		INSERT INTO wisp_events (issue_id, event_type, actor)
		VALUES ('be-2a5-no-such-parent', 'created', 'test')`); orphanErr == nil {
		t.Fatal("expected orphan wisp_events insert to be rejected by fk_wisp_events_issue, but it succeeded — FK not enforced")
	}

	// 3. The be-2a5 happy path: creating an ephemeral wisp inserts the parent
	//    wisps row and a child wisp_events "created" event in one transaction,
	//    and must succeed despite the enforced FK.
	wisp := &types.Issue{
		Title:     "be-2a5 regression wisp",
		Status:    types.StatusOpen,
		Priority:  2,
		IssueType: types.TypeTask,
		Ephemeral: true,
	}
	if err := store.CreateIssue(ctx, wisp, "test"); err != nil {
		t.Fatalf("creating ephemeral wisp failed under fk_wisp_events_issue (be-2a5 regression): %v", err)
	}

	// The auto-recorded "created" event proves the child insert satisfied the FK.
	var eventCount int
	if err := store.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM wisp_events WHERE issue_id = ?", wisp.ID).Scan(&eventCount); err != nil {
		t.Fatalf("counting wisp_events for %s: %v", wisp.ID, err)
	}
	if eventCount == 0 {
		t.Errorf("wisp %s has no wisp_events row; the FK-constrained child insert did not happen", wisp.ID)
	}
}
