package scout

import (
	"errors"
	"sync"

	"github.com/steveyegge/beads/codemapops"
)

var (
	registryMu sync.Mutex
	registry   []Scout
)

// Register adds s to the set of scouts ScanAll considers. Language scouts call
// it from init().
func Register(s Scout) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = append(registry, s)
}

// Detect returns the registered scouts that recognize root, in registration
// order.
func Detect(root string) []Scout {
	registryMu.Lock()
	all := append([]Scout(nil), registry...)
	registryMu.Unlock()
	var found []Scout
	for _, s := range all {
		if s.Detect(root) {
			found = append(found, s)
		}
	}
	return found
}

// ScanAll runs every scout that detects root. A scout whose external tool is
// missing contributes to missing and is otherwise skipped, so one absent
// toolchain does not fail the whole scan; any other error aborts.
func ScanAll(root string, only []string) (graphs []codemapops.Graph, missing []error, err error) {
	for _, s := range Detect(root) {
		g, scanErr := s.Scan(root, only)
		if scanErr != nil {
			var toolErr *ErrToolMissing
			if errors.As(scanErr, &toolErr) {
				missing = append(missing, scanErr)
				continue
			}
			return nil, missing, scanErr
		}
		graphs = append(graphs, g)
	}
	return graphs, missing, nil
}
