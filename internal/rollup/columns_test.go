package rollup

import (
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestColumnForStatus_BuiltIns(t *testing.T) {
	cases := map[types.Status]Column{
		types.StatusOpen:       ColumnTodo,
		types.StatusInProgress: ColumnInProgress,
		types.StatusBlocked:    ColumnInProgress,
		types.StatusHooked:     ColumnInProgress,
		types.StatusClosed:     ColumnDone,
		types.StatusDeferred:   ColumnDeferred,
		types.StatusPinned:     ColumnDeferred,
	}
	for st, want := range cases {
		if got := ColumnForStatus(st, nil); got != want {
			t.Errorf("ColumnForStatus(%q) = %q, want %q", st, got, want)
		}
	}
}

func TestColumnForStatus_CustomCategory(t *testing.T) {
	custom := map[string]types.StatusCategory{
		"in_review": types.CategoryWIP,
		"icebox":    types.CategoryFrozen,
	}
	if got := ColumnForStatus(types.Status("in_review"), custom); got != ColumnInProgress {
		t.Errorf("custom wip status = %q, want %q", got, ColumnInProgress)
	}
	if got := ColumnForStatus(types.Status("icebox"), custom); got != ColumnDeferred {
		t.Errorf("custom frozen status = %q, want %q", got, ColumnDeferred)
	}
}

func TestColumnForStatus_UnknownFallsBack(t *testing.T) {
	if got := ColumnForStatus(types.Status("weird"), nil); got != ColumnFallback {
		t.Errorf("unknown status = %q, want %q", got, ColumnFallback)
	}
}

func TestColumnForStatus_CustomUnspecifiedCategoryFallsBack(t *testing.T) {
	custom := map[string]types.StatusCategory{"vague": types.CategoryUnspecified}
	if got := ColumnForStatus(types.Status("vague"), custom); got != ColumnFallback {
		t.Errorf("custom unspecified-category status = %q, want %q", got, ColumnFallback)
	}
}
