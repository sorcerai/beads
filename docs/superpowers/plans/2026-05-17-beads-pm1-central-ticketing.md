# Beads Central Ticketing on pm1 — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up an always-on `dolt sql-server` on the old pm1 box, reachable only over Tailscale, and connect the Mac Studio + agents as multi-writer beads clients against one shared issue workspace.

**Architecture:** Approach A from the design spec (`docs/superpowers/specs/2026-05-17-beads-pm1-central-ticketing-design.md`). Single Dolt SQL server on `100.85.126.95:3307` bound to the Tailscale IP, fronted by `systemd` + `ufw`; clients run `bd` in server mode. `34.241.191.3` (live polyweather) is never referenced by any command in this plan.

**Tech Stack:** Dolt (`>=1.88.1`), beads `bd` (`>=0.59.0`), systemd, ufw, Tailscale, SSH.

---

## Operating Constraints (read before any task)

- **Destructive scope is `100.85.126.95` ONLY.** Every `ssh` / `rsync` command in this plan uses the variable below. Never substitute `34.241.191.3` or SSH alias `pm1`.
- **SSH permission:** the executor will hit a permission gate the first time it SSHes to pm1 (the classifier flags remote shells). The user must approve SSH access to `100.85.126.95`. Do not attempt to bypass; if denied, stop and ask the user.
- **Phase 0 destruction is pre-authorized** (full reprovision, no inventory gate) per the user. A pre-wipe snapshot is still captured locally as cheap insurance — it does **not** block.

**Shared shell variables (set once per task that uses SSH):**

```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
PM1="admin@100.85.126.95"
PM1_SSH="ssh -i $PM1_KEY -o StrictHostKeyChecking=no -o ConnectTimeout=12 $PM1"
DOLT_PORT=3307
DOLT_DATA="/home/admin/beads-data"      # chosen over /var/lib/beads: admin owns it, no sudo for data ops
```

---

## File Structure

