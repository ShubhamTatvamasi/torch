// Package judge performs the one structured model call per checkpoint.
//
// The judge is deliberately narrow: given a step's acceptance criteria and the
// evidence, decide whether it is done and, if not, write the next instruction.
// It never touches the repository.
package judge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/statvamasi/torch/internal/brief"
	"github.com/statvamasi/torch/internal/opencode"
	"github.com/statvamasi/torch/internal/plan"
)

// Verdict is the judge's structured answer.
type Verdict struct {
	Verdict         string   `json:"verdict"`
	Confidence      float64  `json:"confidence"`
	Evidence        []string `json:"evidence"`
	Remaining       []string `json:"remaining"`
	NextInstruction string   `json:"next_instruction"`
	Notes           string   `json:"notes"`
}

// Valid reports whether the verdict is one of the known outcomes.
func (v *Verdict) Valid() bool {
	switch v.Verdict {
	case "done", "partial", "blocked", "off_track":
		return true
	}
	return false
}

// Validate enforces the contract the runner relies on.
func (v *Verdict) Validate() error {
	if !v.Valid() {
		return fmt.Errorf(`verdict must be "done", "partial", "blocked", or "off_track"`)
	}
	if v.Verdict == "done" && len(v.Evidence) == 0 {
		return fmt.Errorf("a done verdict must cite at least one piece of evidence")
	}
	if v.Verdict != "done" && strings.TrimSpace(v.NextInstruction) == "" {
		return fmt.Errorf("next_instruction is required unless the verdict is done")
	}
	if v.Confidence < 0 {
		v.Confidence = 0
	}
	if v.Confidence > 1 {
		v.Confidence = 1
	}
	return nil
}

// Generator is the one method the judge needs. It is an interface so the retry
// path can be tested without a server.
type Generator interface {
	GenerateText(ctx context.Context, prompt string, model *opencode.ModelRef) (string, error)
}

// Error carries the raw model output when parsing fails twice.
type Error struct {
	Message string
	Raw     string
}

func (e *Error) Error() string { return e.Message }

// ParseVerdict accepts a bare JSON object or one wrapped in a markdown fence.
// Models are inconsistent about this; tolerate both rather than fail the run.
func ParseVerdict(raw string) (*Verdict, error) {
	text := strings.TrimSpace(raw)
	if fenced := extractFence(text); fenced != "" {
		text = fenced
	} else if !strings.HasPrefix(text, "{") {
		start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
		if start < 0 || end <= start {
			return nil, fmt.Errorf("no JSON object in response")
		}
		text = text[start : end+1]
	}
	var v Verdict
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	if err := v.Validate(); err != nil {
		return nil, err
	}
	return &v, nil
}

func extractFence(text string) string {
	const open = "```"
	i := strings.Index(text, open)
	if i < 0 {
		return ""
	}
	rest := text[i+len(open):]
	// Skip an optional language tag on the same line.
	if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
		rest = rest[nl+1:]
	} else {
		return ""
	}
	if j := strings.Index(rest, open); j >= 0 {
		return strings.TrimSpace(rest[:j])
	}
	return ""
}

// Judge asks the model for a verdict, retrying once with the parse failure fed
// back. A second failure is surfaced to the operator with the raw text.
func Judge(ctx context.Context, gen Generator, p *plan.Plan, step *plan.Step,
	evidence string, model *opencode.ModelRef) (*Verdict, error) {

	prompt := brief.ReviewRequest(p, step, evidence, step.Attempts)
	raw, err := gen.GenerateText(ctx, prompt, model)
	if err != nil {
		return nil, fmt.Errorf("judge model call failed: %w", err)
	}
	verdict, parseErr := ParseVerdict(raw)
	if parseErr == nil {
		return verdict, nil
	}

	raw2, err := gen.GenerateText(ctx, brief.RepairRequest(prompt, parseErr.Error()), model)
	if err != nil {
		return nil, fmt.Errorf("judge retry failed: %w", err)
	}
	verdict, parseErr = ParseVerdict(raw2)
	if parseErr == nil {
		return verdict, nil
	}
	return nil, &Error{
		Message: fmt.Sprintf("judge returned invalid output twice: %v", parseErr),
		Raw:     raw2,
	}
}

// ResolveModel picks the judge model: an explicit choice, then the project's
// configured model, then the model the reviewed session used. The last option
// keeps zero-config runs working.
func ResolveModel(ctx context.Context, client *opencode.Client, explicit, configured string,
	fallback *opencode.ModelRef) (*opencode.ModelRef, error) {

	if ref := strings.TrimSpace(explicit); ref != "" {
		return client.ResolveModel(ctx, ref)
	}
	if ref := strings.TrimSpace(configured); ref != "" {
		return client.ResolveModel(ctx, ref)
	}
	if fallback != nil && !fallback.IsZero() {
		return fallback, nil
	}
	return nil, nil
}

// IsParseError reports whether err came from invalid judge output.
func IsParseError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}
