package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const schemaVersion = 3

const schemaV1 = `
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  username TEXT NOT NULL UNIQUE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
  enabled INTEGER NOT NULL DEFAULT 1,
  must_change_password INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  password_changed_at INTEGER,
  last_login_at INTEGER
);

CREATE TABLE IF NOT EXISTS api_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  secret_hash TEXT NOT NULL,
  scopes TEXT NOT NULL,
  expires_at INTEGER,
  last_used_at INTEGER,
  last_used_ip TEXT,
  revoked_at INTEGER,
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS upstreams (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL UNIQUE,
  endpoint TEXT NOT NULL,
  route_host TEXT UNIQUE,
  route_prefix TEXT UNIQUE,
  repository_prefix TEXT NOT NULL DEFAULT '',
  auth_type TEXT NOT NULL,
  credentials_ciphertext BLOB,
  credentials_version INTEGER NOT NULL DEFAULT 1,
  credentials_key_version INTEGER NOT NULL DEFAULT 1,
  ca_pem TEXT,
  insecure_skip_verify INTEGER NOT NULL DEFAULT 0,
  tag_ttl_seconds INTEGER NOT NULL DEFAULT 60,
  request_timeout_seconds INTEGER NOT NULL DEFAULT 30,
  max_blob_bytes INTEGER,
  enabled INTEGER NOT NULL DEFAULT 1,
  health_state TEXT NOT NULL DEFAULT 'unknown',
  health_checked_at INTEGER,
  health_message TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS repositories (
  id INTEGER PRIMARY KEY,
  namespace TEXT NOT NULL CHECK (length(namespace) > 0),
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('local', 'cache')),
  upstream_id TEXT REFERENCES upstreams(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  last_seen_at INTEGER,
  UNIQUE(namespace, name),
  CHECK (
    (kind = 'local' AND namespace = 'local' AND upstream_id IS NULL)
    OR
    (kind = 'cache' AND upstream_id IS NOT NULL AND namespace = upstream_id)
  )
);

CREATE TABLE IF NOT EXISTS contents (
  digest TEXT PRIMARY KEY,
  algorithm TEXT NOT NULL,
  size INTEGER NOT NULL,
  media_type TEXT,
  kind TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'active',
  created_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  access_count INTEGER NOT NULL DEFAULT 0,
  last_verified_at INTEGER
);

CREATE TABLE IF NOT EXISTS content_refs (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  digest TEXT NOT NULL REFERENCES contents(digest) ON DELETE CASCADE,
  kind TEXT NOT NULL CHECK (kind IN ('blob', 'manifest')),
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  PRIMARY KEY(repo_id, digest, kind)
);

CREATE TABLE IF NOT EXISTS tags (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  tag TEXT NOT NULL,
  accept_hash TEXT NOT NULL,
  digest TEXT NOT NULL REFERENCES contents(digest) ON DELETE CASCADE,
  media_type TEXT,
  size INTEGER NOT NULL,
  etag TEXT,
  checked_at INTEGER NOT NULL,
  last_used_at INTEGER NOT NULL,
  PRIMARY KEY(repo_id, tag, accept_hash)
);

CREATE TABLE IF NOT EXISTS content_edges (
  parent_digest TEXT NOT NULL,
  child_digest TEXT NOT NULL,
  child_kind TEXT NOT NULL,
  PRIMARY KEY(parent_digest, child_digest)
);

CREATE TABLE IF NOT EXISTS referrers (
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  subject_digest TEXT NOT NULL,
  manifest_digest TEXT NOT NULL REFERENCES contents(digest) ON DELETE CASCADE,
  media_type TEXT NOT NULL,
  size INTEGER NOT NULL,
  artifact_type TEXT,
  annotations_json TEXT,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(repo_id, subject_digest, manifest_digest)
);

CREATE TABLE IF NOT EXISTS uploads (
  id TEXT PRIMARY KEY,
  repo_id INTEGER NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  size INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  idle_expires_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  ip TEXT,
  user_agent TEXT
);

CREATE TABLE IF NOT EXISTS audit_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  actor_type TEXT NOT NULL,
  actor_id TEXT,
  action TEXT NOT NULL,
  target_type TEXT,
  target_id TEXT,
  ip TEXT,
  result TEXT NOT NULL,
  message TEXT,
  request_id TEXT
);

CREATE TABLE IF NOT EXISTS jobs (
  id TEXT PRIMARY KEY,
  type TEXT NOT NULL,
  state TEXT NOT NULL,
  progress_json TEXT,
  started_at INTEGER,
  finished_at INTEGER,
  error TEXT
);

CREATE TABLE IF NOT EXISTS settings (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_contents_lru ON contents(state, last_used_at);
CREATE INDEX IF NOT EXISTS idx_contents_size ON contents(size);
CREATE INDEX IF NOT EXISTS idx_refs_repo ON content_refs(repo_id, digest);
CREATE INDEX IF NOT EXISTS idx_tags_digest ON tags(digest);
CREATE INDEX IF NOT EXISTS idx_repositories_kind ON repositories(kind, last_seen_at);
CREATE INDEX IF NOT EXISTS idx_repositories_upstream ON repositories(upstream_id, last_seen_at);
CREATE INDEX IF NOT EXISTS idx_referrers_subject ON referrers(repo_id, subject_digest);
CREATE INDEX IF NOT EXISTS idx_uploads_expiry ON uploads(expires_at);
`

