package codemapops

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/steveyegge/beads/codemapops"
)

// NodeID is the deterministic primary key for a code_nodes row: a node's
// identity is its repository, kind and path, so two scans of the same
// package/file produce the same id and an upsert replaces rather than
// duplicates.
func NodeID(repoID string, kind codemapops.NodeKind, path string) string {
	h := sha256.Sum256([]byte(repoID + "\x00" + string(kind) + "\x00" + path))
	return hex.EncodeToString(h[:])
}

// PathHash is the deterministic secondary key used to index code_nodes and
// issue_files by path without storing arbitrarily long TEXT columns in a key.
func PathHash(path string) string {
	h := sha256.Sum256([]byte(path))
	return hex.EncodeToString(h[:])
}
