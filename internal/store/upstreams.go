package store

import (
	"context"
	"database/sql"
	"errors"
)

type Upstream struct {
	ID                 string
	Name               string
	Endpoint           string
	RouteHost          string
	RoutePrefix        string
	RepositoryPrefix   string
	AuthType           string
	Credentials        []byte
	CredentialsVersion int64
	CredentialsKeyVer  int64
	CAPEM              string
	InsecureSkipVerify bool
	TagTTLSeconds      int64
	RequestTimeoutSecs int64
	MaxBlobBytes       *int64
	Enabled            bool
	HealthState        string
	HealthMessage      string
	HealthCheckedAt    *int64
}

func (s *Store) UpsertUpstream(ctx context.Context, upstream Upstream) error {
	return upsertUpstream(ctx, s.DB, upstream)
}

type upstreamExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func upsertUpstream(ctx context.Context, executor upstreamExecer, upstream Upstream) error {
	now := unixNow()
	enabled := 0
	if upstream.Enabled {
		enabled = 1
	}
	var routeHost, routePrefix any
	if upstream.RouteHost != "" {
		routeHost = upstream.RouteHost
	}
	if upstream.RoutePrefix != "" {
		routePrefix = upstream.RoutePrefix
	}
	_, err := executor.ExecContext(ctx, `
		INSERT INTO upstreams(
			id, name, endpoint, route_host, route_prefix, repository_prefix, auth_type,
			ca_pem, insecure_skip_verify, tag_ttl_seconds, request_timeout_seconds,
			max_blob_bytes, enabled, health_state, created_at, updated_at
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'unknown', ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name = excluded.name,
			endpoint = excluded.endpoint,
			route_host = excluded.route_host,
			route_prefix = excluded.route_prefix,
			repository_prefix = excluded.repository_prefix,
			auth_type = excluded.auth_type,
			ca_pem = excluded.ca_pem,
			insecure_skip_verify = excluded.insecure_skip_verify,
			tag_ttl_seconds = excluded.tag_ttl_seconds,
			request_timeout_seconds = excluded.request_timeout_seconds,
			max_blob_bytes = excluded.max_blob_bytes,
			enabled = excluded.enabled,
			updated_at = excluded.updated_at`,
		upstream.ID, upstream.Name, upstream.Endpoint, routeHost, routePrefix,
		upstream.RepositoryPrefix, upstream.AuthType, upstream.CAPEM,
		boolInt(upstream.InsecureSkipVerify), upstream.TagTTLSeconds,
		upstream.RequestTimeoutSecs, upstream.MaxBlobBytes, enabled, now, now)
	return err
}

// UpsertUpstreamWithCredentials atomically writes the upstream definition and,
// when replaceCredentials is true, its encrypted credential payload. The
// expectedVersion guard prevents concurrent updates from replacing credentials
// that were sealed for a different credential version.
func (s *Store) UpsertUpstreamWithCredentials(ctx context.Context, upstream Upstream, ciphertext []byte, keyVersion int64, replaceCredentials bool, expectedVersion int64) (int64, error) {
	if replaceCredentials && keyVersion <= 0 {
		return 0, errors.New("credential key version must be positive")
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var currentVersion int64
	exists := true
	if err := tx.QueryRowContext(ctx, `SELECT credentials_version FROM upstreams WHERE id = ?`, upstream.ID).Scan(&currentVersion); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		exists = false
		currentVersion = 0
	}
	if replaceCredentials {
		if exists && currentVersion != expectedVersion {
			return 0, ErrConflict
		}
		if !exists && expectedVersion != 0 {
			return 0, ErrConflict
		}
	}
	if err := upsertUpstream(ctx, tx, upstream); err != nil {
		return 0, err
	}
	if !replaceCredentials {
		if !exists {
			currentVersion = 1
		}
		if err := tx.Commit(); err != nil {
			return 0, err
		}
		return currentVersion, nil
	}
	nextVersion := currentVersion + 1
	if !exists {
		nextVersion = 1
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE upstreams SET credentials_ciphertext = ?, credentials_version = ?,
		       credentials_key_version = ?, updated_at = ?
		WHERE id = ?`, ciphertext, nextVersion, keyVersion, unixNow(), upstream.ID)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected != 1 {
		return 0, ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return nextVersion, nil
}

func (s *Store) UpstreamByID(ctx context.Context, upstreamID string) (Upstream, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, name, endpoint, COALESCE(route_host, ''), COALESCE(route_prefix, ''),
		       repository_prefix, auth_type, credentials_ciphertext, credentials_version,
		       credentials_key_version, COALESCE(ca_pem, ''), insecure_skip_verify,
		       tag_ttl_seconds, request_timeout_seconds, max_blob_bytes, enabled,
		       health_state, COALESCE(health_message, ''), health_checked_at
		FROM upstreams WHERE id = ?`, upstreamID)
	upstream, err := scanUpstream(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Upstream{}, ErrNotFound
	}
	return upstream, err
}

