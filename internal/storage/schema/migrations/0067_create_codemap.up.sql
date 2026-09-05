-- code_nodes / code_edges / issue_files: the code map (spec docs/superpowers/specs/2026-09-05-codemap-design.md §5).
-- code_nodes and code_edges are per-REPOSITORY, keyed by repo_id (beads.ComputeRepoIDForPath),
-- not per-issue: a scout run replaces them wholesale. issue_files is per-issue and cascades
-- with the issue exactly like provenance_events (0063).
-- No FK from code_edges to code_nodes on purpose: a package rescan replaces its edges in one
-- statement and the indexer prunes orphans; see migrations/README.md on Dolt FK behaviour.
CREATE TABLE IF NOT EXISTS code_nodes (
    id CHAR(64) NOT NULL PRIMARY KEY,
    repo_id VARCHAR(64) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    path TEXT NOT NULL,
    path_hash CHAR(64) NOT NULL,
    name VARCHAR(255) NOT NULL,
    package_id CHAR(64),
    lang VARCHAR(16) NOT NULL,
    blob_hash CHAR(40),
    loc INT,
    is_test TINYINT(1) NOT NULL DEFAULT 0,
    exported INT,
    layer VARCHAR(64),
    summary TEXT,
    tags TEXT,
    summary_blob_hash CHAR(40),
    summary_model VARCHAR(64),
    summarized_at DATETIME,
    indexed_at DATETIME NOT NULL,
    UNIQUE INDEX uq_code_nodes_repo_kind_path (repo_id, kind, path_hash),
    INDEX idx_code_nodes_repo_kind (repo_id, kind),
    INDEX idx_code_nodes_repo_package (repo_id, package_id)
);

CREATE TABLE IF NOT EXISTS code_edges (
    repo_id VARCHAR(64) NOT NULL,
    src_id CHAR(64) NOT NULL,
    dst_id CHAR(64) NOT NULL,
    kind VARCHAR(16) NOT NULL,
    weight INT NOT NULL DEFAULT 1,
    PRIMARY KEY (repo_id, src_id, dst_id, kind),
    INDEX idx_code_edges_repo_dst (repo_id, dst_id, kind)
);

CREATE TABLE IF NOT EXISTS issue_files (
    issue_id VARCHAR(255) NOT NULL,
    repo_id VARCHAR(64) NOT NULL,
    path_hash CHAR(64) NOT NULL,
    path TEXT NOT NULL,
    source VARCHAR(16) NOT NULL,
    commit_sha CHAR(40),
    first_seen DATETIME NOT NULL,
    last_seen DATETIME NOT NULL,
    touches INT NOT NULL DEFAULT 1,
    PRIMARY KEY (issue_id, repo_id, path_hash),
    INDEX idx_issue_files_repo_path (repo_id, path_hash),
    CONSTRAINT fk_issue_files_issue FOREIGN KEY (issue_id) REFERENCES issues(id) ON DELETE CASCADE ON UPDATE CASCADE
);
