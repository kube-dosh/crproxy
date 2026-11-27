package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Upload struct {
	ID        string
	RepoID    int64
	UserID    string
	Size      int64
	State     string
	ExpiresAt time.Time
}

func (s *Store) CreateUpload(ctx context.Context, uploadID string, repositoryID int64, userID string, expiresAt time.Time) error {
	now := unixNow()
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO uploads(id, repo_id, user_id, size, state, created_at, updated_at, expires_at)
		VALUES(?, ?, NULLIF(?, ''), 0, 'created', ?, ?, ?)`,
		uploadID, repositoryID, userID, now, now, expiresAt.UTC().Unix())
	return err
}

func (s *Store) UploadByID(ctx context.Context, uploadID string) (Upload, error) {
	var upload Upload
	var userID sql.NullString
	var expiresAt int64
	err := s.DB.QueryRowContext(ctx, `
		SELECT id, repo_id, user_id, size, state, expires_at FROM uploads WHERE id = ?`, uploadID).
		Scan(&upload.ID, &upload.RepoID, &userID, &upload.Size, &upload.State, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Upload{}, ErrNotFound
	}
	if err != nil {
		return Upload{}, err
	}
	upload.UserID = userID.String
	upload.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	return upload, nil
}

func (s *Store) UpdateUploadSize(ctx context.Context, uploadID string, size int64) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE uploads SET size = ?, state = 'uploading', updated_at = ? WHERE id = ?`,
		size, unixNow(), uploadID)
	return err
}

func (s *Store) DeleteUpload(ctx context.Context, uploadID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM uploads WHERE id = ?`, uploadID)
	return err
}

func (s *Store) ExpireUploads(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM uploads WHERE expires_at < ?`, before.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var uploadID string
		if err := rows.Scan(&uploadID); err != nil {
			return nil, err
		}
		ids = append(ids, uploadID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) > 0 {
		if _, err := s.DB.ExecContext(ctx, `DELETE FROM uploads WHERE expires_at < ?`, before.UTC().Unix()); err != nil {
			return nil, err
		}
	}
	return ids, nil
}
