package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Session struct {
	IDHash        string
	UserID        string
	Username      string
	Role          string
	MustChange    bool
	CSRFHash      string
	CreatedAt     time.Time
	ExpiresAt     time.Time
	IdleExpiresAt time.Time
	LastSeenAt    time.Time
}

func (s *Store) CreateSession(ctx context.Context, idHash, userID, csrfHash string, expiresAt, idleExpiresAt time.Time, ip, userAgent string) error {
	now := time.Now().UTC()
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO sessions(id_hash, user_id, csrf_hash, created_at, expires_at, idle_expires_at, last_seen_at, ip, user_agent)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		idHash, userID, csrfHash, now.Unix(), expiresAt.UTC().Unix(), idleExpiresAt.UTC().Unix(), now.Unix(), ip, userAgent)
	return err
}

func (s *Store) SessionByIDHash(ctx context.Context, idHash string, now time.Time) (Session, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT s.id_hash, s.user_id, u.username, u.role, u.must_change_password, s.csrf_hash, s.created_at, s.expires_at, s.idle_expires_at, s.last_seen_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = ? AND s.expires_at > ? AND s.idle_expires_at > ? AND u.enabled = 1`,
		idHash, now.UTC().Unix(), now.UTC().Unix())
	var session Session
	var mustChange int
	var createdAt, expiresAt, idleExpiresAt, lastSeenAt int64
	err := row.Scan(&session.IDHash, &session.UserID, &session.Username, &session.Role, &mustChange, &session.CSRFHash,
		&createdAt, &expiresAt, &idleExpiresAt, &lastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	session.CreatedAt = time.Unix(createdAt, 0).UTC()
	session.MustChange = mustChange == 1
	session.ExpiresAt = time.Unix(expiresAt, 0).UTC()
	session.IdleExpiresAt = time.Unix(idleExpiresAt, 0).UTC()
	session.LastSeenAt = time.Unix(lastSeenAt, 0).UTC()
	return session, nil
}

func (s *Store) TouchSession(ctx context.Context, idHash string, now, idleExpiresAt time.Time) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE sessions SET last_seen_at = ?, idle_expires_at = ? WHERE id_hash = ?`,
		now.UTC().Unix(), idleExpiresAt.UTC().Unix(), idHash)
	return err
}

func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID, exceptIDHash string) error {
	if exceptIDHash == "" {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND id_hash <> ?`, userID, exceptIDHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR idle_expires_at <= ?`, now.UTC().Unix(), now.UTC().Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
