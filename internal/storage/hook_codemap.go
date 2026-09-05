package storage

import "github.com/steveyegge/beads/codemapops"

// CodeMapIndexer, CodeMapReader and IssueFiles all RECURSE UNWRAPPED, and that
// is a decision rather than an omission.
//
// Apply, SetSummaries and Record are writes, so the reflex is to reach for
// hook_commenter — but this decorator's whole vocabulary is on_create,
// on_update and on_close, and every one of them hands a hook script an ISSUE.
// None of these three operations produces one: an apply replaces nodes and
// edges, a summary pass writes prose onto a node, and a Record binds an issue
// that already existed to a path. Inventing an on_index or on_link event so
// this file could fire one would be a hook-vocabulary proposal wearing a role
// commit's clothes. This is the Sweeper case, not the Commenter case.
//
// So the accessors EXIST on this decorator — declared, never inherited — and
// add no layer. If a code-map event ever joins the hook vocabulary, this file
// is where it lands. See the paragraph on Storage.CodeMapIndexer.
func (h *HookFiringStore) CodeMapIndexer() (codemapops.Indexer, error) {
	return h.inner.CodeMapIndexer()
}
func (h *HookFiringStore) CodeMapReader() (codemapops.Reader, error)  { return h.inner.CodeMapReader() }
func (h *HookFiringStore) IssueFiles() (codemapops.IssueFiles, error) { return h.inner.IssueFiles() }
