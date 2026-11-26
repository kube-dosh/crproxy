package store

import (
	"context"
	"database/sql"
	"errors"
)

type Repository struct {
	ID         int64
	Namespace  string
	Name       string
	Kind       string
	UpstreamID *string
}

func (s *Store) EnsureLocalRepository(ctx context.Context, name string) (Repository, error) {
	now := unixNow()
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO repositories(namespace, name, kind, created_at, last_seen_at)
		VALUES('local', ?, 'local', ?, ?)
		ON CONFLICT(namespace, name) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		name, now, now)
	if err != nil {
		return Repository{}, err
	}
	return s.RepositoryByName(ctx, "local", name)
}

func (s *Store) RepositoryByName(ctx context.Context, namespace, name string) (Repository, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, namespace, name, kind, upstream_id
		FROM repositories WHERE namespace = ? AND name = ?`, namespace, name)
	return scanRepository(row)
}

func (s *Store) RepositoryByID(ctx context.Context, repositoryID int64) (Repository, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT id, namespace, name, kind, upstream_id
		FROM repositories WHERE id = ?`, repositoryID)
	return scanRepository(row)
}

func scanRepository(row *sql.Row) (Repository, error) {
	var repo Repository
	var upstream sql.NullString
	err := row.Scan(&repo.ID, &repo.Namespace, &repo.Name, &repo.Kind, &upstream)
	if errors.Is(err, sql.ErrNoRows) {
		return Repository{}, ErrNotFound
	}
	if err != nil {
		return Repository{}, err
	}
	if upstream.Valid {
		repo.UpstreamID = &upstream.String
	}
	return repo, nil
}

func (s *Store) ListLocalRepositoryNames(ctx context.Context, last string, limit int) ([]string, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT name FROM repositories
		WHERE kind = 'local' AND name > ?
		ORDER BY name LIMIT ?`, last, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		result = append(result, name)
	}
	return result, rows.Err()
}

func (s *Store) CountLocalRepositories(ctx context.Context) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM repositories WHERE kind = 'local'`).Scan(&count)
	return count, err
}
