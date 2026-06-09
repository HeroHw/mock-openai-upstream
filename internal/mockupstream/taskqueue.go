package mockupstream

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// taskqueue.go is the in-memory task table + time-based state machine backing
// the DashScope async image/video flow (doc §8). There is no background worker:
// a task's status is computed from the current time relative to its scheduled
// StartAt/FinishAt, which is simple and race-free (doc §8.2).

const (
	taskKindImage = "image"
	taskKindVideo = "video"

	statusPending   = "PENDING"
	statusRunning   = "RUNNING"
	statusSucceeded = "SUCCEEDED"
	statusFailed    = "FAILED"
)

// Task is one async generation job.
type Task struct {
	ID         string
	Kind       string // image | video
	Model      string
	SubmitAt   time.Time
	StartAt    time.Time // when it transitions PENDING → RUNNING
	FinishAt   time.Time // when it transitions RUNNING → SUCCEEDED/FAILED
	ResultURLs []string  // populated for SUCCEEDED (filled lazily at query time)
	ErrCode    string    // populated for FAILED
	ErrMessage string
	willFail   bool // decided deterministically at submit time
}

// TaskQueue holds all tasks and assigns StartAt according to concurrency slots.
type TaskQueue struct {
	cfg Config
	mu  sync.Mutex
	// tasks keyed by ID. Kept for the life of the process (mock is short-lived).
	tasks map[string]*Task
	seq   uint64
}

func NewTaskQueue(cfg Config) *TaskQueue {
	return &TaskQueue{cfg: cfg, tasks: map[string]*Task{}}
}

// Submit creates a task, computes its schedule and stores it. now is passed in
// so callers (and tests) control the clock.
func (q *TaskQueue) Submit(kind, model string, now time.Time) *Task {
	q.mu.Lock()
	defer q.mu.Unlock()

	q.seq++
	id := fmt.Sprintf("mock-task-%d-%d", now.UnixNano(), q.seq)

	var minDuration, maxDuration time.Duration
	concurrency := 1 << 30 // images effectively unbounded by default
	if kind == taskKindVideo {
		minDuration = q.cfg.VideoDurationMin
		maxDuration = q.cfg.VideoDurationMax
		concurrency = q.cfg.VideoConcurrency
		if concurrency < 1 {
			concurrency = 1
		}
	} else {
		minDuration = q.cfg.ImageDurationMin
		maxDuration = q.cfg.ImageDurationMax
	}
	duration := randomDelay(id, minDuration, maxDuration)

	startAt := q.scheduleStart(kind, concurrency, now)
	t := &Task{
		ID:       id,
		Kind:     kind,
		Model:    model,
		SubmitAt: now,
		StartAt:  startAt,
		FinishAt: startAt.Add(duration),
		willFail: shouldInject(id, q.cfg.TaskFailRate),
	}
	q.tasks[id] = t
	return t
}

// scheduleStart picks StartAt for a new task of the given kind. With
// `concurrency` parallel slots, the new task can begin once the number of
// still-in-flight tasks of this kind would drop below `concurrency`. Sorting
// the in-flight FinishAt values ascending, that moment is the
// (k - concurrency)-th smallest finish time (doc §8.2 queue capacity). Caller
// holds q.mu.
func (q *TaskQueue) scheduleStart(kind string, concurrency int, now time.Time) time.Time {
	// FinishAt of tasks of this kind still occupying a slot (RUNNING or queued).
	var finishes []time.Time
	for _, t := range q.tasks {
		if t.Kind != kind {
			continue
		}
		if t.FinishAt.After(now) {
			finishes = append(finishes, t.FinishAt)
		}
	}
	if len(finishes) < concurrency {
		return now
	}
	sort.Slice(finishes, func(i, j int) bool { return finishes[i].Before(finishes[j]) })
	// After finishes[k-concurrency] completes, a slot is free for the new task.
	return finishes[len(finishes)-concurrency]
}

// Get returns a task by ID, or nil if unknown.
func (q *TaskQueue) Get(id string) *Task {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.tasks[id]
}

// Status computes a task's current status from the clock (doc §8.2).
func (t *Task) Status(now time.Time) string {
	switch {
	case now.Before(t.StartAt):
		return statusPending
	case now.Before(t.FinishAt):
		return statusRunning
	case t.willFail:
		return statusFailed
	default:
		return statusSucceeded
	}
}
