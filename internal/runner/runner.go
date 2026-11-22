package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/statvamasi/torch/internal/brief"
	"github.com/statvamasi/torch/internal/config"
	"github.com/statvamasi/torch/internal/evidence"
	"github.com/statvamasi/torch/internal/judge"
	"github.com/statvamasi/torch/internal/opencode"
	"github.com/statvamasi/torch/internal/plan"
)

// Action is what the supervisor decided to do next.
type Action string

const (
	ActionBrief    Action = "brief"    // first contact with a step
	ActionContinue Action = "continue" // same step, more work required
	ActionAdvance  Action = "advance"  // step done, next step starts
	ActionBlocked  Action = "blocked"  // needs a human
	ActionComplete Action = "complete" // plan finished
)

// Decision is one turn of the outer loop.
//
// A decision carries the mutated plan and state in memory. They are persisted
// only when the caller commits, which happens after an instruction is
// successfully admitted by OpenCode. A failed send therefore leaves the plan
// exactly where it was, ready to retry.
type Decision struct {
	Action       Action
	Instruction  string
	Step         *plan.Step
	ReviewedStep *plan.Step
	Position     int
	Verdict      *judge.Verdict
	Evidence     *evidence.Evidence
	SessionID    string

	plan  *plan.Plan
	state *State
}

// Commit persists the plan and state attached to a decision.
func (d *Decision) Commit(ws *Workspace) error {
	if d.plan == nil {
		return nil
	}
	if err := ws.SavePlan(d.plan); err != nil {
		return err
	}
	if d.state != nil {
		return ws.SaveState(d.state)
	}
	return nil
}

// AdvanceOptions controls one review/decision cycle.
type AdvanceOptions struct {
	// SessionID reviews a specific session instead of the remembered one.
	SessionID string
	// Model is an explicit judge model ("provider/model"). Empty means use the
	// project configuration, then the reviewed session's model.
	Model string
	// MaxRepeat blocks a step after this many identical instructions. Zero uses
	// the default.
	MaxRepeat int
}

// Advance reviews the previous session (if any) and decides what comes next.
// It is read-only on disk; call Decision.Commit to persist the outcome.
func Advance(ctx context.Context, ws *Workspace, client *opencode.Client,
	settings config.Settings, opts AdvanceOptions) (*Decision, error) {

	p, err := ws.LoadPlan()
	if err != nil {
		return nil, fmt.Errorf("load plan: %w", err)
	}
	if len(p.Steps) == 0 {
		return nil, fmt.Errorf("plan has no steps: add them to %s or re-run `torch init --from PLAN.md`", ws.PlanPath)
	}
	state, err := ws.LoadState()
	if err != nil {
		return nil, fmt.Errorf("load state: %w", err)
	}

	step := p.Next()
	if step == nil {
		if blocked := p.NeedsAttention(); blocked != nil {
			return &Decision{
				Action:       ActionBlocked,
				Step:         blocked,
				ReviewedStep: blocked,
				Position:     stepPosition(p, blocked.ID),
				plan:         p,
				state:        state,
			}, nil
		}
		return &Decision{Action: ActionComplete, plan: p, state: state}, nil
	}
	position := stepPosition(p, step.ID)

	// First contact: nothing has run for this step, so there is nothing to
	// judge. Start it and hand over a self-contained brief.
	if step.Attempts == 0 {
		started, err := p.Start(step.ID)
		if err != nil {
			return nil, err
		}
		state.LastStepID = started.ID
		return &Decision{
			Action:      ActionBrief,
			Instruction: brief.StepBrief(p, started, position, ""),
			Step:        started,
			Position:    position,
			plan:        p,
			state:       state,
		}, nil
	}

	target, err := ResolveSession(ctx, client, ws, state, opts.SessionID)
	if err != nil {
		return nil, err
	}
	if target == "" {
		return nil, fmt.Errorf(
			"no session to review: run `torch next --send` to start step %s, or pass --session ses_...",
			step.ID)
	}

	ev, err := evidence.Collect(ctx, ws.Root, client, target)
	if err != nil {
		return nil, err
	}
	state.ObserveCost(ev.Session)

	model, err := judge.ResolveModel(ctx, client, opts.Model, settings.Model, ev.Session.Model)
	if err != nil {
		return nil, fmt.Errorf("resolve judge model: %w", err)
	}
	verdict, err := judge.Judge(ctx, client, p, step, ev.Render(), model)
	if err != nil {
		return nil, err
	}

	decision := &Decision{
		Step:         step,
		ReviewedStep: step,
		Position:     position,
		Verdict:      verdict,
		Evidence:     ev,
		SessionID:    target,
		plan:         p,
		state:        state,
	}

	record := RunRecord{
		Time:       time.Now().UTC().Format(time.RFC3339),
		Step:       step.ID,
		Attempt:    step.Attempts,
		Session:    target,
		Outcome:    ev.Session.Outcome,
		CostUSD:    ev.Session.Cost,
		Tokens:     ev.Session.Tokens,
		Verdict:    verdict.Verdict,
		Confidence: verdict.Confidence,
		Evidence:   verdict.Evidence,
		Remaining:  verdict.Remaining,
		Notes:      verdict.Notes,
	}

	switch verdict.Verdict {
	case "done":
		if _, err := p.Finish(step.ID, verdict.Evidence, verdict.Notes); err != nil {
			return nil, err
		}
		next := p.Next()
		if next == nil {
			decision.Action = ActionComplete
			record.Action = string(ActionComplete)
		} else {
			nextPos := stepPosition(p, next.ID)
			started, err := p.Start(next.ID)
			if err != nil {
				return nil, err
			}
			state.LastStepID = started.ID
			decision.Action = ActionAdvance
			decision.Step = started
			decision.Position = nextPos
			decision.Instruction = brief.StepBrief(p, started, nextPos, verdict.Notes)
			record.Action = string(ActionAdvance)
		}

	case "blocked":
		reason := verdict.Notes
		if strings.TrimSpace(reason) == "" {
			reason = "blocked by reviewer"
		}
		if _, err := p.Block(step.ID, reason); err != nil {
			return nil, err
		}
		decision.Action = ActionBlocked
		record.Action = string(ActionBlocked)

	default: // partial or off_track
		instruction := strings.TrimSpace(verdict.NextInstruction)
		if instruction == "" {
			instruction = brief.Correction(p, step, verdict.Evidence, verdict.Remaining)
		}

		// Loop breaker: an identical instruction means the last attempt did not
		// move the needle. Repeating it forever is how budgets disappear.
		maxRepeat := opts.MaxRepeat
		if maxRepeat <= 0 {
			maxRepeat = 2
		}
		ref := instructionRef(step.ID, instruction)
		if state.LastInstructionRef == ref {
			state.Repeated++
		} else {
			state.LastInstructionRef = ref
			state.Repeated = 0
		}
		record.Instruction = ref

		if state.Repeated >= maxRepeat {
			reason := fmt.Sprintf(
				"no progress after %d identical instructions; needs a human decision", state.Repeated)
			if _, err := p.Block(step.ID, reason); err != nil {
				return nil, err
			}
			decision.Action = ActionBlocked
			decision.Verdict = verdict
			record.Action = string(ActionBlocked)
			record.Notes = reason
			break
		}

		decision.Action = ActionContinue
		decision.Instruction = instruction
		record.Action = string(ActionContinue)
	}

	// Audit logging is best-effort: losing one record must not lose the work,
	// and the operator still sees the decision on stdout.
	_, _ = ws.LogRun(record)
	return decision, nil
}

