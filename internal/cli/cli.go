// Package cli implements the torch command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ShubhamTatvamasi/torch/internal/config"
	"github.com/ShubhamTatvamasi/torch/internal/opencode"
	"github.com/ShubhamTatvamasi/torch/internal/plan"
	"github.com/ShubhamTatvamasi/torch/internal/runner"
)

// Version is overridden at build time with -ldflags "-X ...cli.Version=...".
var Version = "0.1.0"

type app struct {
	ctx  context.Context
	root string
	ui   *ui
}

// Run dispatches a command line.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	root, rest := extractDir(args)
	a := &app{ctx: ctx, root: root, ui: newUI(stdout, stderr)}

	if len(rest) == 0 {
		a.usage()
		return nil
	}
	cmd, rest := rest[0], rest[1:]
	switch cmd {
	case "init":
		return a.init(rest)
	case "status":
		return a.status(rest)
	case "unblock":
		return a.unblock(rest)
	case "next":
		return a.next(rest)
	case "watch":
		return a.watch(rest)
	case "run":
		return a.run(rest)
	case "doctor":
		return a.doctor(rest)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "torch %s\n", Version)
		return nil
	case "help", "--help", "-h":
		a.usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (try `torch help`)", cmd)
	}
}

func extractDir(args []string) (string, []string) {
	root := "."
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-C" || a == "--dir":
			if i+1 < len(args) {
				root = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--dir="):
			root = strings.TrimPrefix(a, "--dir=")
		case strings.HasPrefix(a, "-C="):
			root = strings.TrimPrefix(a, "-C=")
		default:
			out = append(out, a)
		}
	}
	return root, out
}

func (a *app) usage() {
	a.ui.printf(`%s — a plan-driven session supervisor for OpenCode

Usage:
  torch [-C dir] <command> [flags]

Commands:
  init      Create .torch/plan.json, optionally importing a Markdown plan
  status    Show plan progress and the last session
  unblock   Put a blocked step back into the queue
  next      Review the last session and print the next instruction
  watch     Wait for the current session to finish, then review it
  run       Drive the plan end to end (dry run unless --send)
  doctor    Check the OpenCode service, auth, model, and plan
  version   Print the version

Core flags:
  -C, --dir <path>     Repository the plan applies to (default: cwd)
      --send           Actually create sessions and send instructions
      --session <id>   Review a specific session
      --model <ref>    Judge model, e.g. gpustack/deepseek-v4.1-flash-1017
      --agent <name>   OpenCode agent for new sessions

Examples:
  torch init --goal "Ship the items service" --from PLAN.md
  torch next
  torch watch --send
  torch run --send --max-steps 10
`, a.ui.bold("torch"))
}

func (a *app) workspace() (*runner.Workspace, error) {
	ws, err := runner.NewWorkspace(a.root)
	if err != nil {
		return nil, err
	}
	if !ws.Exists() {
		return nil, fmt.Errorf("no plan at %s\nrun `torch init --goal \"...\" --from PLAN.md` first", ws.PlanPath)
	}
	return ws, nil
}

func (a *app) connect() (*opencode.Client, config.Settings, error) {
	settings, err := config.Load(a.root)
	if err != nil {
		return nil, config.Settings{}, err
	}
	client, err := opencode.New(settings)
	if err != nil {
		return nil, config.Settings{}, err
	}
	return client, settings, nil
}

// -- init -------------------------------------------------------------------

