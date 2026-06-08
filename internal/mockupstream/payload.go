package mockupstream

import (
	"strings"
	"sync/atomic"
)

// payload.go holds response-body helpers shared across protocols: deterministic
// content generation, token estimation and the usage block that the gateway's
// billing pipeline depends on (doc §5).

// seq is a process-wide monotonically increasing request counter. It feeds the
// "{seq}" placeholders in mock IDs and the deterministic error-injection key so
// the Nth request behaves identically across runs of the same workload.
var seq atomic.Uint64

func nextSeq() uint64 { return seq.Add(1) }

// estimateTokens approximates a token count from text. The real ratio varies by
// tokenizer; ~4 chars/token is the common rule of thumb. Always returns >=1 for
// non-empty input so usage is never zero.
func estimateTokens(text string) int {
	if text == "" {
		return 0
	}
	n := len([]rune(text)) / 4
	if n < 1 {
		n = 1
	}
	return n
}

// usage computes prompt/completion/total token counts according to UsageMode.
// "fixed" returns constant values for easy billing assertions; "echo" estimates
// from the actual input and output text (doc §5.1).
func (s *Server) usage(promptText, completionText string) (prompt, completion, total int) {
	if s.cfg.UsageMode == "fixed" {
		prompt, completion = 10, 20
	} else {
		prompt = estimateTokens(promptText)
		completion = estimateTokens(completionText)
	}
	return prompt, completion, prompt + completion
}

// replyText returns the configured chat reply text.
func (s *Server) replyText() string {
	return s.cfg.ReplyText
}

// splitTokens breaks reply text into stream chunks. We split on whitespace but
// keep the trailing space on each piece so concatenating the deltas reproduces
// the original text exactly — matching how real providers emit word-ish chunks.
func splitTokens(text string) []string {
	if text == "" {
		return nil
	}
	fields := strings.Fields(text)
	out := make([]string, 0, len(fields))
	for i, f := range fields {
		if i < len(fields)-1 {
			out = append(out, f+" ")
		} else {
			out = append(out, f)
		}
	}
	return out
}