// DispatchOptions controls session selection and model choice for sending.
type DispatchOptions struct {
	SessionID string
	Agent     string
	Model     *opencode.ModelRef
}

// Dispatch delivers a decision to OpenCode and commits the decision. It creates
// a fresh session unless the decision continues work already in flight, which
// keeps per-step session isolation without needless context resets mid-step.
func Dispatch(ctx context.Context, ws *Workspace, client *opencode.Client,
	d *Decision, opts DispatchOptions) (string, error) {

	if strings.TrimSpace(d.Instruction) == "" || d.Step == nil || d.plan == nil {
		return "", nil
	}

	target := firstNonEmpty(opts.SessionID, d.SessionID)
	if d.Action != ActionContinue || target == "" {
		title := fmt.Sprintf("step %s: %s", d.Step.ID, d.Step.Title)
		session, err := client.CreateSession(ctx, ws.Root, clipTitle(title, 120), opts.Agent, opts.Model)
		if err != nil {
			return "", fmt.Errorf("create session: %w", err)
		}
		target = session.ID
	}

	if err := client.Prompt(ctx, target, d.Instruction); err != nil {
		return "", fmt.Errorf("send instruction to %s: %w", target, err)
	}
	if err := d.plan.AttachSession(d.Step.ID, target); err != nil {
		return "", err
	}

	if d.state == nil {
		d.state = &State{}
	}
	d.state.LastSessionID = target
	d.state.LastStepID = d.Step.ID
	d.state.Instructions++

	if err := d.Commit(ws); err != nil {
		return "", fmt.Errorf("commit after send: %w", err)
	}
	d.SessionID = target
	return target, nil
}

// RunHooks lets the CLI render progress without the runner owning output.
type RunHooks struct {
	OnDecision func(*Decision)
	OnSent     func(sessionID string)
	OnInfo     func(message string)
}

// RunOptions bounds the loop.
type RunOptions struct {
	Send       bool
	MaxSteps   int
	MaxMinutes float64
	MaxCost    float64
	Model      string
	Agent      string
	Poll       time.Duration
}