const schemaV2 = `
ALTER TABLE audit_events ADD COLUMN user_agent TEXT;
CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id, expires_at);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at, idle_expires_at);
CREATE INDEX IF NOT EXISTS idx_audit_at ON audit_events(at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_events(actor_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_tokens_user ON api_tokens(user_id, revoked_at, created_at DESC);
CREATE UNIQUE INDEX IF NOT EXISTS idx_tokens_active_name ON api_tokens(user_id, name) WHERE revoked_at IS NULL;
`

const schemaV3 = `
ALTER TABLE jobs ADD COLUMN result_json TEXT;
ALTER TABLE jobs ADD COLUMN requested_by TEXT;

CREATE TABLE IF NOT EXISTS registry_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  repository_id INTEGER REFERENCES repositories(id) ON DELETE SET NULL,
  upstream_id TEXT REFERENCES upstreams(id) ON DELETE SET NULL,
  digest TEXT,
  size INTEGER NOT NULL DEFAULT 0,
  cache_hit INTEGER,
  result TEXT NOT NULL,
  message TEXT
);

CREATE INDEX IF NOT EXISTS idx_registry_events_at ON registry_events(at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_registry_events_type_at ON registry_events(event_type, at DESC);
CREATE INDEX IF NOT EXISTS idx_registry_events_upstream ON registry_events(upstream_id, at DESC);
CREATE INDEX IF NOT EXISTS idx_contents_unreferenced ON contents(digest);
`

func Open(ctx context.Context, path string) (*sql.DB, error) {
	if path == "" {
		return nil, fmt.Errorf("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=journal_mode(DELETE)&_pragma=synchronous(FULL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := verifyPragmas(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func verifyPragmas(ctx context.Context, db *sql.DB) error {
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return err
	}
	if !strings.EqualFold(journalMode, "delete") {
		return fmt.Errorf("unexpected sqlite journal mode %q", journalMode)
	}
	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	if foreignKeys != 1 {
		return fmt.Errorf("sqlite foreign_keys is not enabled")
	}
	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return err
	}
	if synchronous != 2 {
		return fmt.Errorf("sqlite synchronous is not FULL")
	}
	return nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_meta (id INTEGER PRIMARY KEY CHECK (id = 1), version INTEGER NOT NULL)`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO schema_meta(id, version) VALUES(1, 0)`); err != nil {
		return err
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT version FROM schema_meta WHERE id = 1`).Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("database schema version %d is newer than supported version %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	migrations := []string{schemaV1, schemaV2, schemaV3}
	for next := version + 1; next <= schemaVersion; next++ {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[next-1]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply schema migration %d: %w", next, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_meta SET version = ? WHERE id = 1`, next); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
