package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"crproxy/internal/id"
)

type APIToken struct {
	ID         string
	UserID     string
	Username   string
	UserRole   string
	UserActive bool
	Name       string
	Prefix     string
	SecretHash string
	Scopes     []string
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

func (s *Store) CreateAPIToken(ctx context.Context, tokenID, userID, name, prefix, secretHash string, scopes []string, expiresAt *time.Time) (string, error) {
	if tokenID == "" {
		tokenID = id.New("", 6)
	}
	var expiresUnix any
	if expiresAt != nil {
		expiresUnix = expiresAt.UTC().Unix()
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO api_tokens(id, user_id, name, prefix, secret_hash, scopes, expires_at, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
		tokenID, userID, name, prefix, secretHash, strings.Join(scopes, ","), expiresUnix, unixNow())
	if err != nil {
		return "", err
	}
	return tokenID, nil
}

func (s *Store) APITokenByID(ctx context.Context, tokenID string) (APIToken, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT t.id, t.user_id, u.username, u.role, u.enabled, t.name, t.prefix, t.secret_hash,
		       t.scopes, t.expires_at, t.revoked_at, t.created_at
		FROM api_tokens t
		JOIN users u ON u.id = t.user_id
		WHERE t.id = ?`, tokenID)
	var token APIToken
	var scopes string
	var enabled int
	var expiresAt, revokedAt sql.NullInt64
	var createdAt int64
	err := row.Scan(&token.ID, &token.UserID, &token.Username, &token.UserRole, &enabled, &token.Name,
		&token.Prefix, &token.SecretHash, &scopes, &expiresAt, &revokedAt, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, err
	}
	token.UserActive = enabled == 1
	token.Scopes = splitScopes(scopes)
	token.CreatedAt = time.Unix(createdAt, 0).UTC()
	if expiresAt.Valid {
		value := time.Unix(expiresAt.Int64, 0).UTC()
		token.ExpiresAt = &value
	}
	if revokedAt.Valid {
		value := time.Unix(revokedAt.Int64, 0).UTC()
		token.RevokedAt = &value
	}
	return token, nil
}

func (s *Store) TouchAPIToken(ctx context.Context, tokenID string) {
	_, _ = s.DB.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, unixNow(), tokenID)
}

func (s *Store) ListAPITokens(ctx context.Context, userID string) ([]APIToken, error) {
	query := `
		SELECT t.id, t.user_id, u.username, u.role, u.enabled, t.name, t.prefix, t.secret_hash,
		       t.scopes, t.expires_at, t.revoked_at, t.created_at
		FROM api_tokens t JOIN users u ON u.id = t.user_id`
	args := []any{}
	if userID != "" {
		query += ` WHERE t.user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY t.created_at DESC, t.id`
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := make([]APIToken, 0)
	for rows.Next() {
		token, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, rows.Err()
}

func (s *Store) CountActiveAPITokens(ctx context.Context, userID string, now time.Time) (int, error) {
	var count int
	err := s.DB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM api_tokens
		WHERE user_id = ? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)`,
		userID, now.UTC().Unix()).Scan(&count)
	return count, err
}

func (s *Store) RevokeAPIToken(ctx context.Context, tokenID string) error {
	result, err := s.DB.ExecContext(ctx, `
		UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`, unixNow(), tokenID)
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

type tokenScanner interface {
	Scan(dest ...any) error
}

func scanAPIToken(row tokenScanner) (APIToken, error) {
	var token APIToken
	var scopes string
	var enabled int
	var expiresAt, revokedAt sql.NullInt64
	var createdAt int64
	if err := row.Scan(&token.ID, &token.UserID, &token.Username, &token.UserRole, &enabled, &token.Name,
		&token.Prefix, &token.SecretHash, &scopes, &expiresAt, &revokedAt, &createdAt); err != nil {
		return APIToken{}, err
	}
	token.UserActive = enabled == 1
	token.Scopes = splitScopes(scopes)
	token.CreatedAt = time.Unix(createdAt, 0).UTC()
	if expiresAt.Valid {
		value := time.Unix(expiresAt.Int64, 0).UTC()
		token.ExpiresAt = &value
	}
	if revokedAt.Valid {
		value := time.Unix(revokedAt.Int64, 0).UTC()
		token.RevokedAt = &value
	}
	return token, nil
}

func splitScopes(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
