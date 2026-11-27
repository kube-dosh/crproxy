package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type Job struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	State       string     `json:"state"`
	Progress    string     `json:"progressJson,omitempty"`
	Result      string     `json:"resultJson,omitempty"`
	RequestedBy string     `json:"requestedBy,omitempty"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
}

func (s *Store) CreateJob(ctx context.Context, jobID, jobType, requestedBy string) error {
	_, err := s.DB.ExecContext(ctx, `
		INSERT INTO jobs(id, type, state, requested_by) VALUES(?, ?, 'queued', NULLIF(?, ''))`,
		jobID, jobType, requestedBy)
	return err
}

func (s *Store) StartJob(ctx context.Context, jobID string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE jobs SET state='running', started_at=? WHERE id=?`, unixNow(), jobID)
	return err
}

func (s *Store) UpdateJobProgress(ctx context.Context, jobID, progressJSON string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE jobs SET progress_json=NULLIF(?, '') WHERE id=?`, progressJSON, jobID)
	return err
}

func (s *Store) FinishJob(ctx context.Context, jobID, state, resultJSON, errorMessage string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE jobs SET state=?, result_json=NULLIF(?, ''), error=NULLIF(?, ''), finished_at=? WHERE id=?`,
		state, resultJSON, errorMessage, unixNow(), jobID)
	return err
}

func (s *Store) JobByID(ctx context.Context, jobID string) (Job, error) {
	return scanJob(s.DB.QueryRowContext(ctx, `
		SELECT id, type, state, COALESCE(progress_json, ''), COALESCE(result_json, ''), COALESCE(requested_by, ''),
		       started_at, finished_at, COALESCE(error, '') FROM jobs WHERE id=?`, jobID))
}

func (s *Store) ListJobs(ctx context.Context, limit int) ([]Job, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, type, state, COALESCE(progress_json, ''), COALESCE(result_json, ''), COALESCE(requested_by, ''),
		       started_at, finished_at, COALESCE(error, '')
		FROM jobs ORDER BY COALESCE(started_at, finished_at, 0) DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Job, 0)
	for rows.Next() {
		var job Job
		var started, finished sql.NullInt64
		if err := rows.Scan(&job.ID, &job.Type, &job.State, &job.Progress, &job.Result, &job.RequestedBy,
			&started, &finished, &job.Error); err != nil {
			return nil, err
		}
		if started.Valid {
			value := time.Unix(started.Int64, 0).UTC()
			job.StartedAt = &value
		}
		if finished.Valid {
			value := time.Unix(finished.Int64, 0).UTC()
			job.FinishedAt = &value
		}
		result = append(result, job)
	}
	return result, rows.Err()
}

func scanJob(row *sql.Row) (Job, error) {
	var job Job
	var started, finished sql.NullInt64
	err := row.Scan(&job.ID, &job.Type, &job.State, &job.Progress, &job.Result, &job.RequestedBy,
		&started, &finished, &job.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if started.Valid {
		value := time.Unix(started.Int64, 0).UTC()
		job.StartedAt = &value
	}
	if finished.Valid {
		value := time.Unix(finished.Int64, 0).UTC()
		job.FinishedAt = &value
	}
	return job, nil
}

func (s *Store) FailInterruptedJobs(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE jobs SET state='failed', error='process restarted', finished_at=?
		WHERE state IN ('queued','running')`, unixNow())
	return err
}
