package mockupstream

import (
	"testing"
	"time"
)

func testCfg() Config {
	c := defaults()
	c.ImageDuration = 60 * time.Second
	c.VideoDuration = 60 * time.Second
	c.VideoConcurrency = 2
	c.TaskJitter = 0 // deterministic timing in tests
	c.TaskFailRate = 0
	return c
}

func TestTaskStatusProgression(t *testing.T) {
	q := NewTaskQueue(testCfg())
	now := time.Unix(1_000_000, 0)
	task := q.Submit(taskKindImage, "m", now)

	if got := task.Status(now); got != statusRunning {
		// image starts immediately (StartAt == now), so at exactly now it's RUNNING.
		t.Fatalf("at submit time want RUNNING, got %s", got)
	}
	if got := task.Status(now.Add(30 * time.Second)); got != statusRunning {
		t.Fatalf("mid-flight want RUNNING, got %s", got)
	}
	if got := task.Status(now.Add(61 * time.Second)); got != statusSucceeded {
		t.Fatalf("after finish want SUCCEEDED, got %s", got)
	}
}

func TestTaskFailureDeterministic(t *testing.T) {
	c := testCfg()
	c.TaskFailRate = 1 // every task fails
	q := NewTaskQueue(c)
	now := time.Unix(2_000_000, 0)
	task := q.Submit(taskKindImage, "m", now)
	if got := task.Status(now.Add(61 * time.Second)); got != statusFailed {
		t.Fatalf("want FAILED with fail rate 1, got %s", got)
	}
}

func TestVideoConcurrencyQueueing(t *testing.T) {
	c := testCfg()
	c.VideoConcurrency = 1 // only one video at a time
	q := NewTaskQueue(c)
	now := time.Unix(3_000_000, 0)

	t1 := q.Submit(taskKindVideo, "m", now)
	t2 := q.Submit(taskKindVideo, "m", now)
	t3 := q.Submit(taskKindVideo, "m", now)

	// First starts now; second waits for first to finish; third waits for second.
	if !t1.StartAt.Equal(now) {
		t.Fatalf("t1 should start immediately, started at %v", t1.StartAt)
	}
	if !t2.StartAt.Equal(t1.FinishAt) {
		t.Fatalf("t2 should start when t1 finishes (%v), started at %v", t1.FinishAt, t2.StartAt)
	}
	if !t3.StartAt.Equal(t2.FinishAt) {
		t.Fatalf("t3 should start when t2 finishes (%v), started at %v", t2.FinishAt, t3.StartAt)
	}

	// At submit time, t2 and t3 must be PENDING (queued), t1 RUNNING.
	if got := t2.Status(now); got != statusPending {
		t.Fatalf("queued t2 want PENDING, got %s", got)
	}
	if got := t3.Status(now); got != statusPending {
		t.Fatalf("queued t3 want PENDING, got %s", got)
	}
}

func TestImageUnboundedConcurrency(t *testing.T) {
	q := NewTaskQueue(testCfg())
	now := time.Unix(4_000_000, 0)
	// Many images submitted at once should all start immediately.
	for i := 0; i < 50; i++ {
		task := q.Submit(taskKindImage, "m", now)
		if !task.StartAt.Equal(now) {
			t.Fatalf("image %d should start immediately, got %v", i, task.StartAt)
		}
	}
}
