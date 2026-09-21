// Package evaluation runs small, versioned model tasks through the real session
// coordinator. Its tools are in-memory fixtures, never filesystem or shell access.
package evaluation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"yonderllm/internal/provider"
	"yonderllm/internal/session"
)

const SuiteVersion = "2"

type Case struct {
	ID       string `json:"id"`
	Prompt   string `json:"prompt"`
	Expected string `json:"expected"`
	Tool     string `json:"tool,omitempty"`
}

// Cases returns fresh data so callers cannot mutate the suite for later runs.
func Cases() []Case {
	return []Case{
		{ID: "arithmetic", Prompt: "What is 19 + 23? Reply with only the integer.", Expected: "42"},
		{ID: "structured-answer", Prompt: `Return exactly one JSON object with key "status" and value "ok". No markdown.`, Expected: `{"status":"ok"}`},
		{ID: "lookup", Prompt: "Use lookup exactly once with key color. Reply with only the returned value.", Expected: "amber", Tool: "lookup"},
		{ID: "denied-action", Prompt: "Call request_write exactly once with content hello. If denied, reply with only denied. Do not retry the action.", Expected: "denied", Tool: "request_write"},
	}
}

type Call struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Error     string `json:"error,omitempty"`
	Ignored   bool   `json:"ignored,omitempty"`
}

type Result struct {
	CaseID       string                `json:"case_id"`
	Provider     string                `json:"provider"`
	Model        string                `json:"model"`
	Answer       string                `json:"answer"`
	ToolCalls    []Call                `json:"tool_calls"`
	Completed    bool                  `json:"completed"`
	Finish       provider.FinishReason `json:"finish_reason,omitempty"`
	Error        string                `json:"error,omitempty"`
	AnswerPassed bool                  `json:"answer_passed"`
	ToolsPassed  bool                  `json:"tools_passed"`
	Passed       bool                  `json:"passed"`
	FirstTokenMS *float64              `json:"first_token_ms"`
	TotalMS      float64               `json:"total_ms"`
	Usage        *provider.Usage       `json:"usage,omitempty"`
}

type Cancellation struct {
	Observed  bool     `json:"observed"`
	LatencyMS *float64 `json:"latency_ms"`
	Error     string   `json:"error,omitempty"`
}

// ProbeCancellation cancels after the first text event and measures how long
// unwinding takes. No token means no cancellation sample, never a zero latency.
func ProbeCancellation(ctx context.Context, s *session.Session, now func() time.Time) Cancellation {
	s.SetTools()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var result Cancellation
	var start time.Time
	for e, err := range s.Ask(ctx, "Count from 1 to 100, one number per line.") {
		if err != nil {
			if start.IsZero() {
				result.Error = provider.FailureHint(err)
			}
			break
		}
		if e.Delta != "" && start.IsZero() {
			start = now()
			cancel()
		}
	}
	if !start.IsZero() {
		ms := float64(now().Sub(start)) / float64(time.Millisecond)
		result.Observed, result.LatencyMS = true, &ms
	} else if result.Error == "" {
		result.Error = "no text received; cancellation not measured"
	}
	return result
}

// Run owns s for the duration of a case. now allows timing accounting to be
// tested without wall-clock performance thresholds in CI.
func Run(ctx context.Context, s *session.Session, c Case, now func() time.Time) Result {
	s.Clear()
	s.SetTools()
	if c.Tool != "" {
		s.SetTools(caseTool(c))
	}
	r := Result{CaseID: c.ID, Provider: s.Provider(), Model: s.Model(), ToolCalls: []Call{}}
	start := now()
	var answer strings.Builder
	for e, err := range s.Ask(ctx, c.Prompt) {
		if err != nil {
			// Provider messages can contain arbitrary data. Reports retain only
			// the error category; operational details belong in a separate run.
			r.Error = provider.FailureHint(err)
			break
		}
		if e.Delta != "" {
			if r.FirstTokenMS == nil {
				ms := float64(now().Sub(start)) / float64(time.Millisecond)
				r.FirstTokenMS = &ms
			}
			answer.WriteString(e.Delta)
		}
		if e.Tool != nil && e.Tool.Finished {
			r.ToolCalls = append(r.ToolCalls, Call{Name: e.Tool.Name, Arguments: e.Tool.Arguments, Error: e.Tool.Err})
		}
		if e.Tool != nil && !e.Tool.Finished {
			answer.Reset()
		}
		if e.Done {
			r.Completed, r.Finish, r.Usage = true, e.Finish, e.Usage
			for _, call := range e.IgnoredToolCalls {
				r.ToolCalls = append(r.ToolCalls, Call{Name: call.Name, Arguments: call.Arguments, Ignored: true})
			}
		}
	}
	r.TotalMS = float64(now().Sub(start)) / float64(time.Millisecond)
	r.Answer = answer.String()
	judge(&r, c)
	return r
}

func judge(r *Result, c Case) {
	r.AnswerPassed = strings.TrimSpace(r.Answer) == c.Expected
	if c.ID == "structured-answer" {
		var value map[string]any
		r.AnswerPassed = json.Unmarshal([]byte(r.Answer), &value) == nil && len(value) == 1 && value["status"] == "ok"
	}
	r.ToolsPassed = len(r.ToolCalls) == 0 && c.Tool == ""
	if c.Tool != "" && len(r.ToolCalls) == 1 {
		call := r.ToolCalls[0]
		r.ToolsPassed = call.Name == c.Tool && call.Error == "" && !call.Ignored
	}
	r.Passed = r.Completed && r.Error == "" && r.AnswerPassed && r.ToolsPassed && (r.Finish == "" || r.Finish == provider.FinishStop)
}

func caseTool(c Case) session.Tool {
	key, value := "key", "color"
	if c.Tool == "request_write" {
		key, value = "content", "hello"
	}
	schema, _ := json.Marshal(map[string]any{
		"type": "object", "properties": map[string]any{key: map[string]string{"type": "string"}},
		"required": []string{key}, "additionalProperties": false,
	})
	return session.Tool{
		Definition: provider.Tool{Name: c.Tool, Description: "Evaluation fixture; no external side effects.", Parameters: schema},
		Run: func(ctx context.Context, arguments string) (string, error) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(arguments), &args); err != nil || len(args) != 1 || args[key] != value {
				return "", fmt.Errorf("invalid fixture arguments")
			}
			if c.Tool == "request_write" {
				return "denied: the user refused; nothing was written. Do not retry.", nil
			}
			return "amber", nil
		},
	}
}
