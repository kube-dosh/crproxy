package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type RepositorySummary struct {
	ID            int64      `json:"id"`
	Namespace     string     `json:"namespace"`
	Name          string     `json:"name"`
	Kind          string     `json:"kind"`
	UpstreamID    *string    `json:"upstreamId,omitempty"`
	TagCount      int        `json:"tagCount"`
	ManifestCount int        `json:"manifestCount"`
	BlobCount     int        `json:"blobCount"`
	Size          int64      `json:"size"`
	CreatedAt     time.Time  `json:"createdAt"`
	LastSeenAt    *time.Time `json:"lastSeenAt,omitempty"`
}

type RepositoryFilter struct {
	Kind       string
	UpstreamID string
	Query      string
	Limit      int
	Offset     int
}

func (s *Store) ListRepositories(ctx context.Context, filter RepositoryFilter) ([]RepositorySummary, int, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	switch filter.Kind {
	case "":
	case "local", "cache":
		where = append(where, "r.kind = ?")
		args = append(args, filter.Kind)
	default:
		return nil, 0, fmt.Errorf("invalid repository kind %q", filter.Kind)
	}
	if filter.UpstreamID != "" {
		where = append(where, "r.upstream_id = ?")
		args = append(args, filter.UpstreamID)
	}
	if filter.Query != "" {
		where = append(where, "(r.namespace LIKE ? ESCAPE '\\' OR r.name LIKE ? ESCAPE '\\')")
		pattern := "%" + escapeLike(filter.Query) + "%"
		args = append(args, pattern, pattern)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories r WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	query := `
		SELECT r.id, r.namespace, r.name, r.kind, r.upstream_id,
		       (SELECT COUNT(DISTINCT t.tag) FROM tags t WHERE t.repo_id = r.id),
		       (SELECT COUNT(*) FROM content_refs cr WHERE cr.repo_id = r.id AND cr.kind = 'manifest'),
		       (SELECT COUNT(*) FROM content_refs cr WHERE cr.repo_id = r.id AND cr.kind = 'blob'),
		       (SELECT COALESCE(SUM(c.size), 0) FROM contents c WHERE EXISTS(
		            SELECT 1 FROM content_refs cr2 WHERE cr2.digest = c.digest AND cr2.repo_id = r.id)),
		       r.created_at, r.last_seen_at
		FROM repositories r WHERE ` + clause + ` ORDER BY r.last_seen_at DESC, r.name LIMIT ? OFFSET ?`
	args = append(args, filter.Limit, filter.Offset)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]RepositorySummary, 0, filter.Limit)
	for rows.Next() {
		var item RepositorySummary
		var upstream sql.NullString
		var created int64
		var lastSeen sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Namespace, &item.Name, &item.Kind, &upstream,
			&item.TagCount, &item.ManifestCount, &item.BlobCount, &item.Size, &created, &lastSeen); err != nil {
			return nil, 0, err
		}
		if upstream.Valid {
			value := upstream.String
			item.UpstreamID = &value
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		if lastSeen.Valid {
			value := time.Unix(lastSeen.Int64, 0).UTC()
			item.LastSeenAt = &value
		}
		result = append(result, item)
	}
	return result, total, rows.Err()
}

