package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/kube-dosh/crproxy/internal/id"
)

type User struct {
	ID                 string
	Username           string
	PasswordHash       string
	Role               string
	Enabled            bool
	MustChangePassword bool
	CreatedAt          int64
	LastLoginAt        *time.Time
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count)
	return count, err
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash, role string) (User, error) {
	return s.CreateUserWithPasswordChange(ctx, username, passwordHash, role, false)
}

func (s *Store) CreateUserWithPasswordChange(ctx context.Context, username, passwordHash, role string, mustChange bool) (User, error) {
	if role != "admin" && role != "operator" && role != "viewer" {
		return User{}, fmt.Errorf("invalid role %q", role)
	}
	now := unixNow()
	user := User{
		ID:                 id.New("usr_", 12),
		Username:           username,
		PasswordHash:       passwordHash,
		Role:               role,
		Enabled:            true,
		MustChangePassword: mustChange,
		CreatedAt:          now,
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO users(id, username, password_hash, role, enabled, must_change_password, created_at)
		VALUES(?, ?, ?, ?, 1, ?, ?)`,
		user.ID, user.Username, user.PasswordHash, user.Role, boolInt(mustChange), user.CreatedAt)
	if err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, enabled, must_change_password, created_at, last_login_at
		FROM users WHERE username = ?`, username))
}

func (s *Store) UserByID(ctx context.Context, userID string) (User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, `
		SELECT id, username, password_hash, role, enabled, must_change_password, created_at, last_login_at
		FROM users WHERE id = ?`, userID))
}

func scanUser(row *sql.Row) (User, error) {
	var user User
	var enabled, mustChange int
	var lastLogin sql.NullInt64
	err := row.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &enabled, &mustChange, &user.CreatedAt, &lastLogin)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	user.Enabled = enabled == 1
	user.MustChangePassword = mustChange == 1
	if lastLogin.Valid {
		value := time.Unix(lastLogin.Int64, 0).UTC()
		user.LastLoginAt = &value
	}
	return user, nil
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, username, password_hash, role, enabled, must_change_password, created_at, last_login_at
		FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := make([]User, 0)
	for rows.Next() {
		var user User
		var enabled, mustChange int
		var lastLogin sql.NullInt64
		if err := rows.Scan(&user.ID, &user.Username, &user.PasswordHash, &user.Role, &enabled, &mustChange, &user.CreatedAt, &lastLogin); err != nil {
			return nil, err
		}
		user.Enabled = enabled == 1
		user.MustChangePassword = mustChange == 1
		if lastLogin.Valid {
			value := time.Unix(lastLogin.Int64, 0).UTC()
			user.LastLoginAt = &value
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

func (s *Store) UpdateUser(ctx context.Context, userID, role string, enabled bool) error {
	if role != "admin" && role != "operator" && role != "viewer" {
		return fmt.Errorf("invalid role %q", role)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var currentRole string
	var currentEnabled int
	if err := tx.QueryRowContext(ctx, `SELECT role, enabled FROM users WHERE id = ?`, userID).Scan(&currentRole, &currentEnabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	losesActiveAdmin := currentRole == "admin" && currentEnabled == 1 && (role != "admin" || !enabled)
	if losesActiveAdmin {
		var otherAdmins int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND enabled = 1 AND id <> ?`, userID).Scan(&otherAdmins); err != nil {
			return err
		}
		if otherAdmins == 0 {
			return ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ?, enabled = ? WHERE id = ?`, role, boolInt(enabled), userID); err != nil {
		return err
	}
	if !enabled {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) SetUserPassword(ctx context.Context, userID, passwordHash string, mustChange bool) error {
	result, err := s.DB.ExecContext(ctx, `
		UPDATE users SET password_hash = ?, must_change_password = ?, password_changed_at = ? WHERE id = ?`,
		passwordHash, boolInt(mustChange), unixNow(), userID)
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

func (s *Store) DeleteUser(ctx context.Context, userID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var role string
	var enabled int
	if err := tx.QueryRowContext(ctx, `SELECT role, enabled FROM users WHERE id = ?`, userID).Scan(&role, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if role == "admin" && enabled == 1 {
		var otherAdmins int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'admin' AND enabled = 1 AND id <> ?`, userID).Scan(&otherAdmins); err != nil {
			return err
		}
		if otherAdmins == 0 {
			return ErrConflict
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) TouchUserLogin(ctx context.Context, userID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET last_login_at = ? WHERE id = ?`, unixNow(), userID)
	return err
}

func (s *Store) ClearMustChangePassword(ctx context.Context, userID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE users SET must_change_password = 0 WHERE id = ?`, userID)
	return err
}

func (s *Store) SetMustChangePassword(ctx context.Context, userID string, required bool) error {
	result, err := s.DB.ExecContext(ctx, `UPDATE users SET must_change_password = ? WHERE id = ?`, boolInt(required), userID)
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
