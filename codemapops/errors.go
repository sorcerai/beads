package codemapops

import "github.com/steveyegge/beads/beadserrors"

// ErrValidation classifies this plane's deterministic request-validation
// failures. It is an alias of beadserrors.ErrValidation, not a second
// sentinel — see memoryops.ErrValidation for why that identity matters.
var ErrValidation = beadserrors.ErrValidation

// ErrNotIndexed reports that a repository has no code map built yet.
type ErrNotIndexed struct{ RepoID string }

func (e *ErrNotIndexed) Error() string {
	return "code map not built for repository " + e.RepoID + " (run: bd codemap build)"
}
