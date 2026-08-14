// Package queue implements the per-user download queue. Each user gets one
// goroutine that processes jobs sequentially: queued → downloading → done | failed.
package queue

import (
	"context"
	"log/slog"
	"sync"
)

// Status mirrors the download status enum in the DB schema.
type Status string

const (
	StatusQueued      Status = "queued"
	StatusDownloading Status = "downloading"
	StatusDone        Status = "done"
	StatusFailed      Status = "failed"
)

// Job is a single download request passed to the worker.
type Job struct {
	ID           string
	UserID       string
	URL          string
	Quality      string
	SponsorBlock bool
}

// WorkerFunc processes one job. It is responsible for updating the job's
// status in the database and writing the file to disk.
type WorkerFunc func(ctx context.Context, job Job) error

// Manager owns one goroutine per user and routes incoming jobs to the
// appropriate queue. The manager's lifetime is tied to the context passed
// to New; cancelling it stops all workers after their current job completes.
type Manager struct {
	ctx    context.Context
	mu     sync.Mutex
	queues map[string]*userQueue
	wg     sync.WaitGroup
	worker WorkerFunc
	log    *slog.Logger
}

type userQueue struct {
	ch chan Job
}

// New creates a Manager. ctx controls the lifetime of all worker goroutines.
func New(ctx context.Context, worker WorkerFunc, log *slog.Logger) *Manager {
	return &Manager{
		ctx:    ctx,
		queues: make(map[string]*userQueue),
		worker: worker,
		log:    log,
	}
}

// Enqueue adds job to the user's queue, starting a worker goroutine if one is
// not already running for that user.
func (m *Manager) Enqueue(job Job) {
	m.mu.Lock()
	uq, ok := m.queues[job.UserID]
	if !ok {
		uq = m.startWorker(job.UserID)
		m.queues[job.UserID] = uq
	}
	m.mu.Unlock()

	select {
	case uq.ch <- job:
	case <-m.ctx.Done():
		m.log.Warn("enqueue dropped: context cancelled", "user_id", job.UserID, "job_id", job.ID)
	}
}

func (m *Manager) startWorker(userID string) *userQueue {
	uq := &userQueue{ch: make(chan Job, 64)}
	m.wg.Go(func() { m.run(userID, uq.ch) })
	return uq
}

func (m *Manager) run(userID string, ch <-chan Job) {
	for {
		select {
		case job, ok := <-ch:
			if !ok {
				return
			}
			if err := m.worker(m.ctx, job); err != nil {
				m.log.Error("job failed", "user_id", userID, "job_id", job.ID, "err", err)
			}
		case <-m.ctx.Done():
			return
		}
	}
}

// Wait blocks until all worker goroutines have exited. Call after the context
// passed to New has been cancelled.
func (m *Manager) Wait() {
	m.wg.Wait()
}
