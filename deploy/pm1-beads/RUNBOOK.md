# pm1 Beads Ticketing — Runbook

Linear-style shared issue tracker: a Dolt SQL server on pm1, reached over
Tailscale, with the Mac + agents as server-mode `bd` clients.

## Facts

- **Host:** `100.85.126.95` (Tailscale; SSH alias `pm1-webdock`), user `admin`,
  key `/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa`.
- **Server:** systemd `dolt-sql-server.service`, data `/home/admin/beads-data`,
  bound **only** to `100.85.126.95:3307` (not reachable on the public IP).
- **DB:** `beads_workspace`. SQL user `beads` (password in gitignored
  `deploy/pm1-beads/beads-client.env`). Remote `root` is denied; `FILE`
  privilege revoked from `beads`.
- **Off-limits:** `34.241.191.3` (SSH alias `pm1`, AWS) — never touched by any
  of this.
- **Co-exist:** pm1 was NOT wiped. It still runs a **live `polymarket-rs`
  stack** + Postgres (`:5432`) + a `:6380` service. Ghost (`ghost_daily_sync`)
  was neutralized (cron removed, files in `~/_killed-ghost-2026-05-17`).

## Client setup

See `client-setup.md`. Short version:

```bash
source deploy/pm1-beads/beads-client.env
cd ~/beads-workspace          # first time: bd init --server \
                              #   --server-host 100.85.126.95 --server-port 3307 \
                              #   --server-user beads --database beads_workspace
bd list
```

`--server-user beads` is mandatory on `bd init` (default is `root`, which is
disabled). A new client sharing the same board must pass
`--database beads_workspace`.

## Common ops

- Status:  `ssh -i <key> admin@100.85.126.95 'systemctl status dolt-sql-server.service'`
- Restart: `ssh ... 'sudo systemctl restart dolt-sql-server.service'`
- Logs:    `ssh ... 'journalctl -u dolt-sql-server.service -n 100 --no-pager'`
- Admin SQL: `dolt --host 100.85.126.95 --port 3307 --user beads --password <pw> --no-tls sql -q "..."`

## Backup / DR

`bd backup` does NOT support server mode. DR is portable JSONL pulled to the
Mac (zero pm1 disk):

- Script: `deploy/pm1-beads/backup.sh` → snapshots to `~/beads-backups/`,
  symlink `latest.jsonl`, keeps 30, logs to `~/beads-backups/backup.log`.
- Schedule: Mac cron, daily 09:30 local.
- **Restore:** `mkdir restore && cd restore && bd init && bd import ~/beads-backups/latest.jsonl`

## If pm1 dies

1. New host on the tailnet (or reuse). Install `dolt` + `bd`.
2. `dolt init` a data dir; recreate the `beads` SQL user (see below).
3. Install/start `dolt-sql-server.service` (this dir's unit).
4. From the Mac: `bd init --server ... --server-user beads --database beads_workspace`,
   then `bd import ~/beads-backups/latest.jsonl`.

## Rotate the Dolt password

```
dolt --host 100.85.126.95 --port 3307 --user beads --password <old> --no-tls \
  sql -q "ALTER USER 'beads'@'%' IDENTIFIED BY '<new>';"
```
Then update `BEADS_DOLT_PASSWORD` in `deploy/pm1-beads/beads-client.env` on every client.

## Disk note

pm1 `/` ran ~92% full; the hog is reclaimable Docker build cache/images
(~50 GB), not beads (DB <1 MB). Safe reclaim (touches nothing live):
`sudo docker builder prune -af; sudo docker image prune -af; sudo apt-get clean`.
