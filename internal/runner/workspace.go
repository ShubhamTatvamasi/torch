// Package runner is the control loop: decide, instruct, dispatch, wait, repeat.
package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ShubhamTatvamasi/torch/internal/fsutil"
	"github.com/ShubhamTatvamasi/torch/internal/opencode"
	"github.com/ShubhamTatvamasi/torch/internal/plan"
)

// State is the supervisor's checkpoint. OpenCode owns the conversation; this
// file owns where the plan loop is.
type State struct {
	LastSessionID      string             `json:"last_session_id,omitempty"`
	LastStepID         string             `json:"last_step_id,omitempty"`
	Updated            string             `json:"updated,omitempty"`
	LastInstructionRef string             `json:"last_instruction_ref,omitempty"`
	Repeated           int                `json:"repeated_instructions,omitempty"`
	SessionCosts       map[string]float64 `json:"session_costs,omitempty"`
	TotalCost          float64            `json:"total_cost_usd,omitempty"`
	Instructions       int                `json:"instructions_sent,omitempty"`
}

// Workspace is the on-disk layout under <repo>/.torch.
type Workspace struct {
	Root         string
	Dir          string
	PlanPath     string
	StatePath    string
	ConfigPath   string
	ProgressPath string
	RunsPath     string
}

// NewWorkspace resolves the layout for a repository root.
func NewWorkspace(root string) (*Workspace, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(abs, ".torch")
	return &Workspace{
		Root:         abs,
		Dir:          dir,
		PlanPath:     filepath.Join(dir, "plan.json"),
		StatePath:    filepath.Join(dir, "state.json"),
		ConfigPath:   filepath.Join(dir, "config.json"),
		ProgressPath: filepath.Join(dir, "PROGRESS.md"),
		RunsPath:     filepath.Join(dir, "runs"),
	}, nil
}

// Exists reports whether a plan has been initialised.
func (w *Workspace) Exists() bool {
	_, err := os.Stat(w.PlanPath)
	return err == nil
}

// LoadPlan reads the plan.
func (w *Workspace) LoadPlan() (*plan.Plan, error) { return plan.Load(w.PlanPath) }

// SavePlan writes the plan atomically.
func (w *Workspace) SavePlan(p *plan.Plan) error { return p.Save(w.PlanPath) }

// LoadState reads the runner checkpoint, returning an empty state when none
// exists yet.
func (w *Workspace) LoadState() (*State, error) {
	data, err := os.ReadFile(w.StatePath)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse %s: %w", w.StatePath, err)
	}
	if s.SessionCosts == nil {
		s.SessionCosts = map[string]float64{}
	}
	return &s, nil
}

// SaveState writes the checkpoint atomically.
func (w *Workspace) SaveState(s *State) error {
	s.Updated = time.Now().UTC().Format(time.RFC3339)
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteAtomic(w.StatePath, append(data, '\n'), 0o644)
}

// ObserveCost adds the cost delta for a session. Session cost is cumulative, so
// re-reading the same session must not double-count it.
func (s *State) ObserveCost(session opencode.Session) {
	if s.SessionCosts == nil {
		s.SessionCosts = map[string]float64{}
	}
	previous := s.SessionCosts[session.ID]
	if session.Cost > previous {
		s.TotalCost += session.Cost - previous
		s.SessionCosts[session.ID] = session.Cost
	}
}

// RunRecord is one auditable checkpoint, written to .torch/runs.
type RunRecord struct {
	Time        string          `json:"time"`
	Step        string          `json:"step"`
	Attempt     int             `json:"attempt"`
	Action      string          `json:"action"`
	Session     string          `json:"session"`
	Outcome     string          `json:"outcome,omitempty"`
	CostUSD     float64         `json:"cost_usd"`
	Tokens      opencode.Tokens `json:"tokens"`
	Instruction string          `json:"instruction_hash,omitempty"`
	Verdict     string          `json:"verdict,omitempty"`
	Confidence  float64         `json:"confidence,omitempty"`
	Evidence    []string        `json:"evidence,omitempty"`
	Remaining   []string        `json:"remaining,omitempty"`
	Notes       string          `json:"notes,omitempty"`
}

// LogRun appends a run record. Logging is intentionally best-effort: an audit
// failure must not break the loop, but it is reported to the caller.
func (w *Workspace) LogRun(record RunRecord) (string, error) {
	if err := os.MkdirAll(w.RunsPath, 0o755); err != nil {
		return "", err
	}
	name := plan.NewRunID() + "-" + record.Step + ".json"
	path := filepath.Join(w.RunsPath, name)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", err
	}
	return path, fsutil.WriteAtomic(path, append(data, '\n'), 0o644)
}