All generated artifacts live under `deploy/pm1-beads/` in this repo (local ops; add to `.gitignore` if you don't want them upstream — see Task 8).

- Create: `deploy/pm1-beads/dolt-sql-server.service` — systemd unit for the Dolt server
- Create: `deploy/pm1-beads/preexisting-inventory-2026-05-17.txt` — pre-wipe snapshot (generated)
- Create: `deploy/pm1-beads/client-config.yaml` — beads server-mode client config (`dolt: mode/host/port/user` ONLY — no password/database keys; those are not valid beads config keys)
- Create: `deploy/pm1-beads/beads-client.env` — generated; exports `BEADS_DOLT_*` incl. password (chmod 600, **gitignored**); clients `source` this
- Create: `deploy/pm1-beads/backup.sh` — Mac-side backup script (off-box DR)
- Create: `deploy/pm1-beads/RUNBOOK.md` — operations runbook

**Auth model (corrected):** beads has no `password:`/`database:` config key. Password resolution (docs/DOLT.md:348-374) is: `BEADS_DOLT_PASSWORD` env (highest) → `~/.config/beads/credentials` `[host:port]` section → empty. This plan uses the env-var path via a sourced `beads-client.env` (doc-guaranteed, no INI-format guesswork). The beads project database is provisioned by `bd init --server`, not by manual `CREATE DATABASE`.

---

## Task 0: Preflight — connectivity, tooling, no-wrong-host guard

**Files:** none (verification only)

- [ ] **Step 1: Verify the off-limits host is never aliased into our commands**

Run:
```bash
grep -A3 -i '^Host pm1$' ~/.ssh/config
```
Expected: shows `HostName 34.241.191.3`. Confirm our plan uses `admin@100.85.126.95` directly and the literal IP `34.241.191.3` appears in **no** task command.

- [ ] **Step 2: Verify Tailscale + SSH reach pm1 (this triggers the SSH permission prompt — approve for 100.85.126.95)**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 'echo OK; whoami; uname -sr; id; sudo -n true 2>&1 && echo SUDO_NOPASSWD || echo SUDO_NEEDS_PASSWORD'
```
Expected: `OK`, `admin`, a Linux kernel string, and either `SUDO_NOPASSWD` or `SUDO_NEEDS_PASSWORD`.
If `SUDO_NEEDS_PASSWORD`: stop and ask the user for the sudo password before continuing (Tasks 1/3/4 need sudo).

- [ ] **Step 3: Verify local (Mac) tooling**

Run:
```bash
bd version; dolt version
```
Expected: `bd` reports `>= 0.59.0` and `dolt` reports `>= 1.88.1`.
If either is missing/old: install/upgrade — `brew install dolt` and the beads install script from README — then re-run before proceeding.

- [ ] **Step 4: Create artifact directory**

Run:
```bash
mkdir -p /Users/ariapramesi/repos/beads/deploy/pm1-beads
```
Expected: directory exists, no error.

---

## Task 1: Phase 0 — snapshot then reprovision pm1

**Files:**
- Create: `deploy/pm1-beads/preexisting-inventory-2026-05-17.txt`

- [ ] **Step 1: Capture a pre-wipe snapshot to a LOCAL file (insurance, non-gating)**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 '
  echo "### date"; date -u;
  echo "### systemd running units"; systemctl list-units --type=service --state=running --no-pager 2>/dev/null;
  echo "### docker"; (docker ps -a 2>/dev/null || echo none);
  echo "### crontab admin"; (crontab -l 2>/dev/null || echo none);
  echo "### listening ports"; (ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null);
  echo "### home"; ls -la /home/admin 2>/dev/null;
  echo "### disk"; df -h /
' > /Users/ariapramesi/repos/beads/deploy/pm1-beads/preexisting-inventory-2026-05-17.txt 2>&1
```
Expected: file written, non-empty.

- [ ] **Step 2: Review the snapshot for anything surprising**

Run:
```bash
grep -iE 'polyweather|34\.241|postgres|important|prod' /Users/ariapramesi/repos/beads/deploy/pm1-beads/preexisting-inventory-2026-05-17.txt || echo "nothing flagged"
```
Expected: either "nothing flagged", or hits that are clearly the dead pre-AWS polyweather artifacts. If anything looks live/unexpected, **stop and ask the user** (user authorized wipe believing the box is dead — surface contradictions).

- [ ] **Step 3: Stop and disable old workloads (reprovision)**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 '
  set -x;
  ( crontab -r 2>/dev/null || true );
  ( docker ps -aq 2>/dev/null | xargs -r docker rm -f ) 2>/dev/null || true;
  for u in $(systemctl list-units --type=service --state=running --no-pager 2>/dev/null | grep -iE "polyweather|trade|webdock-app" | awk "{print \$1}"); do
    sudo systemctl stop "$u"; sudo systemctl disable "$u";
  done;
  echo REPROVISION_DONE
'
```
Expected: ends with `REPROVISION_DONE`. (Leave OS/system services alone — we only need a clean app surface, not a bare OS.)

- [ ] **Step 4: Verify no app workloads remain**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 'crontab -l 2>/dev/null | grep -v "^#" | grep . ; docker ps -q 2>/dev/null; systemctl list-units --type=service --state=running --no-pager | grep -iE "polyweather|trade" || echo CLEAN'
```
Expected: `CLEAN` and no docker container IDs / cron lines.

- [ ] **Step 5: Commit the snapshot artifact**

```bash
cd /Users/ariapramesi/repos/beads
git add deploy/pm1-beads/preexisting-inventory-2026-05-17.txt
git commit -m "ops(pm1): capture pre-wipe inventory snapshot"
```

---

## Task 2: Install Dolt + bd on pm1, init repo, create non-root SQL user

**Files:**
- Create: `deploy/pm1-beads/beads-client.env` (generated; chmod 600; gitignored)

- [ ] **Step 1: Verify Dolt/bd are NOT yet usable on pm1 (the failing check)**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 'command -v dolt && dolt version || echo NO_DOLT'
```
Expected: `NO_DOLT` (or a too-old version). Establishes the gap.

- [ ] **Step 2: Install Dolt and bd on pm1**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=30 admin@100.85.126.95 '
  set -e;
  curl -fsSL https://github.com/dolthub/dolt/releases/latest/download/install.sh | sudo bash;
  curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash;
  export PATH="$HOME/.local/bin:$PATH";
  dolt version; bd version
'
```
Expected: `dolt` `>= 1.88.1` and `bd` `>= 0.59.0` printed.

- [ ] **Step 3: Initialize a Dolt repo for the server to serve (no manual CREATE DATABASE)**

`bd init --server` (Task 5) provisions the beads project database itself. Here we
only need a valid Dolt repo in the data dir so `dolt sql-server` has something to serve.

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=20 admin@100.85.126.95 '
  set -e;
  mkdir -p /home/admin/beads-data;
  cd /home/admin/beads-data;
  ( dolt status >/dev/null 2>&1 && echo "already a dolt repo" ) || dolt init --name "beads-pm1" --email "beads@pm1.local";
  echo DOLT_REPO_READY
'
```
Expected: ends `DOLT_REPO_READY`, no error. (No `CREATE DATABASE` — that was a bug; beads names/creates its own DB on `bd init --server`.)

- [ ] **Step 4: Generate the password + write the client env file (env-var auth path)**

Run (generates the secret locally; `BEADS_DOLT_PASSWORD` is the documented highest-priority auth path — no `password:` config key exists):
```bash
ART=/Users/ariapramesi/repos/beads/deploy/pm1-beads
DOLT_PW="$(openssl rand -hex 24)"
cat > "$ART/beads-client.env" <<EOF
# source this before running bd against pm1. gitignored (Task 8).
export BEADS_DOLT_SERVER_MODE=1
export BEADS_DOLT_SERVER_HOST=100.85.126.95
export BEADS_DOLT_SERVER_PORT=3307
export BEADS_DOLT_SERVER_USER=beads
export BEADS_DOLT_PASSWORD='${DOLT_PW}'
EOF
chmod 600 "$ART/beads-client.env"
grep -c BEADS_DOLT_PASSWORD "$ART/beads-client.env"
```
Expected: prints `1`; file mode `600`.

- [ ] **Step 5: Create the non-root SQL user with broad privileges (beads creates its own DB)**

Run (privileges on `*.*` because the project DB name is created by `bd init --server`, not pre-known):
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
DOLT_PW="$(grep BEADS_DOLT_PASSWORD /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env | sed "s/.*='//;s/'$//")"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=20 admin@100.85.126.95 "
  cd /home/admin/beads-data &&
  dolt sql -q \"CREATE USER IF NOT EXISTS 'beads'@'%' IDENTIFIED BY '${DOLT_PW}'; GRANT ALL PRIVILEGES ON *.* TO 'beads'@'%' WITH GRANT OPTION; FLUSH PRIVILEGES;\" &&
  dolt sql -q \"SELECT user, host FROM mysql.user;\" &&
  echo DOLT_USER_CREATED
"
```
Expected: ends `DOLT_USER_CREATED`; the `mysql.user` listing shows the `beads` user. **Note any `root`@`%` or empty-password root row — that is closed and verified in Task 3.**

---

## Task 3: Run Dolt SQL server as systemd, bound to the Tailscale IP

**Files:**
- Create: `deploy/pm1-beads/dolt-sql-server.service`

- [ ] **Step 1: Confirm the server is not yet listening (failing check)**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 'ss -tlnp 2>/dev/null | grep 3307 || echo NOT_LISTENING'
```
Expected: `NOT_LISTENING`.

- [ ] **Step 2: Verify `dolt sql-server` flag support on pm1 (don't assume `--data-dir`)**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 'dolt sql-server --help 2>&1 | grep -E "^\s*--(host|port|data-dir)" || true'
```
Expected: confirms `--host` and `--port` exist. The beads docs run `dolt sql-server` from *inside* the repo dir with no `--data-dir`; the unit below uses `WorkingDirectory` + no `--data-dir` to match documented behavior. Only add `--data-dir=/home/admin/beads-data` if `--help` lists it AND running without it fails.

- [ ] **Step 3: Write the systemd unit locally**

Create `deploy/pm1-beads/dolt-sql-server.service`:
```ini
[Unit]
Description=Dolt SQL Server for Beads (tailnet only)
After=network-online.target tailscaled.service
Wants=network-online.target

[Service]
User=admin
WorkingDirectory=/home/admin/beads-data
ExecStart=/usr/local/bin/dolt sql-server --host=100.85.126.95 --port=3307
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

- [ ] **Step 4: Install, enable, and start the unit on pm1**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
scp -i "$PM1_KEY" -o StrictHostKeyChecking=no /Users/ariapramesi/repos/beads/deploy/pm1-beads/dolt-sql-server.service admin@100.85.126.95:/tmp/dolt-sql-server.service
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=20 admin@100.85.126.95 '
  set -e;
  DOLT_BIN="$(command -v dolt)";
  sudo sed -i "s#/usr/local/bin/dolt#${DOLT_BIN}#" /tmp/dolt-sql-server.service;
  sudo mv /tmp/dolt-sql-server.service /etc/systemd/system/dolt-sql-server.service;
  sudo systemctl daemon-reload;
  sudo systemctl enable --now dolt-sql-server.service;
  sleep 4;
  systemctl is-active dolt-sql-server.service
'
```
Expected: prints `active`.

- [ ] **Step 5: Verify it listens on the Tailscale IP (not 0.0.0.0)**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 'ss -tlnp 2>/dev/null | grep 3307'
```
Expected: a line showing `100.85.126.95:3307` (NOT `0.0.0.0:3307` and NOT `*:3307`). If it shows `0.0.0.0`, stop — the `--host` bind failed; fix before Task 4.

- [ ] **Step 6: SECURITY GATE — close & verify passwordless/remote root (HARD STOP if it fails)**

A `dolt sql-server` on a tailnet IP must not accept unauthenticated root. Lock it, then prove it.

Run (lock root, restrict to localhost, drop wildcard root):
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ROOT_PW="$(openssl rand -hex 24)"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=20 admin@100.85.126.95 "
  cd /home/admin/beads-data &&
  dolt sql -q \"ALTER USER IF EXISTS 'root'@'localhost' IDENTIFIED BY '${ROOT_PW}'; DROP USER IF EXISTS 'root'@'%'; FLUSH PRIVILEGES;\" 2>/dev/null;
  dolt sql -q \"SELECT user, host, IF(authentication_string='','EMPTY','SET') AS pw FROM mysql.user;\"
"
```
Expected: no user has `host = %` with `pw = EMPTY` except the intended `beads` user (which has a password set → `SET`). Record `ROOT_PW` into `beads-client.env` is **not** needed (root is not used by clients); note it in the runbook only if you want admin access.

Verify from the Mac that bad/no credentials are rejected:
```bash
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
BEADS_DOLT_SERVER_USER=root BEADS_DOLT_PASSWORD="" bd version >/dev/null 2>&1 && bd list 2>&1 | head -1
```
Expected: the root/no-password attempt **fails to read data** (auth error / connection refused), NOT a ticket list. If passwordless root can read/write over the tailnet: **HARD STOP — do not proceed to Task 4. Escalate to the user.**

- [ ] **Step 7: Commit the unit file**

```bash
cd /Users/ariapramesi/repos/beads
git add deploy/pm1-beads/dolt-sql-server.service
git commit -m "ops(pm1): systemd unit for tailnet-bound Dolt SQL server"
```

---

## Task 4: Firewall — Dolt port reachable only on the tailnet

**Files:** none (remote config)

- [ ] **Step 1: Show current exposure (failing check)**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=12 admin@100.85.126.95 'sudo ufw status verbose 2>/dev/null || echo UFW_INACTIVE'
```
Expected: `UFW_INACTIVE` or a status with no rule restricting 3307.

- [ ] **Step 2: Restrict 3307 to the Tailscale interface only**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=20 admin@100.85.126.95 '
  set -e;
  sudo ufw allow OpenSSH 2>/dev/null || sudo ufw allow 22/tcp;
  sudo ufw allow in on tailscale0 to any port 3307 proto tcp;
  sudo ufw deny 3307/tcp;
  yes | sudo ufw enable;
  sudo ufw status verbose
'
```
Expected: ufw active; `3307` allowed on `tailscale0`, denied generally; SSH allowed.

- [ ] **Step 3: Prove the port is closed off-tailnet but open on-tailnet**

Run (from Mac, which is on the tailnet):
```bash
nc -z -w5 100.85.126.95 3307 && echo "ON_TAILNET_OPEN (expected)" || echo "UNREACHABLE (unexpected from tailnet)"
```
Expected: `ON_TAILNET_OPEN (expected)`. (Off-tailnet closure is enforced by binding to the tailscale IP + ufw; no public interface serves 3307.)

---

## Task 5: Connect the Mac as a server-mode beads client

**Files:**
- Create: `deploy/pm1-beads/client-config.yaml`

- [ ] **Step 1: Confirm no server-mode workspace exists yet (failing check)**

Run:
```bash
mkdir -p ~/beads-workspace && cd ~/beads-workspace && (bd status 2>&1 | head -3 || echo NO_WORKSPACE)
```
Expected: `NO_WORKSPACE` or "not initialized".

- [ ] **Step 2: Write the client server-mode config (NO password/database keys — invalid in beads)**

Create `deploy/pm1-beads/client-config.yaml` exactly as below. Password is supplied
via the sourced `beads-client.env` (`BEADS_DOLT_PASSWORD`), not config:
```yaml
dolt:
  mode: server
  host: 100.85.126.95
  port: 3307
  user: beads
```

- [ ] **Step 3: Initialize the workspace in server mode (env-var auth) and point it at pm1**

Run:
```bash
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
cd ~/beads-workspace
mkdir -p .beads
cp /Users/ariapramesi/repos/beads/deploy/pm1-beads/client-config.yaml .beads/config.yaml
bd init --server
```
Expected: beads reports server-mode init against `100.85.126.95:3307`, no connection/auth error. (If auth fails: confirm `echo $BEADS_DOLT_PASSWORD` is non-empty and matches the pm1 `beads` user from Task 2 Step 5.)

- [ ] **Step 4: Create a smoke-test ticket and read it back**

Run:
```bash
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
cd ~/beads-workspace
bd create "pm1 central ticketing online" -p 1 -t task --json | tee /tmp/bd_create.json
bd list --json | grep "pm1 central ticketing online"
```
Expected: create returns JSON with a new issue ID; `bd list` shows the ticket.

- [ ] **Step 5: Commit the client config template**

```bash
cd /Users/ariapramesi/repos/beads
git add deploy/pm1-beads/client-config.yaml
git commit -m "ops(pm1): beads server-mode client config template"
```

---

## Task 6: Multi-client + restart-persistence verification

**Files:** none (verification only)

- [ ] **Step 1: Simulate a second client (agent) reading the shared state**

Run:
```bash
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
mkdir -p /tmp/agent-bd && cd /tmp/agent-bd && mkdir -p .beads
cp /Users/ariapramesi/repos/beads/deploy/pm1-beads/client-config.yaml .beads/config.yaml
bd init --server
bd list --json | grep "pm1 central ticketing online"
```
Expected: the ticket created in Task 5 is visible here — proves one shared source of truth across clients.

- [ ] **Step 2: Restart the server and confirm persistence**

Run:
```bash
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no -o ConnectTimeout=15 admin@100.85.126.95 'sudo systemctl restart dolt-sql-server.service; sleep 4; systemctl is-active dolt-sql-server.service'
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
cd ~/beads-workspace && bd list --json | grep "pm1 central ticketing online"
```
Expected: service `active` after restart; ticket still present (data persisted to Dolt).

---

## Task 7: Off-box backup + restore round-trip

**Files:**
- Create: `deploy/pm1-beads/backup.sh`

- [ ] **Step 1: Write the Mac-side backup script (off-box DR — pulls Dolt history to the Mac)**

Create `deploy/pm1-beads/backup.sh`:
```bash
#!/usr/bin/env bash
# Off-box DR for the pm1 beads workspace. Run from a server-mode client.
set -euo pipefail
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
DEST="${1:-$HOME/beads-backups}"
mkdir -p "$DEST"
cd "$HOME/beads-workspace"
bd backup init "$DEST" 2>/dev/null || true
bd backup sync
echo "backup synced to $DEST at $(date -u)"
```

- [ ] **Step 2: Make it executable and run a backup**

Run:
```bash
chmod +x /Users/ariapramesi/repos/beads/deploy/pm1-beads/backup.sh
/Users/ariapramesi/repos/beads/deploy/pm1-beads/backup.sh "$HOME/beads-backups"
ls -la "$HOME/beads-backups"
```
Expected: "backup synced" message; backup directory is non-empty.

- [ ] **Step 3: Prove restore round-trips**

Run:
```bash
mkdir -p /tmp/restore-test && cd /tmp/restore-test && bd init
bd backup restore --force "$HOME/beads-backups"
bd list --json | grep "pm1 central ticketing online" && echo RESTORE_OK
```
(Restore target uses default embedded mode — no env needed; it reads the backup, not pm1.)
Expected: ends `RESTORE_OK` — the smoke-test ticket exists in the restored copy.

- [ ] **Step 4: Schedule the backup (Mac cron, daily)**

Run:
```bash
( crontab -l 2>/dev/null; echo "30 9 * * * /Users/ariapramesi/repos/beads/deploy/pm1-beads/backup.sh >> $HOME/beads-backups/backup.log 2>&1" ) | crontab -
crontab -l | grep beads-backups
```
Expected: cron line present.

- [ ] **Step 5: Commit the backup script**

```bash
cd /Users/ariapramesi/repos/beads
git add deploy/pm1-beads/backup.sh
git commit -m "ops(pm1): off-box backup script + restore-tested DR"
```

---

## Task 8: Runbook + secret hygiene

**Files:**
- Create: `deploy/pm1-beads/RUNBOOK.md`
- Modify: `.gitignore`

- [ ] **Step 1: Gitignore the secret env file**

Append to `/Users/ariapramesi/repos/beads/.gitignore`:
```
deploy/pm1-beads/beads-client.env
```
Run:
```bash
cd /Users/ariapramesi/repos/beads && git check-ignore deploy/pm1-beads/beads-client.env && echo IGNORED
```
Expected: `IGNORED`. (Verify the secret was never committed: `git log --all --oneline -- deploy/pm1-beads/beads-client.env` returns nothing.)

- [ ] **Step 2: Write the runbook**

Create `deploy/pm1-beads/RUNBOOK.md`:
```markdown
# pm1 Beads Ticketing — Runbook

- Host: 100.85.126.95 (Tailscale), user admin, key in cross-market-state-fusion repo.
- Server: systemd `dolt-sql-server.service`, data `/home/admin/beads-data`, port 3307 (tailnet only).
- Off-limits: 34.241.191.3 (live polyweather) — never touch.

## Common ops
- Status:  ssh ... 'systemctl status dolt-sql-server.service'
- Restart: ssh ... 'sudo systemctl restart dolt-sql-server.service'
- Logs:    ssh ... 'journalctl -u dolt-sql-server.service -n 100 --no-pager'
- Client setup: `source deploy/pm1-beads/beads-client.env` then copy client-config.yaml to <workspace>/.beads/config.yaml. Password is the `BEADS_DOLT_PASSWORD` env var (no password key in config).
- Backup:  deploy/pm1-beads/backup.sh (cron: daily 09:30 local).
- Restore: bd init && bd backup restore --force ~/beads-backups

## If pm1 is lost
Reprovision from this plan (Tasks 2-4), then `bd backup restore --force` from ~/beads-backups.

## Rotate Dolt password
On pm1 (in /home/admin/beads-data): dolt sql -q "ALTER USER 'beads'@'%' IDENTIFIED BY '<new>';"
then update BEADS_DOLT_PASSWORD in deploy/pm1-beads/beads-client.env on every client.
```

- [ ] **Step 3: Commit runbook + gitignore**

```bash
cd /Users/ariapramesi/repos/beads
git add deploy/pm1-beads/RUNBOOK.md .gitignore
git commit -m "ops(pm1): runbook + gitignore Dolt credentials"
```

- [ ] **Step 4: Final end-to-end verification**

Run:
```bash
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
cd ~/beads-workspace
bd create "verify: full pipeline" -t task --json >/dev/null
bd list --json | grep "verify: full pipeline" && echo PIPELINE_OK
PM1_KEY="/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa"
ssh -i "$PM1_KEY" -o StrictHostKeyChecking=no admin@100.85.126.95 'systemctl is-active dolt-sql-server.service'
```
Expected: `PIPELINE_OK` and `active`.

---

## Self-Review

**Spec coverage:** Phase 0 nuke → Task 1. pm1 provisioning → Task 2. Dolt server (systemd, tailnet bind, non-root user, **root-auth lockdown gate**) → Tasks 2–3. Network/security (ufw, tailnet-only) → Task 4. bd CLI install → Task 2 (pm1) + Task 0 (Mac). Client config + bootstrap → Task 5. Backup/DR → Task 7. Verification (create/read/restart/off-tailnet/auth-rejected/backup round-trip) → Tasks 3, 5–8. Wrong-host guard → Task 0 Step 1 + operating constraints. Out-of-scope (web UI, reverie) honored — no tasks added. No gaps.

**Auth-model correction (post-review):** Removed invalid `password:`/`database:` config keys and the bogus `CREATE DATABASE beads`; auth now uses the documented `BEADS_DOLT_PASSWORD` env path via a gitignored `beads-client.env`; added a hard security gate (Task 3 Step 6) that closes and *verifies* passwordless/remote root before the port is opened in Task 4.

**Placeholder scan:** No fill-in placeholders remain. Secrets are generated by command (`openssl rand`) into `beads-client.env`; no `PASTE_…` tokens.

**Type/name consistency:** Host `100.85.126.95`, user `admin`, key path, port `3307`, data dir `/home/admin/beads-data`, service `dolt-sql-server.service`, Dolt user `beads`, secret file `deploy/pm1-beads/beads-client.env`, workspace `~/beads-workspace`, backup dir `~/beads-backups`, artifact dir `deploy/pm1-beads/` — used identically across all tasks.
