package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// postCloseWatermarkName is the machine-local file recording the newest
// closed_at the post-close reconciliation sweep has already processed. It lives
// under .beads/ (gitignored by the .beads/* rule) and is deliberately NOT
// synced: the post-close hook is a per-machine advisory (e.g. arch-check), so an
// in-DB flag would mark it "done" fleet-wide the moment it synced — a bug, not a
// feature, for this class of hook.
const postCloseWatermarkName = "post-close-watermark"

// postCloseLedgerName is the machine-local, append-only record of issue IDs this
// machine has already handled (fired the hook for, or explicitly opted out of).
// It lives beside the watermark under .beads/ (gitignored) and is the sweep's
// dedup source of truth — the watermark only bounds how far back the sweep looks.
const postCloseLedgerName = "post-close-fired"

// postCloseLedgerRetention is how long a fired-ledger entry is kept. Entries are
// only needed until the watermark has advanced past their close, which happens on
// the very next sweep; 30 days is a wide safety margin against clock skew and
// long-idle machines, and a few thousand lines cost nothing to scan.
const postCloseLedgerRetention = 30 * 24 * time.Hour

// sweepMissedCloses fires the post-close hook for closes that happened WITHOUT
// this machine firing it: closed on another machine and synced in via Dolt,
// closed by a different client, or closed while the hook file was missing. It
// runs opportunistically at the start of `bd ready` / `bd prime`.
//
// Advisory contract, identical to firePostCloseHook: silent when idle, bounded
// in time, skipped when no hook resolves or BD_NO_CLOSE_HOOK=1, and it never
// fails or perceptibly slows the parent command — every error is logged and
// swallowed. Dedup comes from the fired-ledger: any ID this machine already
// handled (direct-fired or opted out) is skipped, so a direct `bd close` is never
// re-fired by the next sweep.
func sweepMissedCloses(ctx context.Context, s storage.DoltStorage) {
	if os.Getenv("BD_NO_CLOSE_HOOK") == "1" {
		return
	}
	// Cheap check first: with no post-close hook there is nothing to sweep for.
	// This resolves from cwd when s is nil, so it stays free of any DB work.
	if resolvePostCloseHookPathForStore(s) == "" {
		return
	}
	if s == nil {
		return // The query needs a store; prime opens one before calling.
	}

	wmPath := postCloseWatermarkPath(s)
	if wmPath == "" {
		return
	}

	watermark, ok := readPostCloseWatermark(wmPath)
	if !ok {
		// First run on this machine: seed the watermark to now and sweep
		// nothing. A fresh machine must not re-fire hooks for years of history.
		if err := writePostCloseWatermark(wmPath, time.Now()); err != nil {
			debug.Logf("post-close sweep: seeding watermark failed: %v\n", err)
		} else {
			debug.Logf("post-close sweep: seeded watermark %s (first run, swept nothing)\n", wmPath)
		}
		return
	}

	// Bound the query in time so a slow store can't stall the parent command.
	queryCtx, cancel := context.WithTimeout(ctx, postCloseHookTimeoutValue())
	defer cancel()

	// Inclusive lower bound: ClosedAfter is a strict `>` at RFC3339 (second)
	// precision, so query from one second before the watermark to include closes
	// stamped exactly at the watermark second (a sibling closed in the same second
	// the last sweep advanced through). The ledger dedups the resulting overlap.
	lowerBound := watermark.Add(-time.Second)

	// No Limit: the 60s queryCtx (postCloseHookTimeoutValue) is the safety bound,
	// and the watermark keeps the window to closes since the last sweep. A row
	// cap would be unsafe — the engine orders by priority, not closed_at, so a
	// truncated page can never rotate under ledger-dedup, permanently skipping
	// rows ranked below the cut. Worst case: a long-offline machine syncs a large
	// closed backlog and fires it in one sweep — acceptable under the advisory
	// contract, and firePostCloseHook chunks the hook argv so it can't overflow.
	statusClosed := types.StatusClosed
	closed, err := s.SearchIssues(queryCtx, "", types.IssueFilter{
		Status:      &statusClosed,
		ClosedAfter: &lowerBound,
	})
	if err != nil {
		debug.Logf("post-close sweep: query failed (non-blocking): %v\n", err)
		return
	}
	if len(closed) == 0 {
		return
	}

	// Skip IDs this machine already handled; fire the rest.
	fired := readPostCloseFiredSet(postCloseLedgerPath(s))
	ids := make([]string, 0, len(closed))
	newest := watermark
	for _, iss := range closed {
		if iss.ClosedAt != nil && iss.ClosedAt.After(newest) {
			newest = *iss.ClosedAt
		}
		if fired[iss.ID] {
			continue
		}
		ids = append(ids, iss.ID)
	}

	// Fire the hook for the un-handled closes. Use the parent ctx (not queryCtx)
	// so the hook gets its own full timeout, independent of the query spend.
	// firePostCloseHook chunks the argv and records these IDs to the ledger.
	if len(ids) > 0 {
		firePostCloseHook(ctx, s, ids)
	}

	// Always advance the watermark to the newest close we saw. The ledger — not
	// the watermark — dedups, so advancing can never skip un-handled work; it only
	// shrinks the next look-back window and keeps the ledger prunable.
	// ponytail: concurrent sweeps (two bd processes) can double-fire the last
	// window; harmless under the advisory/idempotent contract, so no lock.
	if newest.After(watermark) {
		if err := writePostCloseWatermark(wmPath, newest); err != nil {
			debug.Logf("post-close sweep: advancing watermark failed: %v\n", err)
		}
	}

	// Drop ledger entries older than the retention window (best-effort).
	prunePostCloseFired(postCloseLedgerPath(s), time.Now().Add(-postCloseLedgerRetention))
}

