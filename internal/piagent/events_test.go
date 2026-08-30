package piagent

import (
	"strings"
	"testing"
)

// newTestSink builds a sink whose logger collects lines and whose
// cancel callback flips the returned bool.
func newTestSink(maxTurns int) (*eventSink, *logBuf, *bool) {
	logs := &logBuf{}
	cancelled := false
	s := newEventSink(logs.logf, false, maxTurns, func() { cancelled = true })
	return s, logs, &cancelled
}

// feed drives the sink with lines as if they came off the agent's
// stdout.
func feed(s *eventSink, lines ...string) {
	for _, l := range lines {
		s.onLine("stdout", l)
	}
}

func TestEventSinkTurnsAndSummary(t *testing.T) {
	s, logs, cancelled := newTestSink(5)
	feed(s,
		`{"type":"session","version":3,"id":"x"}`,
		`{"type":"agent_start"}`,
		`{"type":"turn_start"}`,
		`{"type":"message_start","message":{"role":"assistant","content":[]}}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"investigating"}],"stopReason":"stop"}}`,
		`{"type":"tool_execution_start","toolName":"bash","args":{"command":"go test ./..."}}`,
		`{"type":"turn_end","message":{"role":"assistant"},"toolResults":[]}`,
		`{"type":"turn_start"}`,
		`{"type":"message_end","message":{"role":"assistant","content":[{"type":"text","text":"Fixed the bug; go build and go test pass."}],"stopReason":"stop"}}`,
		`{"type":"turn_end","message":{"role":"assistant"},"toolResults":[]}`,
		`{"type":"agent_end","messages":[],"willRetry":false}`,
		`{"type":"agent_settled"}`,
	)
	if s.turns != 2 {
		t.Errorf("turns = %d, want 2", s.turns)
	}
	if s.summary != "Fixed the bug; go build and go test pass." {
		t.Errorf("summary = %q (the last assistant text)", s.summary)
	}
	if *cancelled {
		t.Error("the cancel callback fired under the turn cap")
	}
	for _, want := range []string{
		"agent: turn 1 done",
		"agent: turn 2 done",
		"agent: bash go test ./...",
		"agent: investigating",
		"agent: finished (2 turns)",
	} {
		if !logs.has(want) {
			t.Errorf("missing progress line %q:\n%s", want, logs)
		}
	}
}

func TestEventSinkTurnCapCancels(t *testing.T) {
	s, _, cancelled := newTestSink(2)
	feed(s,
		`{"type":"turn_end","message":{"role":"assistant"},"toolResults":[]}`,
		`{"type":"turn_end","message":{"role":"assistant"},"toolResults":[]}`,
	)
	if !*cancelled {
		t.Error("turn cap reached but the run was not cancelled")
	}
	if !s.budgetHit || !s.turnCapHit {
		t.Errorf("budget flags = {hit:%t turn:%t}, want both set", s.budgetHit, s.turnCapHit)
	}
}

func TestEventSinkModelErrorAndRetries(t *testing.T) {
	s, logs, _ := newTestSink(10)
	feed(s,
		`{"type":"message_end","message":{"role":"assistant","stopReason":"error","errorMessage":"500: {\"message\":\"boom\"}"}}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"500: boom"}`,
		`{"type":"compaction_start"}`,
		`{"type":"compaction_end"}`,
		`{"type":"agent_end","messages":[],"willRetry":true}`,
	)
	for _, want := range []string{
		"agent: model error: 500: {\"message\":\"boom\"}",
		"agent: endpoint trouble (retry 1/3 in 2s): 500: boom",
		"agent: compacting context ...",
		"agent: context compacted",
		"agent: pausing to retry the model call ...",
	} {
		if !logs.has(want) {
			t.Errorf("missing line %q:\n%s", want, logs)
		}
	}
}

func TestEventSinkNonJSONAndVerbose(t *testing.T) {
	s, logs, _ := newTestSink(10)
	s.verbose = true
	feed(s,
		`not json at all`,
		`{"type":"turn_end","message":{"role":"assistant"}}`,
	)
	if !logs.has("agent: not json at all") {
		t.Errorf("non-JSON line not forwarded:\n%s", logs)
	}
	if !logs.has("agent (raw): {") {
		t.Errorf("verbose raw event line missing:\n%s", logs)
	}
	if s.turns != 1 {
		t.Errorf("turns = %d, want 1", s.turns)
	}
}

func TestEventSinkIgnoresUserMessages(t *testing.T) {
	s, _, _ := newTestSink(10)
	feed(s,
		`{"type":"message_end","message":{"role":"user","content":[{"type":"text","text":"the task"}]}}`,
	)
	if s.summary != "" {
		t.Errorf("the user message became the summary: %q", s.summary)
	}
	if s.turns != 0 {
		t.Errorf("turns = %d, want 0 (no assistant turn yet)", s.turns)
	}
}

func TestToolArgsSummary(t *testing.T) {
	if got := toolArgsSummary(map[string]any{"command": "go test ./..."}); got != "go test ./..." {
		t.Errorf("bash command summary = %q", got)
	}
	if got := toolArgsSummary(map[string]any{"path": "/src/main.go"}); got != "/src/main.go" {
		t.Errorf("path summary = %q", got)
	}
	if got := toolArgsSummary(nil); got != "" {
		t.Errorf("nil args summary = %q", got)
	}
	long := strings.Repeat("x", 500)
	if got := toolArgsSummary(map[string]any{"command": long}); len(got) > 500 || !strings.Contains(got, "more characters") {
		t.Errorf("long command not truncated: %d bytes", len(got))
	}
}

func TestRenderProgressTruncates(t *testing.T) {
	short := renderProgress("short")
	if short != "short" {
		t.Errorf("short text mangled: %q", short)
	}
	long := renderProgress(strings.Repeat("a", 1000))
	if len(long) > 500 || !strings.Contains(long, "more characters") {
		t.Errorf("long text not truncated: %d bytes", len(long))
	}
}
