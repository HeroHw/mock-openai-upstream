package mockupstream

import (
	"hash/fnv"
	"time"
)

// behavior.go centralizes the deterministic "realism" knobs: latency, TTFT,
// error injection and jitter. Nothing here uses math/rand — every decision is
// derived from a stable hash of request inputs so CI runs are reproducible
// (doc §4.1).

// hashString returns a stable 64-bit FNV-1a hash of s.
func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// shouldInject reports whether a request keyed by `key` should be hit, given a
// rate in [0,1]. The decision is deterministic: with rate=0.1, exactly 1 in 10
// distinct keys hit. Callers pass "model + sequence" style keys so the Nth
// request of a model behaves identically across runs.
func shouldInject(key string, rate float64) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	// Map hash into [0,1000) and compare against rate*1000 for stable bucketing.
	bucket := hashString(key) % 1000
	return bucket < uint64(rate*1000)
}

// jitter returns a deterministic offset in [-max, +max] derived from key.
// Used to make sync delays / task durations look organic while staying
// reproducible per request (doc §7.1, §8.3).
func jitter(key string, max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	span := int64(max)*2 + 1 // range size in ns
	off := int64(hashString(key)%uint64(span)) - int64(max)
	return time.Duration(off)
}

// randomDelay returns a deterministic delay in [min, max] derived from key.
// Used to generate random-looking delays while staying reproducible per request.
func randomDelay(key string, min, max time.Duration) time.Duration {
	if min >= max {
		return min
	}
	span := int64(max - min)
	offset := int64(hashString(key) % uint64(span))
	return min + time.Duration(offset)
}

// sleepCtx sleeps for d, but returns early if done fires (e.g. client
// disconnect). Returns true if the full duration elapsed.
func sleepCtx(d time.Duration, done <-chan struct{}) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-done:
		return false
	}
}
