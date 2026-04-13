// Package batch provides a job queue with human-like pacing for platform actions.
//
// When a user wants to post 20 items or send 20 messages, Claude submits them
// as a single batch. The queue runs them one by one with random delays (5-10s)
// to mimic human speed and avoid detection/rate limits.
package batch

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"sync"
	"time"
)

// JobType identifies the kind of action to perform.
type JobType string

const (
	JobCreatePost    JobType = "create_post"
	JobSendMessage   JobType = "send_message"
	JobReplyComment  JobType = "reply_comment"
)

// JobStatus tracks the state of a single job.
type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
)

// Job is a single action in a batch.
type Job struct {
	ID       int       `json:"id"`
	Type     JobType   `json:"type"`
	Platform string    `json:"platform"`
	Params   map[string]string `json:"params"`
	Status   JobStatus `json:"status"`
	Error    string    `json:"error,omitempty"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Batch is a group of jobs submitted together.
type Batch struct {
	ID        string     `json:"id"`
	Platform  string     `json:"platform"`
	Type      JobType    `json:"type"`
	Jobs      []*Job     `json:"jobs"`
	Status    JobStatus  `json:"status"`
	MinDelay  int        `json:"min_delay_seconds"` // minimum seconds between jobs
	MaxDelay  int        `json:"max_delay_seconds"` // maximum seconds between jobs
	CreatedAt time.Time  `json:"created_at"`
	Progress  int        `json:"progress"` // number of completed jobs
	Total     int        `json:"total"`
}

// ExecutorFunc is the function that actually performs a job.
// The queue calls this for each job with the job's params.
type ExecutorFunc func(ctx context.Context, jobType JobType, platform string, params map[string]string) error

// Queue manages batches of jobs with pacing.
type Queue struct {
	mu       sync.RWMutex
	batches  map[string]*Batch
	executor ExecutorFunc
	logger   *log.Logger
	counter  int // for generating batch IDs
}

// NewQueue creates a new batch queue with the given executor function.
func NewQueue(executor ExecutorFunc) *Queue {
	return &Queue{
		batches:  make(map[string]*Batch),
		executor: executor,
		logger:   log.Default(),
	}
}

// Submit creates a new batch and starts processing it in the background.
// Returns the batch ID for status tracking.
func (q *Queue) Submit(platform string, jobType JobType, items []map[string]string, minDelay, maxDelay int) string {
	q.mu.Lock()
	q.counter++
	batchID := fmt.Sprintf("batch_%s_%s_%d", platform, jobType, q.counter)

	if minDelay <= 0 {
		minDelay = 5
	}
	if maxDelay <= 0 || maxDelay < minDelay {
		maxDelay = minDelay + 5
	}

	batch := &Batch{
		ID:        batchID,
		Platform:  platform,
		Type:      jobType,
		Status:    StatusRunning,
		MinDelay:  minDelay,
		MaxDelay:  maxDelay,
		CreatedAt: time.Now(),
		Total:     len(items),
	}

	for i, params := range items {
		batch.Jobs = append(batch.Jobs, &Job{
			ID:       i + 1,
			Type:     jobType,
			Platform: platform,
			Params:   params,
			Status:   StatusPending,
		})
	}

	q.batches[batchID] = batch
	q.mu.Unlock()

	q.logger.Printf("[batch] Submitted %s: %d %s jobs (delay %d-%ds)", batchID, len(items), jobType, minDelay, maxDelay)

	go q.processBatch(batchID)

	return batchID
}

// GetBatch returns the current state of a batch.
func (q *Queue) GetBatch(batchID string) *Batch {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return q.batches[batchID]
}

// ListBatches returns all batches, optionally filtered by platform.
func (q *Queue) ListBatches(platform string) []*Batch {
	q.mu.RLock()
	defer q.mu.RUnlock()

	var result []*Batch
	for _, b := range q.batches {
		if platform == "" || b.Platform == platform {
			result = append(result, b)
		}
	}
	return result
}

// CancelBatch stops a running batch. Jobs already completed stay completed.
func (q *Queue) CancelBatch(batchID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	batch, ok := q.batches[batchID]
	if !ok {
		return false
	}
	if batch.Status != StatusRunning {
		return false
	}
	batch.Status = StatusFailed
	for _, job := range batch.Jobs {
		if job.Status == StatusPending {
			job.Status = StatusFailed
			job.Error = "batch cancelled"
		}
	}
	return true
}

func (q *Queue) processBatch(batchID string) {
	q.mu.RLock()
	batch := q.batches[batchID]
	q.mu.RUnlock()

	if batch == nil {
		return
	}

	ctx := context.Background()

	for i, job := range batch.Jobs {
		// Check if batch was cancelled
		q.mu.RLock()
		if batch.Status != StatusRunning {
			q.mu.RUnlock()
			return
		}
		q.mu.RUnlock()

		// Wait between jobs (except before the first one)
		if i > 0 {
			delay := q.randomDelay(batch.MinDelay, batch.MaxDelay)
			q.logger.Printf("[batch] %s: waiting %v before job %d/%d", batchID, delay, i+1, batch.Total)
			time.Sleep(delay)
		}

		// Check again after delay
		q.mu.RLock()
		if batch.Status != StatusRunning {
			q.mu.RUnlock()
			return
		}
		q.mu.RUnlock()

		// Execute the job
		now := time.Now()
		q.mu.Lock()
		job.Status = StatusRunning
		job.StartedAt = &now
		q.mu.Unlock()

		q.logger.Printf("[batch] %s: running job %d/%d (%s)", batchID, i+1, batch.Total, job.Type)

		err := q.executor(ctx, job.Type, job.Platform, job.Params)

		finished := time.Now()
		q.mu.Lock()
		job.FinishedAt = &finished
		if err != nil {
			job.Status = StatusFailed
			job.Error = err.Error()
			q.logger.Printf("[batch] %s: job %d FAILED: %v", batchID, i+1, err)
		} else {
			job.Status = StatusCompleted
			batch.Progress++
			q.logger.Printf("[batch] %s: job %d completed (%d/%d)", batchID, i+1, batch.Progress, batch.Total)
		}
		q.mu.Unlock()
	}

	// Mark batch as completed
	q.mu.Lock()
	allDone := true
	for _, job := range batch.Jobs {
		if job.Status == StatusFailed {
			allDone = false
			break
		}
	}
	if allDone {
		batch.Status = StatusCompleted
	} else {
		batch.Status = StatusCompleted // still mark completed, individual jobs show failures
	}
	q.mu.Unlock()

	q.logger.Printf("[batch] %s: finished — %d/%d succeeded", batchID, batch.Progress, batch.Total)
}

func (q *Queue) randomDelay(minSec, maxSec int) time.Duration {
	if maxSec <= minSec {
		return time.Duration(minSec) * time.Second
	}
	secs := minSec + rand.Intn(maxSec-minSec+1)
	// Add some millisecond jitter for more human-like timing
	ms := rand.Intn(1000)
	return time.Duration(secs)*time.Second + time.Duration(ms)*time.Millisecond
}
