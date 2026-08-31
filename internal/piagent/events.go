package piagent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// eventSink consumes the agent's JSON event stream (stdout) and other
// output lines: it renders human progress lines for the caller's
// logger, counts assistant turns, captures the agent's final summary,
// and cancels the run context when the turn budget is reached.
//
// The stream is line-oriented JSON (pi --mode json); non-JSON lines
// (pi's own warnings, stderr) are forwarded as-is.
type eventSink struct {
	log      func(format string, args ...any)
	verbose  bool
	maxTurns int
	cancel   func()

	turns      int
	turnCapHit bool
	budgetHit  bool
	summary    string
}

func newEventSink(log func(string, ...any), verbose bool, maxTurns int, cancel func()) *eventSink {
	return &eventSink{
		log: log, verbose: verbose, maxTurns: maxTurns,
		cancel: cancel,
	}
}

// wireMessage is a message as it appears in pi's JSON events.
type wireMessage struct {
	Role         string `json:"role"`
	StopReason   string `json:"stopReason"`
	ErrorMessage string `json:"errorMessage"`
	Content      []struct {
		Text string `json:"text"`
	} `json:"content"`
}

// wireEvent is the flexible subset of pi's JSON events the sink
// renders. Unknown fields and event types are ignored (the raw line is
// still available via --verbose).
type wireEvent struct {
	Type string `json:"type"`

	// message_end / turn_end / agent_end carry messages.
	Message  wireMessage `json:"message"`
	Messages []struct {
		Role string `json:"role"`
	} `json:"messages"`

	// tool_execution_*
	ToolName string `json:"toolName"`
	Args     any    `json:"args"`

	// auto_retry_start
	Attempt     int    `json:"attempt"`
	MaxAttempts int    `json:"maxAttempts"`
	DelayMs     int    `json:"delayMs"`
	ErrMessage  string `json:"errorMessage"`

	// agent_end
	WillRetry bool `json:"willRetry"`
}

// onLine handles one output line of the agent process. It is called
// from several goroutines (stdout and stderr pumps) and must be
// concurrency-safe.
func (s *eventSink) onLine(stream, line string) {
	if line == "" {
		return
	}
	if stream == "stdout" {
		if ev, ok := parseEvent(line); ok {
			s.handleEvent(ev)
			if s.verbose {
				s.log("agent (raw): %s", line)
			}
			return
		}
	}
	// Non-event line (stderr warnings, or unparseable stdout):
	// forward as-is.
	s.log("agent: %s", line)
}

func parseEvent(line string) (wireEvent, bool) {
	var ev wireEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return ev, false
	}
	if ev.Type == "" {
		return ev, false
	}
	return ev, true
}

func (s *eventSink) handleEvent(ev wireEvent) {
	switch ev.Type {
	case "turn_end":
		// One assistant turn (response + tool results) finished.
		if ev.Message.Role != "assistant" {
			return
		}
		s.turns++
		s.log("agent: turn %d done", s.turns)
		if s.turns >= s.maxTurns {
			s.turnCapHit = true
			s.budgetHit = true
			s.cancel()
		}
	case "message_end":
		m := ev.Message
		if m.Role != "assistant" {
			return
		}
		if m.StopReason == "error" {
			msg := m.ErrorMessage
			if msg == "" {
				msg = "no error message reported"
			}
			s.log("agent: model error: %s", msg)
			return
		}
		text := joinText(m)
		if text == "" {
			return
		}
		s.summary = text
		s.log("agent: %s", renderProgress(text))
	case "tool_execution_start":
		s.log("agent: %s %s", ev.ToolName, toolArgsSummary(ev.Args))
	case "auto_retry_start":
		delay := ""
		if ev.DelayMs > 0 {
			delay = fmt.Sprintf(" (retry %d/%d in %.0fs)", ev.Attempt, ev.MaxAttempts, float64(ev.DelayMs)/1000)
		}
		s.log("agent: endpoint trouble%s: %s", delay, ev.ErrMessage)
	case "compaction_start":
		s.log("agent: compacting context ...")
	case "compaction_end":
		s.log("agent: context compacted")
	case "agent_end":
		if ev.WillRetry {
			s.log("agent: pausing to retry the model call ...")
		} else {
			s.log("agent: finished (%d turn%s)", s.turns, plural(s.turns))
		}
	}
}

// joinText concatenates the text blocks of a message.
func joinText(m wireMessage) string {
	var b strings.Builder
	for _, c := range m.Content {
		if c.Text != "" {
			b.WriteString(c.Text)
			b.WriteByte('\n')
		}
	}
	return strings.TrimSpace(b.String())
}

// renderProgress renders one assistant text message for the progress
// log: the first lines, truncated when long (the cut is announced).
func renderProgress(text string) string {
	const limit = 400
	if len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf(" … (%d more characters; see the agent log or --verbose for the full output)", len(text)-limit)
}

// toolArgsSummary renders a short summary of a tool call's arguments.
func toolArgsSummary(args any) string {
	if args == nil {
		return ""
	}
	m, ok := args.(map[string]any)
	if !ok {
		return compactJSON(args)
	}
	if cmd, ok := m["command"].(string); ok {
		return renderProgress(strings.TrimSpace(cmd))
	}
	if path, ok := m["path"].(string); ok {
		return path
	}
	if pattern, ok := m["pattern"].(string); ok {
		return pattern
	}
	return compactJSON(m)
}

// compactJSON renders v as a short single-line JSON string.
func compactJSON(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "<unrenderable>"
	}
	s := strings.TrimSpace(string(data))
	const limit = 200
	if len(s) <= limit {
		return s
	}
	return s[:limit] + "…"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
