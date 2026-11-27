package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kube-dosh/crproxy/internal/store"
)

func TestRunnerRecordsProgressResultCancelTimeoutAndPanic(t *testing.T) {
	ctx := context.Background()
	metadata, runner := newTestRunner(t)

	started, err := runner.Start(ctx, "scan", "usr_test", time.Minute, func(_ context.Context, progress func(string)) (any, error) {
		progress("halfway")
		return map[string]int{"checked": 3}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	finished := waitForJobState(t, metadata, started.ID, "succeeded")
	if finished.Progress == "" || !strings.Contains(finished.Progress, "halfway") || finished.Result == "" {
		t.Fatalf("successful job did not retain progress/result: %+v", finished)
	}
	var result map[string]int
	if err := json.Unmarshal([]byte(finished.Result), &result); err != nil || result["checked"] != 3 {
		t.Fatalf("unexpected result payload %q: %v", finished.Result, err)
	}
	if finished.StartedAt == nil || finished.FinishedAt == nil || finished.RequestedBy != "usr_test" {
		t.Fatalf("job timestamps or requester missing: %+v", finished)
	}

	runStarted := make(chan struct{})
	cancelled, err := runner.Start(ctx, "download", "", time.Minute, func(jobCtx context.Context, _ func(string)) (any, error) {
		close(runStarted)
		<-jobCtx.Done()
		return nil, jobCtx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-runStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start")
	}
	if !runner.Cancel(cancelled.ID) {
		t.Fatal("active job should be cancellable")
	}
	cancelled = waitForJobState(t, metadata, cancelled.ID, "failed")
	if !strings.Contains(cancelled.Error, "context canceled") {
		t.Fatalf("cancelled job should record context cancellation: %+v", cancelled)
	}

	timedOut, err := runner.Start(ctx, "timeout", "", 50*time.Millisecond, func(jobCtx context.Context, _ func(string)) (any, error) {
		<-jobCtx.Done()
		return nil, jobCtx.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	timedOut = waitForJobState(t, metadata, timedOut.ID, "failed")
	if !strings.Contains(timedOut.Error, "deadline exceeded") {
		t.Fatalf("timed out job should record deadline failure: %+v", timedOut)
	}

	panicked, err := runner.Start(ctx, "panic", "", time.Minute, func(context.Context, func(string)) (any, error) {
		panic("test panic")
	})
	if err != nil {
		t.Fatal(err)
	}
	panicked = waitForJobState(t, metadata, panicked.ID, "failed")
	if !strings.Contains(panicked.Error, "panic: test panic") {
		t.Fatalf("panicking job should be contained and recorded: %+v", panicked)
	}
}

func TestRunnerRecoversInterruptedJobs(t *testing.T) {
	ctx := context.Background()
	metadata, runner := newTestRunner(t)
	if err := metadata.CreateJob(ctx, "job_interrupted", "gc", "usr_test"); err != nil {
		t.Fatal(err)
	}
	if err := runner.RecoverInterrupted(ctx); err != nil {
		t.Fatal(err)
	}
	job, err := metadata.JobByID(ctx, "job_interrupted")
	if err != nil {
		t.Fatal(err)
	}
	if job.State != "failed" || !strings.Contains(job.Error, "process restarted") {
		t.Fatalf("interrupted job was not recovered: %+v", job)
	}
}

func newTestRunner(t *testing.T) (*store.Store, *Runner) {
	t.Helper()
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "meta", "crproxy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	metadata := store.New(db)
	runner, err := New(metadata, nil)
	if err != nil {
		t.Fatal(err)
	}
	return metadata, runner
}

func waitForJobState(t *testing.T, metadata *store.Store, jobID, state string) store.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		job, err := metadata.JobByID(context.Background(), jobID)
		if err != nil {
			t.Fatal(err)
		}
		if job.State == state {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := metadata.JobByID(context.Background(), jobID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	t.Fatalf("job %s did not reach state %s: %+v", jobID, state, job)
	return store.Job{}
}
