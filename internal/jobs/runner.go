package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"crproxy/internal/id"
	"crproxy/internal/store"
)

type Runner struct {
	store  *store.Store
	logger *slog.Logger
	mu     sync.Mutex
	active map[string]context.CancelFunc
}

func New(metadata *store.Store, logger *slog.Logger) (*Runner, error) {
	if metadata == nil {
		return nil, fmt.Errorf("job runner requires a store")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Runner{store: metadata, logger: logger, active: map[string]context.CancelFunc{}}, nil
}

func (r *Runner) RecoverInterrupted(ctx context.Context) error {
	return r.store.FailInterruptedJobs(ctx)
}

func (r *Runner) Start(ctx context.Context, jobType, requestedBy string, timeout time.Duration, run func(context.Context, func(string)) (any, error)) (store.Job, error) {
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	jobID := id.New("job_", 12)
	if err := r.store.CreateJob(ctx, jobID, jobType, requestedBy); err != nil {
		return store.Job{}, err
	}
	jobCtx, cancel := context.WithTimeout(context.Background(), timeout)
	r.mu.Lock()
	r.active[jobID] = cancel
	r.mu.Unlock()
	go func() {
		defer func() {
			cancel()
			r.mu.Lock()
			delete(r.active, jobID)
			r.mu.Unlock()
			if recovered := recover(); recovered != nil {
				r.logger.Error("background job panicked", "job_id", jobID, "type", jobType, "panic", recovered, "stack", string(debug.Stack()))
				_ = r.store.FinishJob(context.Background(), jobID, "failed", "", fmt.Sprintf("panic: %v", recovered))
			}
		}()
		if err := r.store.StartJob(jobCtx, jobID); err != nil {
			r.logger.Error("start job record failed", "job_id", jobID, "error", err)
			return
		}
		progress := func(message string) {
			encoded, _ := json.Marshal(map[string]string{"message": message, "at": time.Now().UTC().Format(time.RFC3339)})
			_ = r.store.UpdateJobProgress(context.Background(), jobID, string(encoded))
		}
		result, err := run(jobCtx, progress)
		if err != nil {
			_ = r.store.FinishJob(context.Background(), jobID, "failed", "", err.Error())
			r.logger.Warn("background job failed", "job_id", jobID, "type", jobType, "error", err)
			return
		}
		encoded := ""
		if result != nil {
			raw, encodeErr := json.Marshal(result)
			if encodeErr != nil {
				_ = r.store.FinishJob(context.Background(), jobID, "failed", "", "encode job result: "+encodeErr.Error())
				return
			}
			encoded = string(raw)
		}
		_ = r.store.FinishJob(context.Background(), jobID, "succeeded", encoded, "")
		r.logger.Info("background job completed", "job_id", jobID, "type", jobType)
	}()
	return r.store.JobByID(ctx, jobID)
}

func (r *Runner) Cancel(jobID string) bool {
	r.mu.Lock()
	cancel := r.active[jobID]
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
