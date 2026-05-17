#!/usr/bin/env bash
# Off-box DR for the pm1 beads workspace. Runs from the Mac (a server-mode
# client), pulls all issues as JSONL. Zero pm1 disk usage. bd's own
# 'bd backup' does not support server mode, so we use the portable JSONL
# export/import path. Restore: `bd import < snapshot.jsonl` into a workspace.
set -euo pipefail
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
DEST="${1:-$HOME/beads-backups}"
mkdir -p "$DEST"
cd "$HOME/beads-workspace"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
OUT="$DEST/beads_workspace-$TS.jsonl"
bd export > "$OUT"
ln -sf "$OUT" "$DEST/latest.jsonl"
# keep the 30 most recent snapshots
ls -1t "$DEST"/beads_workspace-*.jsonl 2>/dev/null | tail -n +31 | xargs -r rm -f
echo "backup: $(wc -l < "$OUT") issues -> $OUT at $(date -u)"
