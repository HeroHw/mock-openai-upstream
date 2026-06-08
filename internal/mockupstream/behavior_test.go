package mockupstream

import (
	"testing"
	"time"
)

func TestShouldInjectDeterministic(t *testing.T) {
	if shouldInject("x", 0) {
		t.Fatal("rate 0 must never inject")
	}
	if !shouldInject("x", 1) {
		t.Fatal("rate 1 must always inject")
	}
	// Same key, same rate → same decision, repeatably.
	a := shouldInject("model#7", 0.5)
	b := shouldInject("model#7", 0.5)
	if a != b {
		t.Fatal("injection decision must be deterministic for a fixed key")
	}
}

func TestShouldInjectRateApprox(t *testing.T) {
	// Across many distinct keys, ~10% should hit at rate 0.1.
	hits := 0
	const total = 1000
	for i := 0; i < total; i++ {
		if shouldInject("m#"+itoa(i), 0.1) {
			hits++
		}
	}
	if hits < 50 || hits > 150 {
		t.Fatalf("expected ~100 hits at rate 0.1, got %d", hits)
	}
}

func TestJitterBounds(t *testing.T) {
	max := 5 * time.Second
	for i := 0; i < 100; i++ {
		j := jitter("task#"+itoa(i), max)
		if j < -max || j > max {
			t.Fatalf("jitter %v out of [-%v,%v]", j, max, max)
		}
	}
	if jitter("k", max) != jitter("k", max) {
		t.Fatal("jitter must be deterministic for a fixed key")
	}
}

func TestEstimateTokens(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Fatal("empty text → 0 tokens")
	}
	if estimateTokens("a") < 1 {
		t.Fatal("non-empty text → at least 1 token")
	}
}

func TestSplitTokensRoundTrip(t *testing.T) {
	in := "hello mock world"
	toks := splitTokens(in)
	joined := ""
	for _, tk := range toks {
		joined += tk
	}
	if joined != in {
		t.Fatalf("concatenated tokens %q != original %q", joined, in)
	}
}
