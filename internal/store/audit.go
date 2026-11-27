package store

import (
	"context"
	"time"
)

type AuditEvent struct {
	ID         int64     `json:"id"`
	At         time.Time `json:"at"`
	ActorType  string    `json:"actorType"`
	ActorID    string    `json:"actorId,omitempty"`
	Action     string    `json:"action"`
	TargetType string    `json:"targetType,omitempty"`
	TargetID   string    `json:"targetId,omitempty"`
	IP         string    `json:"ip,omitempty"`
	UserAgent  string    `json:"userAgent,omitempty"`
	Result     string    `json:"result"`
	Message    string    `json:"message,omitempty"`
	RequestID  string    `json:"requestId,omitempty"`
}

func (s *Store) AddAuditEvent(ctx context.Context, event AuditEvent) error {
	at := event.At.UTC().Unix()
	if event.At.IsZero() {
		at = unixNow()
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO audit_events(at, actor_type, actor_id, action, target_type, target_id, ip, user_agent, result, message, request_id)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		at, event.ActorType, event.ActorID, event.Action, event.TargetType, event.TargetID,
		event.IP, event.UserAgent, event.Result, event.Message, event.RequestID)
	return err
}

type AuditFilter struct {
	BeforeID int64
	Limit    int
	Action   string
	ActorID  string
	Result   string
}

func (s *Store) ListAuditEvents(ctx context.Context, filter AuditFilter) ([]AuditEvent, error) {
	if filter.Limit <= 0 || filter.Limit > 500 {
		filter.Limit = 100
	}
	query := `
		SELECT id, at, actor_type, COALESCE(actor_id, ''), action, COALESCE(target_type, ''),
		       COALESCE(target_id, ''), COALESCE(ip, ''), COALESCE(user_agent, ''), result,
		       COALESCE(message, ''), COALESCE(request_id, '')
		FROM audit_events
		WHERE (? = 0 OR id < ?)`
	args := []any{filter.BeforeID, filter.BeforeID}
	if filter.Action != "" {
		query += ` AND action = ?`
		args = append(args, filter.Action)
	}
	if filter.ActorID != "" {
		query += ` AND actor_id = ?`
		args = append(args, filter.ActorID)
	}
	if filter.Result != "" {
		query += ` AND result = ?`
		args = append(args, filter.Result)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, filter.Limit)
	rows, err := s.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]AuditEvent, 0)
	for rows.Next() {
		var event AuditEvent
		var at int64
		if err := rows.Scan(&event.ID, &at, &event.ActorType, &event.ActorID, &event.Action,
			&event.TargetType, &event.TargetID, &event.IP, &event.UserAgent, &event.Result,
			&event.Message, &event.RequestID); err != nil {
			return nil, err
		}
		event.At = time.Unix(at, 0).UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) DeleteAuditEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM audit_events WHERE at < ?`, before.UTC().Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
