package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"crproxy/internal/oci"
)

type Content struct {
	Digest    string
	Algorithm string
	Size      int64
	MediaType string
	Kind      string
}

func (s *Store) BlobRefExists(ctx context.Context, repositoryID int64, digest oci.Digest) (bool, error) {
	return s.contentRefExists(ctx, repositoryID, digest, "blob")
}

func (s *Store) ManifestRefExists(ctx context.Context, repositoryID int64, digest oci.Digest) (bool, error) {
	return s.contentRefExists(ctx, repositoryID, digest, "manifest")
}

func (s *Store) contentRefExists(ctx context.Context, repositoryID int64, digest oci.Digest, kind string) (bool, error) {
	var exists int
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM content_refs
			WHERE repo_id = ? AND digest = ? AND kind = ?
		)`, repositoryID, digest.String(), kind).Scan(&exists)
	return exists == 1, err
}

func (s *Store) BlobContent(ctx context.Context, repositoryID int64, digest oci.Digest) (Content, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT c.digest, c.algorithm, c.size, COALESCE(c.media_type, ''), c.kind
		FROM content_refs cr
		JOIN contents c ON c.digest = cr.digest
		WHERE cr.repo_id = ? AND cr.digest = ? AND cr.kind = 'blob'`,
		repositoryID, digest.String())
	return scanContent(row)
}

func (s *Store) PutBlobRef(ctx context.Context, repositoryID int64, digest oci.Digest, size int64, mediaType string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertContentTx(ctx, tx, digest, size, mediaType, "blob"); err != nil {
		return err
	}
	if err := upsertRefTx(ctx, tx, repositoryID, digest, "blob"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MountBlob(ctx context.Context, sourceRepositoryID, targetRepositoryID int64, digest oci.Digest) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var size int64
	var mediaType sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT c.size, c.media_type
		FROM content_refs cr JOIN contents c ON c.digest = cr.digest
		WHERE cr.repo_id = ? AND cr.digest = ? AND cr.kind = 'blob'`,
		sourceRepositoryID, digest.String()).Scan(&size, &mediaType)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if err := upsertRefTx(ctx, tx, targetRepositoryID, digest, "blob"); err != nil {
		return err
	}
	_ = size
	_ = mediaType
	return tx.Commit()
}

func (s *Store) CommitUploadedBlob(ctx context.Context, uploadID string, repositoryID int64, digest oci.Digest, size int64, mediaType string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertContentTx(ctx, tx, digest, size, mediaType, "blob"); err != nil {
		return err
	}
	if err := upsertRefTx(ctx, tx, repositoryID, digest, "blob"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, uploadID); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertContentTx(ctx context.Context, tx *sql.Tx, digest oci.Digest, size int64, mediaType, kind string) error {
	now := unixNow()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO contents(digest, algorithm, size, media_type, kind, state, created_at, last_used_at)
		VALUES(?, ?, ?, ?, ?, 'active', ?, ?)
		ON CONFLICT(digest) DO UPDATE SET
			size = excluded.size,
			media_type = COALESCE(NULLIF(excluded.media_type, ''), contents.media_type),
			kind = excluded.kind,
			state = 'active',
			last_used_at = excluded.last_used_at`,
		digest.String(), digest.Algorithm, size, mediaType, kind, now, now)
	return err
}

func upsertRefTx(ctx context.Context, tx *sql.Tx, repositoryID int64, digest oci.Digest, kind string) error {
	now := unixNow()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO content_refs(repo_id, digest, kind, first_seen_at, last_seen_at)
		VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(repo_id, digest, kind) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		repositoryID, digest.String(), kind, now, now)
	return err
}

func scanContent(row *sql.Row) (Content, error) {
	var content Content
	err := row.Scan(&content.Digest, &content.Algorithm, &content.Size, &content.MediaType, &content.Kind)
	if errors.Is(err, sql.ErrNoRows) {
		return Content{}, ErrNotFound
	}
	return content, err
}

type EdgeInput struct {
	Digest oci.Digest
	Kind   string
}

type ManifestInput struct {
	Digest       oci.Digest
	MediaType    string
	Size         int64
	Kind         string
	Tag          string
	AcceptHash   string
	ETag         string
	Subject      *oci.Descriptor
	ArtifactType string
	Annotations  map[string]string
	Edges        []EdgeInput
}

