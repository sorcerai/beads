# Connecting a beads client to pm1 (server mode)

bd 1.0.4 resolves server connection from **env vars + init flags**, not a `dolt:`
YAML block. Use the gitignored `beads-client.env` (holds the password).

## One-time per workspace

```bash
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
mkdir -p ~/beads-workspace && cd ~/beads-workspace
bd init --server --server-host 100.85.126.95 --server-port 3307 --server-user beads
```

`--server-user beads` is **required** — without it `bd init` defaults to `root`
for the database-create path, and remote root is intentionally disabled on pm1
(security gate). Password comes from `BEADS_DOLT_PASSWORD` (set by the env file).

## Every session / before any bd command

```bash
source /Users/ariapramesi/repos/beads/deploy/pm1-beads/beads-client.env
cd ~/beads-workspace
bd list        # etc.
```

`beads-client.env` exports `BEADS_DOLT_SERVER_MODE=1`,
`BEADS_DOLT_SERVER_HOST/PORT/USER`, and `BEADS_DOLT_PASSWORD`. For normal ops
`bd` reads `BEADS_DOLT_SERVER_USER` from the env first, so sourcing the file is
sufficient after the initial `bd init`.

Database on the server: `beads_workspace`. Issue prefix: `beads-workspace-<hash>`.