func (a *app) init(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(a.ui.err)
	goal := fs.String("goal", "", "one-sentence project goal")
	fs.StringVar(goal, "g", "", "one-sentence project goal")
	from := fs.String("from", "", "markdown file with '- [ ]' steps")
	fs.StringVar(from, "f", "", "markdown file with '- [ ]' steps")
	force := fs.Bool("force", false, "overwrite an existing plan")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ws, err := runner.NewWorkspace(a.root)
	if err != nil {
		return err
	}
	if ws.Exists() && !*force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", ws.PlanPath)
	}
	if strings.TrimSpace(*goal) == "" {
		return fmt.Errorf("--goal is required")
	}

	var p *plan.Plan
	if *from != "" {
		path := *from
		if !filepath.IsAbs(path) {
			path = filepath.Join(ws.Root, path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		p, err = plan.FromMarkdown(string(data), *goal, ws.Root)
		if err != nil {
			return err
		}
	} else {
		p = plan.New(*goal, ws.Root)
	}
	if err := ws.SavePlan(p); err != nil {
		return err
	}
	if err := ensureProgress(ws, p); err != nil {
		return err
	}

	a.ui.printf("%s %s (%d steps)\n", a.ui.green("Created"), ws.PlanPath, len(p.Steps))
	if len(p.Steps) == 0 {
		a.ui.printf("%s\n", a.ui.yellow("No steps yet. Add them to the JSON, or re-run with --from PLAN.md."))
	}
	a.ui.printf("\nNext:\n  torch doctor\n  torch next\n  torch next --send\n")
	return nil
}

func ensureProgress(ws *runner.Workspace, p *plan.Plan) error {
	if _, err := os.Stat(ws.ProgressPath); err == nil {
		return nil
	}
	body := fmt.Sprintf(`# Progress log

Goal: %s

Each session appends what changed, what was verified, and what is still open.
Keep this file short; it is part of the next session's context.

## Log

`, p.Goal)
	if err := os.MkdirAll(filepath.Dir(ws.ProgressPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(ws.ProgressPath, []byte(body), 0o644)
}

// -- status -----------------------------------------------------------------

func (a *app) status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(a.ui.err)
	asJSON := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ws, err := a.workspace()
	if err != nil {
		return err
	}
	p, err := ws.LoadPlan()
	if err != nil {
		return err
	}
	state, err := ws.LoadState()
	if err != nil {
		return err
	}

	if *asJSON {
		payload := map[string]any{
			"goal":     p.Goal,
			"repo":     p.Repo,
			"steps":    p.Steps,
			"counts":   p.Counts(),
			"state":    state,
			"finished": p.Finished(),
		}
		enc := json.NewEncoder(a.ui.out)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}

	a.ui.printf("%s %s\n", a.ui.bold("Goal:"), p.Goal)
	a.ui.printf("%s %s\n", a.ui.bold("Repo:"), p.Repo)
	a.ui.printf("%s %s\n\n", a.ui.bold("Progress:"), progressBar(p))

	w := tabwriter.NewWriter(a.ui.out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTEP\tSTATUS\tTRIES\tSESSIONS")
	for _, step := range p.Steps {
		title := step.Title
		if len([]rune(title)) > 56 {
			title = string([]rune(title)[:55]) + "…"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\n",
			step.ID, title, statusLabel(step.Status), step.Attempts, len(step.SessionIDs))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	a.ui.printf("\n")
	if state.LastSessionID != "" {
		a.ui.printf("%s %s (step %s)\n", a.ui.dim("last session:"), state.LastSessionID, state.LastStepID)
	}
	if state.Instructions > 0 || state.TotalCost > 0 {
		a.ui.printf("%s %d sent · %s\n", a.ui.dim("totals:"), state.Instructions,
			a.ui.dim(fmt.Sprintf("$%.4f", state.TotalCost)))
	}
	for _, step := range p.Steps {
		if step.Status == plan.Blocked && strings.TrimSpace(step.Notes) != "" {
			a.ui.printf("%s step %s: %s\n", a.ui.red("blocked"), step.ID, step.Notes)
		}
	}
	return nil
}

func statusLabel(s plan.Status) string {
	switch s {
	case plan.Done:
		return "done"
	case plan.InProgress:
		return "in_progress"
	case plan.Blocked:
		return "blocked"
	case plan.Skipped:
		return "skipped"
	default:
		return "pending"
	}
}

func progressBar(p *plan.Plan) string {
	total := len(p.Steps)
	if total == 0 {
		return "(no steps)"
	}
	counts := p.Counts()
	complete := counts[plan.Done] + counts[plan.Skipped]
	const width = 24
	filled := int(float64(complete) / float64(total) * width)
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("%s %d/%d  (%d done, %d in progress, %d pending, %d blocked)",
		bar, complete, total,
		counts[plan.Done], counts[plan.InProgress], counts[plan.Pending], counts[plan.Blocked])
}

// -- unblock ----------------------------------------------------------------

func (a *app) unblock(args []string) error {
	fs := flag.NewFlagSet("unblock", flag.ContinueOnError)
	fs.SetOutput(a.ui.err)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: torch unblock <step-id>")
	}
	id := fs.Arg(0)

	ws, err := a.workspace()
	if err != nil {
		return err
	}
	p, err := ws.LoadPlan()
	if err != nil {
		return err
	}
	step, err := p.Step(id)
	if err != nil {
		return err
	}
	if step.Status != plan.Blocked {
		return fmt.Errorf("step %s is %s, not blocked", id, step.Status)
	}
	if _, err := p.Reopen(id); err != nil {
		return err
	}
	if err := ws.SavePlan(p); err != nil {
		return err
	}

	state, err := ws.LoadState()
	if err != nil {
		return err
	}
	if state.LastStepID == id {
		state.LastInstructionRef = ""
		state.Repeated = 0
		if err := ws.SaveState(state); err != nil {
			return err
		}
	}

	a.ui.printf("%s step %s is pending again (%s)\n", a.ui.green("Reopened"), id, step.Title)
	a.ui.printf("%s\n", a.ui.dim("Run `torch next --send` for a fresh brief."))
	return nil
}

// -- next -------------------------------------------------------------------
func (a *app) next(args []string) error {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	fs.SetOutput(a.ui.err)
	session := fs.String("session", "", "session to review")
	fs.StringVar(session, "s", "", "session to review")
	send := fs.Bool("send", false, "deliver the instruction to OpenCode")
	model := fs.String("model", "", "judge model (provider/model)")
	agent := fs.String("agent", "", "agent for a new session")
	asJSON := fs.Bool("json", false, "print the decision as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ws, err := a.workspace()
	if err != nil {
		return err
	}
	client, settings, err := a.connect()
	if err != nil {
		return err
	}

	decision, err := runner.Advance(a.ctx, ws, client, settings, runner.AdvanceOptions{
		SessionID: *session,
		Model:     *model,
	})
	if err != nil {
		return err
	}

	var sentTo string
	if *send {
		sendModel, err := a.sessionModel(client, settings)
		if err != nil {
			return err
		}
		sentTo, err = runner.Dispatch(a.ctx, ws, client, decision, runner.DispatchOptions{
			SessionID: *session,
			Agent:     *agent,
			Model:     sendModel,
		})
		if err != nil {
			return err
		}
		if sentTo == "" {
			if err := decision.Commit(ws); err != nil {
				return err
			}
		}
	}

	if *asJSON {
		payload := map[string]any{
			"action":      decision.Action,
			"step":        stepID(decision.Step),
			"position":    decision.Position,
			"instruction": decision.Instruction,
			"session":     sentTo,
			"verdict":     decision.Verdict,
		}
		enc := json.NewEncoder(a.ui.out)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}
	a.ui.decision(decision, sentTo, *send)
	return nil
}

// -- watch ------------------------------------------------------------------

func (a *app) watch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(a.ui.err)
	session := fs.String("session", "", "session to wait for")
	fs.StringVar(session, "s", "", "session to wait for")
	send := fs.Bool("send", false, "deliver the instruction when the session finishes")
	model := fs.String("model", "", "judge model (provider/model)")
	agent := fs.String("agent", "", "agent for a new session")
	interval := fs.Duration("interval", 2*time.Second, "polling interval")
	maxMinutes := fs.Float64("max-minutes", 0, "maximum time to wait (0 = configured)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ws, err := a.workspace()
	if err != nil {
		return err
	}
	client, settings, err := a.connect()
	if err != nil {
		return err
	}
	state, err := ws.LoadState()
	if err != nil {
		return err
	}

	target := *session
	if target == "" {
		// Prefer the session that is actually running in this repository. The
		// remembered session is a fallback, not a lock: you may have started a
		// new session by hand.
		target, err = runner.ActiveSessionAt(a.ctx, client, ws.Root)
		if err != nil {
			return err
		}
	}
	if target == "" {
		target, err = runner.ResolveSession(a.ctx, client, ws, state, "")
		if err != nil {
			return err
		}
	}

	minutes := *maxMinutes
	if minutes <= 0 {
		minutes = settings.MaxMinutes
	}
	if target != "" {
		active, aerr := client.ActiveSessions(a.ctx)
		if aerr == nil && active[target] {
			a.ui.printf("%s %s\n", a.ui.dim("Waiting for"), a.ui.bold(target))
			waitCtx, cancel := context.WithTimeout(a.ctx, time.Duration(minutes*float64(time.Minute)))
			err = client.WaitIdle(waitCtx, target, *interval)
			cancel()
			if err != nil {
				if errors.Is(err, context.DeadlineExceeded) {
					return fmt.Errorf("session %s is still running after %.0f minutes", target, minutes)
				}
				return err
			}
			a.ui.printf("%s\n", a.ui.green("Session finished."))
		}
	}

	decision, err := runner.Advance(a.ctx, ws, client, settings, runner.AdvanceOptions{
		SessionID: target,
		Model:     *model,
	})
	if err != nil {
		return err
	}

	var sentTo string
	if *send {
		sendModel, err := a.sessionModel(client, settings)
		if err != nil {
			return err
		}
		sentTo, err = runner.Dispatch(a.ctx, ws, client, decision, runner.DispatchOptions{
			SessionID: target,
			Agent:     *agent,
			Model:     sendModel,
		})
		if err != nil {
			return err
		}
		if sentTo == "" {
			if err := decision.Commit(ws); err != nil {
				return err
			}
		}
	}
	a.ui.decision(decision, sentTo, *send)
	return nil
}

// -- run --------------------------------------------------------------------

func (a *app) run(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(a.ui.err)
	send := fs.Bool("send", false, "actually drive sessions")
	model := fs.String("model", "", "judge model (provider/model)")
	agent := fs.String("agent", "", "agent for new sessions")
	maxSteps := fs.Int("max-steps", 0, "maximum instructions to send")
	maxMinutes := fs.Float64("max-minutes", 0, "maximum wall-clock minutes")
	maxCost := fs.Float64("max-cost", 0, "maximum total spend in USD")
	interval := fs.Duration("interval", 2*time.Second, "polling interval")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ws, err := a.workspace()
	if err != nil {
		return err
	}
	client, settings, err := a.connect()
	if err != nil {
		return err
	}
	if !*send {
		a.ui.printf("%s\n\n", a.ui.yellow("Dry run. Add --send to let the supervisor drive sessions."))
	}
	if *maxSteps == 0 {
		*maxSteps = settings.MaxSteps
	}
	if *maxMinutes == 0 {
		*maxMinutes = settings.MaxMinutes
	}

	step := 0
	hooks := runner.RunHooks{
		OnDecision: func(d *runner.Decision) {
			step++
			if step > 1 {
				a.ui.printf("\n%s\n", a.ui.dim(strings.Repeat("═", 64)))
			}
			a.ui.printf("%s\n", a.ui.dim(fmt.Sprintf("[turn %d/%d]", step, *maxSteps)))
			a.ui.decision(d, "", *send)
		},
		OnSent: func(sessionID string) {
			a.ui.printf("%s %s %s\n", a.ui.green("Sent →"), a.ui.bold(sessionID),
				a.ui.dim("waiting for it to go idle…"))
		},
		OnInfo: func(message string) {
			a.ui.printf("%s\n", a.ui.yellow(message))
		},
	}

	err = runner.Run(a.ctx, ws, client, settings, runner.RunOptions{
		Send:       *send,
		MaxSteps:   *maxSteps,
		MaxMinutes: *maxMinutes,
		MaxCost:    *maxCost,
		Model:      *model,
		Agent:      *agent,
		Poll:       *interval,
	}, hooks)
	if errors.Is(err, context.Canceled) {
		a.ui.printf("\n%s\n", a.ui.yellow("Interrupted. State is saved; run `torch status` to resume."))
		return nil
	}
	return err
}

// -- doctor -----------------------------------------------------------------

func (a *app) doctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(a.ui.err)
	if err := fs.Parse(args); err != nil {
		return err
	}

	fail := false
	check := func(ok bool, format string, args ...any) {
		label := a.ui.green("✓")
		if !ok {
			label = a.ui.red("✗")
			fail = true
		}
		a.ui.printf("%s %s\n", label, fmt.Sprintf(format, args...))
	}

	client, settings, err := a.connect()
	if err != nil {
		a.ui.printf("%s %v\n", a.ui.red("✗"), err)
		return err
	}
	info, err := client.Info(a.ctx)
	check(err == nil, "service reachable at %s", client.URL())
	if err == nil {
		a.ui.printf("  %s\n", a.ui.dim(fmt.Sprintf("OpenCode %s · pid %d", info.Version, info.PID)))
	}

	var agents []opencode.Agent
	if err == nil {
		agents, err = client.Agents(a.ctx)
		check(err == nil, "agents listed (%d)", len(agents))
	}
	if err == nil {
		var ids []string
		for _, ag := range agents {
			if !ag.Hidden {
				ids = append(ids, ag.ID)
			}
		}
		a.ui.printf("  %s\n", a.ui.dim(strings.Join(ids, ", ")))
	}

	if settings.Model != "" {
		ref, merr := client.ResolveModel(a.ctx, settings.Model)
		check(merr == nil, "judge model resolves: %s", settings.Model)
		if merr == nil && ref != nil {
			a.ui.printf("  %s\n", a.ui.dim(ref.String()))
		}
	} else {
		a.ui.printf("%s no judge model pinned; using the reviewed session's model\n", a.ui.dim("•"))
	}

	ws, werr := runner.NewWorkspace(a.root)
	if werr == nil {
		if ws.Exists() {
			p, perr := ws.LoadPlan()
			check(perr == nil, "plan loads: %s", ws.PlanPath)
			if perr == nil {
				a.ui.printf("  %s\n", a.ui.dim(fmt.Sprintf("%d steps · %s", len(p.Steps), progressBar(p))))
			}
		} else {
			a.ui.printf("%s no plan yet (%s)\n", a.ui.dim("•"), ws.PlanPath)
		}
	}

	gitOK := exec.CommandContext(a.ctx, "git", "-C", a.root, "rev-parse", "--is-inside-work-tree").
		Run() == nil
	check(gitOK, "repository is a git work tree")

	if active, aerr := client.ActiveSessions(a.ctx); aerr == nil {
		a.ui.printf("%s %d\n", a.ui.dim("• active sessions:"), len(active))
	}

	if fail {
		return fmt.Errorf("doctor found problems")
	}
	return nil
}

func (a *app) sessionModel(client *opencode.Client, settings config.Settings) (*opencode.ModelRef, error) {
	if strings.TrimSpace(settings.Model) == "" {
		return nil, nil
	}
	ref, err := client.ResolveModel(a.ctx, settings.Model)
	if err != nil {
		return nil, fmt.Errorf("resolve model %q: %w", settings.Model, err)
	}
	return ref, nil
}

func stepID(s *plan.Step) string {
	if s == nil {
		return ""
	}
	return s.ID
}
