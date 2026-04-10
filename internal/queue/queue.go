package queue

import (
	"context"
	"sync"

	"github.com/trusch/ghenkins/internal/poller"
)

type Queue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	jobs   []poller.Job
	cap    int
	closed bool
}

func New(cap int) *Queue {
	q := &Queue{cap: cap}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Enqueue adds j to the queue. If the queue is full and a job for the same
// WatchName exists, it replaces that job. If full with no match, the oldest
// job is dropped.
func (q *Queue) Enqueue(j poller.Job) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.jobs) < q.cap {
		q.jobs = append(q.jobs, j)
		q.cond.Signal()
		return
	}

	// Full: replace job for same WatchName if one exists.
	for i, existing := range q.jobs {
		if existing.WatchName == j.WatchName {
			q.jobs[i] = j
			return
		}
	}

	// Full, no match: drop oldest, append new.
	q.jobs = append(q.jobs[1:], j)
}

// Dequeue blocks until a job is available or ctx is done.
// Returns ctx.Err() if the context is cancelled.
func (q *Queue) Dequeue(ctx context.Context) (poller.Job, error) {
	stop := context.AfterFunc(ctx, func() {
		q.cond.Broadcast()
	})
	defer stop()

	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.jobs) == 0 && !q.closed {
		if ctx.Err() != nil {
			return poller.Job{}, ctx.Err()
		}
		q.cond.Wait()
	}

	if ctx.Err() != nil {
		return poller.Job{}, ctx.Err()
	}

	if q.closed && len(q.jobs) == 0 {
		return poller.Job{}, context.Canceled
	}

	j := q.jobs[0]
	q.jobs = q.jobs[1:]
	return j, nil
}

// Close wakes all Dequeue callers so they can observe the closed state.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}