// Run drives the plan end to end. Without Send it performs exactly one
// read-only review and reports the decision.
func Run(ctx context.Context, ws *Workspace, client *opencode.Client,
	settings config.Settings, opts RunOptions, hooks RunHooks) error {

	if opts.MaxSteps <= 0 {
		opts.MaxSteps = settings.MaxSteps
	}
	if opts.MaxMinutes <= 0 {
		opts.MaxMinutes = settings.MaxMinutes
	}
	if opts.MaxCost <= 0 {
		opts.MaxCost = settings.MaxCost
	}
	if opts.Poll <= 0 {
		opts.Poll = 2 * time.Second
	}

	// Resolve the session model once. A bad reference is a configuration error,
	// not something to discover after the first paid review.
	var sendModel *opencode.ModelRef
	if strings.TrimSpace(settings.Model) != "" {
		resolved, err := client.ResolveModel(ctx, settings.Model)
		if err != nil {
			return fmt.Errorf("resolve model %q: %w", settings.Model, err)
		}
		sendModel = resolved
	}

	started := time.Now()
	lastSession := ""
	sent := 0

	for {
		if sent >= opts.MaxSteps {
			info(hooks, "stopped: reached max steps (%d)", opts.MaxSteps)
			return nil
		}
		if deadlineAfter(started, opts.MaxMinutes) <= 0 {
			info(hooks, "stopped: reached max minutes (%.0f)", opts.MaxMinutes)
			return nil
		}

		decision, err := Advance(ctx, ws, client, settings, AdvanceOptions{
			SessionID: lastSession,
			Model:     opts.Model,
		})
		if err != nil {
			return err
		}
		if hooks.OnDecision != nil {
			hooks.OnDecision(decision)
		}

		switch decision.Action {
		case ActionComplete, ActionBlocked:
			if opts.Send {
				if err := decision.Commit(ws); err != nil {
					return err
				}
			}
			return nil
		}
		if strings.TrimSpace(decision.Instruction) == "" {
			return nil
		}
		if !opts.Send {
			return nil
		}

		// Budget gate before spending more.
		if state, err := ws.LoadState(); err == nil && opts.MaxCost > 0 && state.TotalCost >= opts.MaxCost {
			info(hooks, "stopped: cost budget reached ($%.4f of $%.2f)", state.TotalCost, opts.MaxCost)
			return nil
		}

		sessionID, err := Dispatch(ctx, ws, client, decision, DispatchOptions{
			SessionID: lastSession,
			Agent:     opts.Agent,
			Model:     sendModel,
		})
		if err != nil {
			return err
		}
		sent++
		lastSession = sessionID
		if hooks.OnSent != nil {
			hooks.OnSent(sessionID)
		}

		waitCtx, cancel := context.WithTimeout(ctx, remainingDuration(started, opts.MaxMinutes))
		err = client.WaitIdle(waitCtx, sessionID, opts.Poll)
		cancel()
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				info(hooks, "stopped: the session exceeded the wall-clock budget")
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("wait for %s: %w", sessionID, err)
		}
	}
}

// ResolveSession chooses the session to review: explicit, remembered, or the
// newest session in this repository.
func ResolveSession(ctx context.Context, client *opencode.Client, ws *Workspace,
	state *State, explicit string) (string, error) {

	if explicit != "" {
		return explicit, nil
	}
	if state.LastSessionID != "" {
		session, err := client.GetSession(ctx, state.LastSessionID)
		if err == nil && (session.Location.Directory == "" || session.Location.Directory == ws.Root) {
			return state.LastSessionID, nil
		}
	}
	sessions, err := client.ListSessions(ctx, ws.Root, 25)
	if err != nil {
		return "", fmt.Errorf("list sessions: %w", err)
	}
	for _, s := range sessions {
		if s.Location.Directory == "" || s.Location.Directory == ws.Root {
			return s.ID, nil
		}
	}
	return "", nil
}

// ActiveSessionAt returns a running session in this repository, if any.
func ActiveSessionAt(ctx context.Context, client *opencode.Client, root string) (string, error) {
	active, err := client.ActiveSessions(ctx)
	if err != nil {
		return "", err
	}
	for id := range active {
		session, err := client.GetSession(ctx, id)
		if err != nil {
			continue
		}
		if session.Location.Directory == root {
			return id, nil
		}
	}
	return "", nil
}

func stepPosition(p *plan.Plan, id string) int {
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			return i + 1
		}
	}
	return 0
}

func instructionRef(stepID, instruction string) string {
	sum := sha256.Sum256([]byte(stepID + "\x00" + strings.TrimSpace(instruction)))
	return hex.EncodeToString(sum[:8])
}

func clipTitle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func info(hooks RunHooks, format string, args ...any) {
	if hooks.OnInfo != nil {
		hooks.OnInfo(fmt.Sprintf(format, args...))
	}
}

func deadlineAfter(start time.Time, minutes float64) time.Duration {
	if minutes <= 0 {
		return 24 * time.Hour
	}
	return time.Until(start.Add(time.Duration(minutes * float64(time.Minute))))
}

func remainingDuration(start time.Time, minutes float64) time.Duration {
	d := deadlineAfter(start, minutes)
	if d <= 0 {
		return time.Second
	}
	return d
}
