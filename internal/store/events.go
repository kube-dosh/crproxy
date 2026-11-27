package store

import (
	"context"
	"time"
)

type RegistryEvent struct {
	ID           int64     `json:"id"`
	At           time.Time `json:"at"`
	Type         string    `json:"type"`
	RepositoryID *int64    `json:"repositoryId,omitempty"`
	UpstreamID   *string   `json:"upstreamId,omitempty"`
	Digest       string    `json:"digest,omitempty"`
	Size         int64     `json:"size"`
	CacheHit     *bool     `json:"cacheHit,omitempty"`
	Result       string    `json:"result"`
	Message      string    `json:"message,omitempty"`
}

func (s *Store) AddRegistryEvent(ctx context.Context, event RegistryEvent) error {
	var repositoryID any
	if event.RepositoryID != nil && *event.RepositoryID > 0 {
		repositoryID = *event.RepositoryID
	}
	var upstreamID any
	if event.UpstreamID != nil && *event.UpstreamID != "" {
		upstreamID = *event.UpstreamID
	}
	var cacheHit any
	if event.CacheHit != nil {
		cacheHit = boolInt(*event.CacheHit)
	}
	at := event.At.UTC().Unix()
	if event.At.IsZero() {
		at = unixNow()
	}
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO registry_events(at, event_type, repository_id, upstream_id, digest, size, cache_hit, result, message)
		VALUES(?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, NULLIF(?, ''))`,
		at, event.Type, repositoryID, upstreamID, event.Digest, event.Size, cacheHit, event.Result, event.Message)
	return err
}

func (s *Store) ListRegistryEvents(ctx context.Context, limit int) ([]RegistryEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, at, event_type, repository_id, upstream_id, COALESCE(digest, ''), size, cache_hit, result, COALESCE(message, '')
		FROM registry_events ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RegistryEvent, 0)
	for rows.Next() {
		var item RegistryEvent
		var at int64
		var repoID *int64
		var upstreamID *string
		var cacheHit *int
		if err := rows.Scan(&item.ID, &at, &item.Type, &repoID, &upstreamID, &item.Digest, &item.Size, &cacheHit, &item.Result, &item.Message); err != nil {
			return nil, err
		}
		item.At = time.Unix(at, 0).UTC()
		item.RepositoryID = repoID
		if upstreamID != nil {
			value := *upstreamID
			item.UpstreamID = &value
		}
		if cacheHit != nil {
			value := *cacheHit == 1
			item.CacheHit = &value
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

type EventSummary struct {
	PullRequests   int64 `json:"pullRequests"`
	CacheHits      int64 `json:"cacheHits"`
	CacheMisses    int64 `json:"cacheMisses"`
	UpstreamErrors int64 `json:"upstreamErrors"`
	BytesServed    int64 `json:"bytesServed"`
	BytesCached    int64 `json:"bytesCached"`
}

type EventBucket struct {
	At             time.Time `json:"at"`
	PullRequests   int64     `json:"pullRequests"`
	CacheHits      int64     `json:"cacheHits"`
	CacheMisses    int64     `json:"cacheMisses"`
	UpstreamErrors int64     `json:"upstreamErrors"`
	BytesServed    int64     `json:"bytesServed"`
	BytesCached    int64     `json:"bytesCached"`
}

func (s *Store) EventBucketsSince(ctx context.Context, since time.Time) ([]EventBucket, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT (at / 3600) * 3600 AS bucket,
		       COALESCE(SUM(CASE WHEN event_type IN ('manifest.pull','blob.pull') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN cache_hit = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN cache_hit = 0 AND result = 'success' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN result = 'error' AND upstream_id IS NOT NULL THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN event_type IN ('manifest.pull','blob.pull') AND result='success' THEN size ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN event_type IN ('manifest.cache','blob.cache') AND result='success' THEN size ELSE 0 END), 0)
		FROM registry_events WHERE at >= ?
		GROUP BY bucket ORDER BY bucket`, since.UTC().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]EventBucket, 0)
	for rows.Next() {
		var item EventBucket
		var at int64
		if err := rows.Scan(&at, &item.PullRequests, &item.CacheHits, &item.CacheMisses,
			&item.UpstreamErrors, &item.BytesServed, &item.BytesCached); err != nil {
			return nil, err
		}
		item.At = time.Unix(at, 0).UTC()
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) EventSummarySince(ctx context.Context, since time.Time) (EventSummary, error) {
	var summary EventSummary
	err := s.DB.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN event_type IN ('manifest.pull','blob.pull') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN cache_hit = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN cache_hit = 0 AND result = 'success' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN result = 'error' AND upstream_id IS NOT NULL THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN event_type IN ('manifest.pull','blob.pull') AND result='success' THEN size ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN event_type IN ('manifest.cache','blob.cache') AND result='success' THEN size ELSE 0 END), 0)
		FROM registry_events WHERE at >= ?`, since.UTC().Unix()).Scan(&summary.PullRequests, &summary.CacheHits,
		&summary.CacheMisses, &summary.UpstreamErrors, &summary.BytesServed, &summary.BytesCached)
	return summary, err
}

func (s *Store) DeleteRegistryEventsBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.DB.ExecContext(ctx, `DELETE FROM registry_events WHERE at < ?`, before.UTC().Unix())
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