func (s *Store) PutManifest(ctx context.Context, repositoryID int64, input ManifestInput) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertContentTx(ctx, tx, input.Digest, input.Size, input.MediaType, input.Kind); err != nil {
		return err
	}
	if err := upsertRefTx(ctx, tx, repositoryID, input.Digest, "manifest"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_edges WHERE parent_digest = ?`, input.Digest.String()); err != nil {
		return err
	}
	for _, edge := range input.Edges {
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO content_edges(parent_digest, child_digest, child_kind)
			VALUES(?, ?, ?)`, input.Digest.String(), edge.Digest.String(), edge.Kind); err != nil {
			return err
		}
	}
	now := unixNow()
	if input.Tag != "" {
		acceptHash := input.AcceptHash
		if acceptHash == "" {
			acceptHash = "*"
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO tags(repo_id, tag, accept_hash, digest, media_type, size, etag, checked_at, last_used_at)
			VALUES(?, ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?)
			ON CONFLICT(repo_id, tag, accept_hash) DO UPDATE SET
				digest = excluded.digest,
				media_type = excluded.media_type,
				size = excluded.size,
				etag = excluded.etag,
				checked_at = excluded.checked_at,
				last_used_at = excluded.last_used_at`,
			repositoryID, input.Tag, acceptHash, input.Digest.String(), input.MediaType, input.Size, input.ETag, now, now)
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM referrers WHERE repo_id = ? AND manifest_digest = ?`, repositoryID, input.Digest.String()); err != nil {
		return err
	}
	if input.Subject != nil {
		annotations := ""
		if len(input.Annotations) > 0 {
			raw, _ := json.Marshal(input.Annotations)
			annotations = string(raw)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO referrers(repo_id, subject_digest, manifest_digest, media_type, size, artifact_type, annotations_json, created_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			repositoryID, input.Subject.Digest, input.Digest.String(), input.MediaType, input.Size, input.ArtifactType, annotations, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ManifestByReference(ctx context.Context, repositoryID int64, reference string) (Content, error) {
	if strings.Contains(reference, ":") {
		digest, err := oci.ParseDigest(reference)
		if err != nil {
			return Content{}, ErrNotFound
		}
		row := s.DB.QueryRowContext(ctx, `
			SELECT c.digest, c.algorithm, c.size, COALESCE(c.media_type, ''), c.kind
			FROM content_refs cr JOIN contents c ON c.digest = cr.digest
			WHERE cr.repo_id = ? AND cr.digest = ? AND cr.kind = 'manifest'`,
			repositoryID, digest.String())
		return scanContent(row)
	}
	row := s.DB.QueryRowContext(ctx, `
		SELECT c.digest, c.algorithm, c.size, COALESCE(c.media_type, ''), c.kind
		FROM tags t JOIN contents c ON c.digest = t.digest
		WHERE t.repo_id = ? AND t.tag = ?
		ORDER BY t.checked_at DESC LIMIT 1`, repositoryID, reference)
	return scanContent(row)
}

// ManifestByReferenceForAccept resolves a tag using the exact content
// negotiation variant that produced it. Digest references are independent of
// Accept and use the normal digest lookup.
func (s *Store) ManifestByReferenceForAccept(ctx context.Context, repositoryID int64, reference, acceptHash string) (Content, error) {
	if strings.Contains(reference, ":") {
		return s.ManifestByReference(ctx, repositoryID, reference)
	}
	if acceptHash == "" {
		acceptHash = "*"
	}
	row := s.DB.QueryRowContext(ctx, `
		SELECT c.digest, c.algorithm, c.size, COALESCE(c.media_type, ''), c.kind
		FROM tags t JOIN contents c ON c.digest = t.digest
		WHERE t.repo_id = ? AND t.tag = ? AND t.accept_hash = ?`, repositoryID, reference, acceptHash)
	return scanContent(row)
}

func (s *Store) CachedTagETag(ctx context.Context, repositoryID int64, tag string) (string, error) {
	var etag sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT etag FROM tags
		WHERE repo_id = ? AND tag = ?
		ORDER BY checked_at DESC LIMIT 1`, repositoryID, tag).Scan(&etag)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return etag.String, nil
}

func (s *Store) CachedTagETagForAccept(ctx context.Context, repositoryID int64, tag, acceptHash string) (string, error) {
	if acceptHash == "" {
		acceptHash = "*"
	}
	var etag sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT etag FROM tags WHERE repo_id = ? AND tag = ? AND accept_hash = ?`,
		repositoryID, tag, acceptHash).Scan(&etag)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return etag.String, nil
}

func (s *Store) TouchTag(ctx context.Context, repositoryID int64, tag string) error {
	now := unixNow()
	_, err := s.DB.ExecContext(ctx, `
		UPDATE tags SET checked_at = ?, last_used_at = ?
		WHERE repo_id = ? AND tag = ?`, now, now, repositoryID, tag)
	return err
}

func (s *Store) TouchTagForAccept(ctx context.Context, repositoryID int64, tag, acceptHash string) error {
	if acceptHash == "" {
		acceptHash = "*"
	}
	now := unixNow()
	_, err := s.DB.ExecContext(ctx, `
		UPDATE tags SET checked_at = ?, last_used_at = ?
		WHERE repo_id = ? AND tag = ? AND accept_hash = ?`, now, now, repositoryID, tag, acceptHash)
	return err
}

func (s *Store) ListTags(ctx context.Context, repositoryID int64, last string, limit int) ([]string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT DISTINCT tag FROM tags
		WHERE repo_id = ? AND tag > ?
		ORDER BY tag LIMIT ?`, repositoryID, last, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return nil, err
		}
		result = append(result, tag)
	}
	return result, rows.Err()
}

type Referrer struct {
	Digest       string
	MediaType    string
	Size         int64
	ArtifactType string
	Annotations  map[string]string
}

type RepositoryReferrer struct {
	SubjectDigest string            `json:"subjectDigest"`
	Digest        string            `json:"digest"`
	MediaType     string            `json:"mediaType"`
	Size          int64             `json:"size"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
}

func (s *Store) RepositoryReferrers(ctx context.Context, repositoryID int64) ([]RepositoryReferrer, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT subject_digest, manifest_digest, media_type, size, COALESCE(artifact_type, ''),
		       COALESCE(annotations_json, ''), created_at
		FROM referrers WHERE repo_id = ? ORDER BY created_at DESC, manifest_digest`, repositoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RepositoryReferrer, 0)
	for rows.Next() {
		var item RepositoryReferrer
		var annotations string
		var created int64
		if err := rows.Scan(&item.SubjectDigest, &item.Digest, &item.MediaType, &item.Size, &item.ArtifactType, &annotations, &created); err != nil {
			return nil, err
		}
		if annotations != "" {
			_ = json.Unmarshal([]byte(annotations), &item.Annotations)
		}
		item.CreatedAt = time.Unix(created, 0).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) ListReferrers(ctx context.Context, repositoryID int64, subjectDigest, artifactType, last string, limit int) ([]Referrer, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	query := `
		SELECT manifest_digest, media_type, size, COALESCE(artifact_type, ''), COALESCE(annotations_json, '')
		FROM referrers WHERE repo_id = ? AND subject_digest = ? AND manifest_digest > ?`
	args := []any{repositoryID, subjectDigest, last}
	if artifactType != "" {
		query += ` AND artifact_type = ?`
		args = append(args, artifactType)
	}
	query += ` ORDER BY manifest_digest LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Referrer, 0)
	for rows.Next() {
		var ref Referrer
		var annotations string
		if err := rows.Scan(&ref.Digest, &ref.MediaType, &ref.Size, &ref.ArtifactType, &annotations); err != nil {
			return nil, err
		}
		if annotations != "" {
			_ = json.Unmarshal([]byte(annotations), &ref.Annotations)
		}
		result = append(result, ref)
	}
	return result, rows.Err()
}

func (s *Store) DeleteTag(ctx context.Context, repositoryID int64, tag string) (bool, error) {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM tags WHERE repo_id = ? AND tag = ?`, repositoryID, tag)
	if err != nil {
		return false, err
	}
	rows, err := result.RowsAffected()
	return rows > 0, err
}

func (s *Store) DeleteManifest(ctx context.Context, repositoryID int64, digest oci.Digest) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM content_refs WHERE repo_id = ? AND digest = ? AND kind = 'manifest')`,
		repositoryID, digest.String()).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM tags WHERE repo_id = ? AND digest = ?`, repositoryID, digest.String()); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_refs WHERE repo_id = ? AND digest = ? AND kind = 'manifest'`, repositoryID, digest.String()); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM referrers WHERE repo_id = ? AND manifest_digest = ?`, repositoryID, digest.String()); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) DeleteBlob(ctx context.Context, repositoryID int64, digest oci.Digest) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM content_refs WHERE repo_id = ? AND digest = ? AND kind = 'blob')`,
		repositoryID, digest.String()).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return ErrNotFound
	}
	var referenced int
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM content_edges ce
			JOIN content_refs cr ON cr.digest = ce.parent_digest AND cr.repo_id = ? AND cr.kind = 'manifest'
			WHERE ce.child_digest = ?
		)`, repositoryID, digest.String()).Scan(&referenced); err != nil {
		return err
	}
	if referenced == 1 {
		return ErrBlobReferenced
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM content_refs WHERE repo_id = ? AND digest = ? AND kind = 'blob'`, repositoryID, digest.String()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RepositoryManifestCount(ctx context.Context, repositoryID int64) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM content_refs WHERE repo_id = ? AND kind = 'manifest'`, repositoryID).Scan(&count)
	return count, err
}

func (s *Store) RepositoryBlobCount(ctx context.Context, repositoryID int64) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM content_refs WHERE repo_id = ? AND kind = 'blob'`, repositoryID).Scan(&count)
	return count, err
}

func (s *Store) ContentSize(ctx context.Context, digest oci.Digest) (int64, error) {
	var size int64
	err := s.DB.QueryRowContext(ctx, `SELECT size FROM contents WHERE digest = ?`, digest.String()).Scan(&size)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return size, err
}

func (s *Store) MustParseDigest(value string) (oci.Digest, error) {
	d, err := oci.ParseDigest(value)
	if err != nil {
		return oci.Digest{}, fmt.Errorf("invalid digest: %w", err)
	}
	return d, nil
}
