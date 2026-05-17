# Beads as Central Ticketing on pm1 (Server Mode over Tailscale) — Design

**Date:** 2026-05-17
**Status:** Approved (design); Phase 0 destructive decision authorized by user
**Approach:** A — Central `dolt sql-server` on pm1, clients in beads server mode

## Goal

Run beads as a Linear-style shared ticketing system: one always-on Dolt SQL
server on a dedicated box, reached over Tailscale, with the Mac Studio and its
agents connecting as multi-writer clients against a single source of truth.

"Like Linear" here means **role and workflow** (one shared, dependency-aware
issue board with real-time shared state), **not** a Linear.app-style web UI.
Beads ships no first-class web board; a browser UI is explicitly out of scope
(deferred — Approach C). It can be added later without reworking this design.

## Hosts (unambiguous)

| Role | Host | Access | User | Key | Disposition |
|---|---|---|---|---|---|
| **Beads target** | `100.85.126.95` (SSH `pm1-webdock`) | Tailscale | `admin` | `/Users/ariapramesi/repos/cross-market-state-fusion/pm1_webdock_id_rsa` | Old pre-AWS host. **Full reprovision authorized.** |
| **Off-limits** | `34.241.191.3` (SSH `pm1`) | public IP | `ubuntu` | `~/Downloads/pm1.pem` | **LIVE polyweather stack. Never touched by this work.** |

reverie / "reveried" is the Mac Studio's local memory/context system. It is
**not** on pm1 and is **not** a beads integration in this scope. pm1 is purely
the shared ticket backend that the Mac and agents sync to over the tailnet.

## Architecture

A single `dolt sql-server` process on `100.85.126.95` owns one Dolt database
(the shared workspace). Clients run `bd` in **server mode** and connect to it
over Tailscale. Writes auto-commit to Dolt history; all clients read the same
live state.

```
Mac Studio (bd, server mode) ─┐
agents (bd, server mode)      ─┼─ Tailscale ─→ 100.85.126.95:3307
                                              dolt sql-server
                                              (beads workspace DB)
                                                   │ scheduled
                                                   ▼
                                              offsite backup / Dolt mirror
```

## Components (each independently testable)

### Phase 0 — Reprovision gate (DECISION RESOLVED)
The user has authorized treating `100.85.126.95` as dead: **full reprovision,
no read-only SSH inventory required.** The destructive wipe still executes
during the implementation phase (not at design time), with the standard
execution-time confirmation. `34.241.191.3` is explicitly excluded from every
command.

### 1. pm1 provisioning
Base packages, a dedicated data directory (e.g. `/var/lib/beads` or
`/home/admin/beads-data`), and a service account context for the daemon.

### 2. Dolt server
- Install Dolt `>= 1.88.1`.
- Initialize the beads Dolt database in the data directory.
- Run `dolt sql-server` as a `systemd` unit with `Restart=always`.
- **Bind to the Tailscale IP only** (`100.85.126.95:3307`) — never `0.0.0.0`.
- Create a **non-root Dolt SQL user with a password**; disable anonymous/root-open access.

### 3. Network & security
- Confirm `tailscaled` is healthy on pm1 and the Mac is on the same tailnet.
- `ufw` (or equivalent) denies the Dolt port on all public interfaces.
- Tailscale ACLs scope which devices/agents can reach port 3307.
- CMSF SSH key used only for provisioning; Dolt credentials stored/rotated
  deliberately (not committed to git).

### 4. `bd` CLI install
- On pm1 for admin/debug.
- On the Mac Studio and each agent machine, pinned to `bd >= 0.59.0`.

### 5. Client configuration
- Server-mode config on Mac + agents: `dolt.mode=server`,
  `host=100.85.126.95`, `port=3307`, the non-root user.
- Bootstrap the initial workspace (`bd init --server`) and seed issues.

### 6. Backup / DR
- Scheduled `bd backup` to an offsite destination (filesystem/S3/DoltHub) or a
  Dolt remote mirror, since pm1 is treated as disposable.
- A documented, **tested** restore path.

## Data flow

`bd create/update` on any client → SQL over Tailscale → `dolt sql-server` on
pm1 writes and auto-commits to Dolt history → all clients read identical state.
A scheduled job pushes Dolt history offsite.

## Error handling & risks

| Risk | Mitigation |
|---|---|
| Daemon crash | `systemd Restart=always` + healthcheck |
| Tailscale/network down | Clients cannot reach tickets — accepted tradeoff of server mode; documented, **no silent local fallback** |
| pm1 lost entirely | Restore from offsite backup; optional Dolt remote mirror for fast recovery |
| Concurrent writes | Native to Dolt sql-server + beads server mode |
| Security exposure | Tailnet-only bind + ufw + Dolt auth + credential rotation |
| Version skew | Pin `dolt >= 1.88.1` and `bd >= 0.59.0` on every machine |
| Wrong-host destruction | Every destructive command hard-scoped to `100.85.126.95`; `34.241.191.3` never referenced in provisioning |

## Verification (post-deploy)

From the Mac over Tailscale:
1. Connect in server mode; create a ticket.
2. Read that ticket from a second client (agent context).
3. Restart the `systemd` unit; confirm the ticket persists.
4. Confirm port `3307` is **unreachable from off the tailnet**.
5. Prove a backup → restore round-trip recovers issue data and history.

## Out of scope (YAGNI)

- Linear.app-style self-hosted web board (Approach C) — deferred.
- Any reverie ↔ beads integration code.
- Any change to `34.241.191.3` / the live polyweather stack.