func (s *Store) RepositorySummaryByID(ctx context.Context, repositoryID int64) (RepositorySummary, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT r.id, r.namespace, r.name, r.kind, r.upstream_id,
		       (SELECT COUNT(DISTINCT t.tag) FROM tags t WHERE t.repo_id = r.id),
		       (SELECT COUNT(*) FROM content_refs cr WHERE cr.repo_id = r.id AND cr.kind = 'manifest'),
		       (SELECT COUNT(*) FROM content_refs cr WHERE cr.repo_id = r.id AND cr.kind = 'blob'),
		       (SELECT COALESCE(SUM(c.size), 0) FROM contents c WHERE EXISTS(
		            SELECT 1 FROM content_refs cr2 WHERE cr2.digest = c.digest AND cr2.repo_id = r.id)),
		       r.created_at, r.last_seen_at
		FROM repositories r WHERE r.id = ?`, repositoryID)
	var item RepositorySummary
	var upstream sql.NullString
	var created int64
	var lastSeen sql.NullInt64
	err := row.Scan(&item.ID, &item.Namespace, &item.Name, &item.Kind, &upstream,
		&item.TagCount, &item.ManifestCount, &item.BlobCount, &item.Size, &created, &lastSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return RepositorySummary{}, ErrNotFound
	}
	if err != nil {
		return RepositorySummary{}, err
	}
	if upstream.Valid {
		value := upstream.String
		item.UpstreamID = &value
	}
	item.CreatedAt = time.Unix(created, 0).UTC()
	if lastSeen.Valid {
		value := time.Unix(lastSeen.Int64, 0).UTC()
		item.LastSeenAt = &value
	}
	return item, nil
}

type TagSummary struct {
	Tag        string    `json:"tag"`
	Digest     string    `json:"digest"`
	MediaType  string    `json:"mediaType"`
	Size       int64     `json:"size"`
	ETag       string    `json:"etag,omitempty"`
	CheckedAt  time.Time `json:"checkedAt"`
	LastUsedAt time.Time `json:"lastUsedAt"`
	Stale      bool      `json:"stale"`
}

func (s *Store) RepositoryTags(ctx context.Context, repositoryID int64, staleBefore time.Time) ([]TagSummary, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT tag, digest, COALESCE(media_type, ''), size, COALESCE(etag, ''), checked_at, last_used_at
		FROM tags WHERE repo_id = ? ORDER BY tag`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TagSummary, 0)
	for rows.Next() {
		var item TagSummary
		var checked, used int64
		if err := rows.Scan(&item.Tag, &item.Digest, &item.MediaType, &item.Size, &item.ETag, &checked, &used); err != nil {
			return nil, err
		}
		item.CheckedAt = time.Unix(checked, 0).UTC()
		item.LastUsedAt = time.Unix(used, 0).UTC()
		item.Stale = !staleBefore.IsZero() && item.CheckedAt.Before(staleBefore.UTC())
		result = append(result, item)
	}
	return result, rows.Err()
}

type ContentEntry struct {
	Digest         string     `json:"digest"`
	Algorithm      string     `json:"algorithm"`
	Size           int64      `json:"size"`
	MediaType      string     `json:"mediaType,omitempty"`
	Kind           string     `json:"kind"`
	State          string     `json:"state"`
	CreatedAt      time.Time  `json:"createdAt"`
	LastUsedAt     time.Time  `json:"lastUsedAt"`
	AccessCount    int64      `json:"accessCount"`
	LastVerifiedAt *time.Time `json:"lastVerifiedAt,omitempty"`
	RefCount       int        `json:"refCount"`
	LocalRefs      int        `json:"localRefs"`
	CacheRefs      int        `json:"cacheRefs"`
}

type ContentFilter struct {
	RepositoryID int64
	UpstreamID   string
	Kind         string
	Query        string
	Sort         string
	Limit        int
	Offset       int
}