// maybeSweepMissedClosesForPrime runs the sweep from `bd prime`, which is in
// noDbCommands and thus starts with no open store. It does the cheap hook check
// BEFORE opening a store, so the common (no-hook) case stays fast and prime
// never pays the DB-open cost just to no-op.
func maybeSweepMissedClosesForPrime(ctx context.Context) {
	if os.Getenv("BD_NO_CLOSE_HOOK") == "1" {
		return
	}
	if resolvePostCloseHookPath() == "" {
		return // No hook resolvable from cwd — skip without touching the store.
	}
	if store == nil {
		timeout := primeStoreTimeout()
		openCtx := context.Background()
		if timeout > 0 {
			var cancel context.CancelFunc
			openCtx, cancel = context.WithTimeout(openCtx, timeout)
			defer cancel()
		}
		if err := ensureStoreActiveForPrime(openCtx); err != nil {
			return // Store unavailable; the sweep is strictly best-effort.
		}
	}
	sweepMissedCloses(ctx, store)
}

// postCloseLocalPath returns a machine-local .beads file path, in the same
// .beads dir the post-close hook resolves from (store workspace preferred, cwd
// fallback). Returns "" when no .beads dir can be determined.
func postCloseLocalPath(s storage.DoltStorage, name string) string {
	if bd := storeBeadsDir(s); bd != "" {
		return filepath.Join(bd, name)
	}
	if bd := beads.FindBeadsDir(); bd != "" {
		return filepath.Join(bd, name)
	}
	return ""
}

// postCloseWatermarkPath is the machine-local watermark file.
func postCloseWatermarkPath(s storage.DoltStorage) string {
	return postCloseLocalPath(s, postCloseWatermarkName)
}

// postCloseLedgerPath is the machine-local fired-ledger file.
func postCloseLedgerPath(s storage.DoltStorage) string {
	return postCloseLocalPath(s, postCloseLedgerName)
}

// recordPostCloseFired appends issue IDs to this machine's fired-ledger, marking
// them handled so the reconciliation sweep won't re-fire them. Best-effort: a
// failed append only risks one redundant advisory re-fire, never a lost close.
func recordPostCloseFired(s storage.DoltStorage, ids []string) {
	appendPostCloseFired(postCloseLedgerPath(s), ids, time.Now().UTC())
}

// appendPostCloseFired appends "<RFC3339 t> <id>" lines to the ledger at path.
// Path "" or empty ids is a no-op; errors are logged and swallowed.
func appendPostCloseFired(path string, ids []string, t time.Time) {
	if path == "" || len(ids) == 0 {
		return
	}
	ts := t.Format(time.RFC3339)
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(ts)
		b.WriteByte(' ')
		b.WriteString(id)
		b.WriteByte('\n')
	}
	// #nosec G304 -- path is .beads/post-close-fired, constructed by us.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		debug.Logf("post-close ledger: append open failed: %v\n", err)
		return
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(b.String()); err != nil {
		debug.Logf("post-close ledger: append write failed: %v\n", err)
	}
}

// readPostCloseFiredSet returns the set of issue IDs currently in the ledger.
// Malformed lines are skipped; a missing file reads as the empty set.
func readPostCloseFiredSet(path string) map[string]bool {
	set := map[string]bool{}
	if path == "" {
		return set
	}
	// #nosec G304 -- path is .beads/post-close-fired, constructed by us.
	data, err := os.ReadFile(path)
	if err != nil {
		return set
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 {
			set[fields[1]] = true
		}
	}
	return set
}

// prunePostCloseFired rewrites the ledger, dropping entries whose timestamp is
// before cutoff. Best-effort: on any read/write error the ledger is left intact
// (it only ever grows or over-retains — never drops a still-needed entry).
func prunePostCloseFired(path string, cutoff time.Time) {
	if path == "" {
		return
	}
	// #nosec G304 -- path is .beads/post-close-fired, constructed by us.
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	keep := make([]string, 0)
	changed := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if fields := strings.Fields(line); len(fields) >= 2 {
			if t, err := time.Parse(time.RFC3339, fields[0]); err == nil && t.Before(cutoff) {
				changed = true
				continue
			}
		}
		keep = append(keep, line)
	}
	if !changed {
		return
	}
	out := ""
	if len(keep) > 0 {
		out = strings.Join(keep, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
		debug.Logf("post-close ledger: prune write failed: %v\n", err)
	}
}

// readPostCloseWatermark reads the RFC3339 watermark. ok=false means "no
// watermark yet" (missing, empty, or corrupt file) — the caller treats that as a
// first run and seeds it rather than storming through history.
func readPostCloseWatermark(path string) (time.Time, bool) {
	// #nosec G304 -- path is .beads/post-close-watermark, constructed by us.
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(data)))
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// writePostCloseWatermark records the RFC3339 watermark.
func writePostCloseWatermark(path string, t time.Time) error {
	return os.WriteFile(path, []byte(t.Format(time.RFC3339)+"\n"), 0o600)
}
