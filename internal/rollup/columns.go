// Package rollup computes a project→epic→status-category board view over
// the beads issue store. It depends only on a narrow read interface and the
// canonical status-category mapping; it contains no SQL (spec constraint C3).
package rollup

import "github.com/steveyegge/beads/internal/types"

// Column is a fixed board lane. The set is constant regardless of which
// custom statuses exist (spec: never derive columns dynamically).
type Column string

const (
	ColumnTodo       Column = "todo"        // category: active
	ColumnInProgress Column = "in_progress" // category: wip
	ColumnDone       Column = "done"        // category: done
	ColumnDeferred   Column = "deferred"    // category: frozen
	ColumnFallback   Column = "fallback"    // unspecified / unknown custom
)

// columnForCategory maps a StatusCategory to a fixed board column.
func columnForCategory(c types.StatusCategory) Column {
	switch c {
	case types.CategoryActive:
		return ColumnTodo
	case types.CategoryWIP:
		return ColumnInProgress
	case types.CategoryDone:
		return ColumnDone
	case types.CategoryFrozen:
		return ColumnDeferred
	default:
		return ColumnFallback
	}
}

// ColumnForStatus returns the board column for a status. Built-in statuses
// use types.BuiltInStatusCategory. Custom statuses use their self-declared
// category from customCategories (status name -> category); unknown -> fallback.
func ColumnForStatus(s types.Status, customCategories map[string]types.StatusCategory) Column {
	cat := types.BuiltInStatusCategory(s)
	if cat == types.CategoryUnspecified {
		if cc, ok := customCategories[string(s)]; ok {
			cat = cc
		}
	}
	return columnForCategory(cat)
}