func (s *Store) ListContents(ctx context.Context, filter ContentFilter) ([]ContentEntry, int, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	if filter.Offset < 0 {
		filter.Offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	if filter.RepositoryID > 0 {
		where = append(where, "EXISTS(SELECT 1 FROM content_refs cr WHERE cr.digest = c.digest AND cr.repo_id = ?)")
		args = append(args, filter.RepositoryID)
	}
	if filter.UpstreamID != "" {
		where = append(where, "EXISTS(SELECT 1 FROM content_refs cr JOIN repositories r ON r.id = cr.repo_id WHERE cr.digest = c.digest AND r.upstream_id = ?)")
		args = append(args, filter.UpstreamID)
	}
	if filter.Kind != "" {
		where = append(where, "c.kind = ?")
		args = append(args, filter.Kind)
	}
	if filter.Query != "" {
		where = append(where, "(c.digest LIKE ? ESCAPE '\\' OR c.media_type LIKE ? ESCAPE '\\')")
		pattern := "%" + escapeLike(filter.Query) + "%"
		args = append(args, pattern, pattern)
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM contents c WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	order := "c.last_used_at DESC"
	switch filter.Sort {
	case "size", "size_desc":
		order = "c.size DESC"
	case "size_asc":
		order = "c.size ASC"
	case "last_used", "last_used_asc":
		order = "c.last_used_at ASC"
	case "", "last_used_desc":
	default:
		return nil, 0, fmt.Errorf("invalid content sort %q", filter.Sort)
	}
	query := `
		SELECT c.digest, c.algorithm, c.size, COALESCE(c.media_type, ''), c.kind, c.state,
		       c.created_at, c.last_used_at, c.access_count, c.last_verified_at,
		       (SELECT COUNT(*) FROM content_refs cr WHERE cr.digest = c.digest),
		       (SELECT COUNT(*) FROM content_refs cr JOIN repositories r ON r.id = cr.repo_id WHERE cr.digest = c.digest AND r.kind = 'local'),
		       (SELECT COUNT(*) FROM content_refs cr JOIN repositories r ON r.id = cr.repo_id WHERE cr.digest = c.digest AND r.kind = 'cache')
		FROM contents c WHERE ` + clause + ` ORDER BY ` + order + ` LIMIT ? OFFSET ?`
	args = append(args, filter.Limit, filter.Offset)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	result := make([]ContentEntry, 0, filter.Limit)
	for rows.Next() {
		var item ContentEntry
		var created, used int64
		var verified sql.NullInt64
		if err := rows.Scan(&item.Digest, &item.Algorithm, &item.Size, &item.MediaType, &item.Kind, &item.State,
			&created, &used, &item.AccessCount, &verified, &item.RefCount, &item.LocalRefs, &item.CacheRefs); err != nil {
			return nil, 0, err
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		item.LastUsedAt = time.Unix(used, 0).UTC()
		if verified.Valid {
			value := time.Unix(verified.Int64, 0).UTC()
			item.LastVerifiedAt = &value
		}
		result = append(result, item)
	}
	return result, total, rows.Err()
}

func (s *Store) AllContents(ctx context.Context) ([]ContentEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT digest, algorithm, size, COALESCE(media_type, ''), kind, state,
		       created_at, last_used_at, access_count, last_verified_at
		FROM contents ORDER BY digest`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ContentEntry, 0)
	for rows.Next() {
		var item ContentEntry
		var created, used int64
		var verified sql.NullInt64
		if err := rows.Scan(&item.Digest, &item.Algorithm, &item.Size, &item.MediaType, &item.Kind, &item.State,
			&created, &used, &item.AccessCount, &verified); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		item.LastUsedAt = time.Unix(used, 0).UTC()
		if verified.Valid {
			value := time.Unix(verified.Int64, 0).UTC()
			item.LastVerifiedAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) MarkContentVerified(ctx context.Context, digest string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE contents SET last_verified_at = ? WHERE digest = ?`, unixNow(), digest)
	return err
}

func (s *Store) ContentsWithLocalRefs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT DISTINCT cr.digest FROM content_refs cr JOIN repositories r ON r.id = cr.repo_id WHERE r.kind='local'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, err
		}
		result[digest] = true
	}
	return result, rows.Err()
}

func (s *Store) ContentDigestSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT digest FROM contents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, err
		}
		result[digest] = true
	}
	return result, rows.Err()
}

type StorageUsage struct {
	PhysicalBytes int64 `json:"physicalBytes"`
	LocalBytes    int64 `json:"localBytes"`
	CacheBytes    int64 `json:"cacheOnlyBytes"`
	SharedBytes   int64 `json:"sharedBytes"`
	ContentCount  int64 `json:"contentCount"`
	LocalCount    int64 `json:"localContentCount"`
	CacheCount    int64 `json:"cacheContentCount"`
}

