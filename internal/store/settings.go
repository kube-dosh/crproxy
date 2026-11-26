package store

import (
	"context"
	"database/sql"
	"errors"
)

func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var value string
	err := s.DB.QueryRowContext(ctx, `SELECT value_json FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return value, err
}

func (s *Store) SetSetting(ctx context.Context, key, valueJSON string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO settings(key, value_json, updated_at) VALUES(?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET value_json=excluded.value_json, updated_at=excluded.updated_at`,
		key, valueJSON, unixNow())
	return err
}

func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	return err
}