type upstreamScanner interface {
	Scan(dest ...any) error
}

func scanUpstream(row upstreamScanner) (Upstream, error) {
	var upstream Upstream
	var insecure, enabled int
	var maxBlob, checkedAt sql.NullInt64
	var credentials []byte
	err := row.Scan(&upstream.ID, &upstream.Name, &upstream.Endpoint, &upstream.RouteHost,
		&upstream.RoutePrefix, &upstream.RepositoryPrefix, &upstream.AuthType, &credentials,
		&upstream.CredentialsVersion, &upstream.CredentialsKeyVer, &upstream.CAPEM, &insecure,
		&upstream.TagTTLSeconds, &upstream.RequestTimeoutSecs, &maxBlob, &enabled,
		&upstream.HealthState, &upstream.HealthMessage, &checkedAt)
	if err != nil {
		return Upstream{}, err
	}
	upstream.Credentials = append([]byte(nil), credentials...)
	upstream.InsecureSkipVerify = insecure == 1
	upstream.Enabled = enabled == 1
	if maxBlob.Valid {
		value := maxBlob.Int64
		upstream.MaxBlobBytes = &value
	}
	if checkedAt.Valid {
		value := checkedAt.Int64
		upstream.HealthCheckedAt = &value
	}
	return upstream, nil
}

func (s *Store) ListUpstreams(ctx context.Context) ([]Upstream, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, name, endpoint, COALESCE(route_host, ''), COALESCE(route_prefix, ''),
		       repository_prefix, auth_type, credentials_ciphertext, credentials_version,
		       credentials_key_version, COALESCE(ca_pem, ''), insecure_skip_verify,
		       tag_ttl_seconds, request_timeout_seconds, max_blob_bytes, enabled,
		       health_state, COALESCE(health_message, ''), health_checked_at
		FROM upstreams ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	upstreams := make([]Upstream, 0)
	for rows.Next() {
		upstream, err := scanUpstream(rows)
		if err != nil {
			return nil, err
		}
		upstreams = append(upstreams, upstream)
	}
	return upstreams, rows.Err()
}

func (s *Store) SetUpstreamCredentials(ctx context.Context, upstreamID string, ciphertext []byte, keyVersion int64) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE upstreams SET credentials_ciphertext = ?, credentials_version = credentials_version + 1,
		       credentials_key_version = ?, updated_at = ?
		WHERE id = ?`, ciphertext, keyVersion, unixNow(), upstreamID)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if affected == 0 {
		return 0, ErrNotFound
	}
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT credentials_version FROM upstreams WHERE id = ?`, upstreamID).Scan(&version); err != nil {
		return 0, err
	}
	return version, tx.Commit()
}

func (s *Store) UpdateUpstreamHealth(ctx context.Context, upstreamID, state, message string) error {
	result, err := s.DB.ExecContext(ctx, `
		UPDATE upstreams SET health_state = ?, health_message = ?, health_checked_at = ?, updated_at = ? WHERE id = ?`,
		state, message, unixNow(), unixNow(), upstreamID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteUpstream(ctx context.Context, upstreamID string) error {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM upstreams WHERE id = ?`, upstreamID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) EnsureCacheRepository(ctx context.Context, upstreamID, name string) (Repository, error) {
	now := unixNow()
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO repositories(namespace, name, kind, upstream_id, created_at, last_seen_at)
		VALUES(?, ?, 'cache', ?, ?, ?)
		ON CONFLICT(namespace, name) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		upstreamID, name, upstreamID, now, now)
	if err != nil {
		return Repository{}, err
	}
	return s.RepositoryByName(ctx, upstreamID, name)
}

func (s *Store) CacheTagFresh(ctx context.Context, repositoryID int64, tag string, ttlSeconds int64) (bool, error) {
	if ttlSeconds <= 0 {
		return false, nil
	}
	var fresh int
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM tags
			WHERE repo_id = ? AND tag = ? AND checked_at >= ?
		)`, repositoryID, tag, unixNow()-ttlSeconds).Scan(&fresh)
	return fresh == 1, err
}

func (s *Store) CacheTagFreshForAccept(ctx context.Context, repositoryID int64, tag, acceptHash string, ttlSeconds int64) (bool, error) {
	if ttlSeconds <= 0 {
		return false, nil
	}
	if acceptHash == "" {
		acceptHash = "*"
	}
	var fresh int
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM tags
			WHERE repo_id = ? AND tag = ? AND accept_hash = ? AND checked_at >= ?
		)`, repositoryID, tag, acceptHash, unixNow()-ttlSeconds).Scan(&fresh)
	return fresh == 1, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