func (s *Store) StorageUsage(ctx context.Context) (StorageUsage, error) {
	var usage StorageUsage
	err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*), COALESCE(SUM(size), 0),
		       COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM content_refs cr JOIN repositories r ON r.id=cr.repo_id WHERE cr.digest=c.digest AND r.kind='local') THEN size ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM content_refs cr JOIN repositories r ON r.id=cr.repo_id WHERE cr.digest=c.digest AND r.kind='cache') THEN size ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM content_refs cr JOIN repositories r ON r.id=cr.repo_id WHERE cr.digest=c.digest AND r.kind='local')
		                         AND EXISTS(SELECT 1 FROM content_refs cr JOIN repositories r ON r.id=cr.repo_id WHERE cr.digest=c.digest AND r.kind='cache') THEN size ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM content_refs cr JOIN repositories r ON r.id=cr.repo_id WHERE cr.digest=c.digest AND r.kind='local') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN EXISTS(SELECT 1 FROM content_refs cr JOIN repositories r ON r.id=cr.repo_id WHERE cr.digest=c.digest AND r.kind='cache') THEN 1 ELSE 0 END), 0)
		FROM contents c`).Scan(&usage.ContentCount, &usage.PhysicalBytes, &usage.LocalBytes, &usage.CacheBytes,
		&usage.SharedBytes, &usage.LocalCount, &usage.CacheCount)
	if err != nil {
		return StorageUsage{}, err
	}
	usage.CacheBytes -= usage.SharedBytes
	if usage.CacheBytes < 0 {
		usage.CacheBytes = 0
	}
	return usage, nil
}

type Breakdown struct {
	Key             string `json:"key"`
	Name            string `json:"name"`
	Kind            string `json:"kind,omitempty"`
	Bytes           int64  `json:"bytes"`
	RepositoryCount int    `json:"repositoryCount"`
	ContentCount    int    `json:"contentCount"`
}

func (s *Store) BreakdownByUpstream(ctx context.Context) ([]Breakdown, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT u.id, u.name, COALESCE(SUM(c.size), 0), COUNT(DISTINCT r.id), COUNT(DISTINCT c.digest)
		FROM upstreams u
		LEFT JOIN repositories r ON r.upstream_id = u.id AND r.kind = 'cache'
		LEFT JOIN content_refs cr ON cr.repo_id = r.id
		LEFT JOIN contents c ON c.digest = cr.digest AND NOT EXISTS(
			SELECT 1 FROM content_refs crl JOIN repositories rl ON rl.id=crl.repo_id
			WHERE crl.digest=c.digest AND rl.kind='local')
		GROUP BY u.id, u.name ORDER BY u.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Breakdown, 0)
	for rows.Next() {
		var item Breakdown
		if err := rows.Scan(&item.Key, &item.Name, &item.Bytes, &item.RepositoryCount, &item.ContentCount); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) BreakdownByRepository(ctx context.Context, kind string) ([]Breakdown, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT CAST(r.id AS TEXT), COALESCE(r.upstream_id || '/', '') || r.name, r.kind,
		       COALESCE(SUM(c.size), 0), 1, COUNT(DISTINCT c.digest)
		FROM repositories r
		LEFT JOIN content_refs cr ON cr.repo_id = r.id
		LEFT JOIN contents c ON c.digest = cr.digest
		WHERE (? = '' OR r.kind = ?)
		GROUP BY r.id ORDER BY COALESCE(SUM(c.size), 0) DESC`, kind, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Breakdown, 0)
	for rows.Next() {
		var item Breakdown
		if err := rows.Scan(&item.Key, &item.Name, &item.Kind, &item.Bytes, &item.RepositoryCount, &item.ContentCount); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type GCContent struct {
	Digest   string
	Size     int64
	DigestID int64
}

func (s *Store) OrphanContents(ctx context.Context, limit int) ([]ContentEntry, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT c.digest, c.algorithm, c.size, COALESCE(c.media_type, ''), c.kind, c.state,
		       c.created_at, c.last_used_at, c.access_count, c.last_verified_at
		FROM contents c WHERE c.state = 'active' AND NOT EXISTS(
			SELECT 1 FROM content_refs cr WHERE cr.digest = c.digest
		) ORDER BY c.last_used_at LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ContentEntry, 0)
	for rows.Next() {
		var item ContentEntry
		var created, used int64
		var verified sql.NullInt64
		if err := rows.Scan(&item.Digest, &item.Algorithm, &item.Size, &item.MediaType, &item.Kind, &item.State,
			&created, &used, &item.AccessCount, &verified); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		item.LastUsedAt = time.Unix(used, 0).UTC()
		if verified.Valid {
			value := time.Unix(verified.Int64, 0).UTC()
			item.LastVerifiedAt = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type CacheEvictionCandidate struct {
	RepositoryID int64
	Name         string
	UpstreamID   string
	Bytes        int64
	LastUsedAt   time.Time
}

func (s *Store) CacheEvictionCandidates(ctx context.Context, limit int) ([]CacheEvictionCandidate, error) {
	if limit <= 0 || limit > 10000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT r.id, r.name, COALESCE(r.upstream_id, ''),
		       COALESCE((SELECT SUM(c2.size) FROM contents c2 WHERE c2.state='active' AND EXISTS(
		           SELECT 1 FROM content_refs cr2 WHERE cr2.digest=c2.digest AND cr2.repo_id=r.id)
		           AND NOT EXISTS(SELECT 1 FROM content_refs crl JOIN repositories rl ON rl.id=crl.repo_id
		                          WHERE crl.digest=c2.digest AND rl.kind='local')), 0),
		       COALESCE(MAX(c.last_used_at), r.last_seen_at, r.created_at)
		FROM repositories r
		LEFT JOIN content_refs cr ON cr.repo_id = r.id
		LEFT JOIN contents c ON c.digest = cr.digest
		WHERE r.kind = 'cache'
		GROUP BY r.id ORDER BY COALESCE(MAX(c.last_used_at), r.last_seen_at, r.created_at) ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]CacheEvictionCandidate, 0)
	for rows.Next() {
		var item CacheEvictionCandidate
		var used int64
		if err := rows.Scan(&item.RepositoryID, &item.Name, &item.UpstreamID, &item.Bytes, &used); err != nil {
			return nil, err
		}
		item.LastUsedAt = time.Unix(used, 0).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) DeleteRepositoryReferences(ctx context.Context, repositoryID int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM repositories WHERE id = ?)`, repositoryID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	for _, statement := range []string{
		`DELETE FROM tags WHERE repo_id = ?`,
		`DELETE FROM referrers WHERE repo_id = ?`,
		`DELETE FROM content_refs WHERE repo_id = ?`,
		`DELETE FROM uploads WHERE repo_id = ?`,
		`DELETE FROM repositories WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, statement, repositoryID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MarkContentDeleted(ctx context.Context, digest string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `UPDATE contents SET state = 'deleting' WHERE digest = ? AND state = 'active' AND NOT EXISTS(
		SELECT 1 FROM content_refs cr WHERE cr.digest = contents.digest)`, digest)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func (s *Store) FinishContentDelete(ctx context.Context, digest string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_edges WHERE parent_digest = ?`, digest); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM contents WHERE digest = ? AND state = 'deleting'`, digest); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ResetDeletingContents(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT digest FROM contents WHERE state = 'deleting'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	digests := make([]string, 0)
	for rows.Next() {
		var digest string
		if err := rows.Scan(&digest); err != nil {
			return nil, err
		}
		digests = append(digests, digest)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE contents SET state='active' WHERE state='deleting'`); err != nil {
		return nil, err
	}
	return digests, nil
}

func (s *Store) TouchContent(ctx context.Context, digest string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE contents SET last_used_at = ?, access_count = access_count + 1 WHERE digest = ?`, unixNow(), digest)
	return err
}

type UploadSummary struct {
	ID         string    `json:"id"`
	RepoID     int64     `json:"repositoryId"`
	Repository string    `json:"repository"`
	UserID     string    `json:"userId"`
	Username   string    `json:"username"`
	Size       int64     `json:"size"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func (s *Store) ListUploads(ctx context.Context, activeOnly bool, limit int) ([]UploadSummary, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT u.id, u.repo_id, COALESCE(r.upstream_id || '/', '') || r.name,
	                 COALESCE(u.user_id, ''), COALESCE(us.username, ''), u.size, u.state, u.created_at, u.updated_at, u.expires_at
	          FROM uploads u LEFT JOIN repositories r ON r.id=u.repo_id LEFT JOIN users us ON us.id=u.user_id`
	args := []any{}
	if activeOnly {
		query += ` WHERE u.expires_at >= ?`
		args = append(args, unixNow())
	}
	query += ` ORDER BY u.updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]UploadSummary, 0)
	for rows.Next() {
		var item UploadSummary
		var created, updated, expires int64
		if err := rows.Scan(&item.ID, &item.RepoID, &item.Repository, &item.UserID, &item.Username, &item.Size,
			&item.State, &created, &updated, &expires); err != nil {
			return nil, err
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		item.UpdatedAt = time.Unix(updated, 0).UTC()
		item.ExpiresAt = time.Unix(expires, 0).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) UploadOwner(ctx context.Context, uploadID string) (string, error) {
	var owner sql.NullString
	err := s.DB.QueryRowContext(ctx, `SELECT user_id FROM uploads WHERE id = ?`, uploadID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return owner.String, err
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}
