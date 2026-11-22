// Package plan defines the canonical, machine-readable description of the work.
//
// The plan is the source of truth. Markdown is only an import format: models are
// measurably less likely to rewrite a JSON file than a prose file, so JSON wins
// once the plan exists.
package plan

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/statvamasi/torch/internal/fsutil"
)

// Version is the schema version written into new plans.
const Version = 1

// Status is the lifecycle state of a step.
type Status string

const (
	Pending    Status = "pending"
	InProgress Status = "in_progress"
	Done       Status = "done"
	Blocked    Status = "blocked"
	Skipped    Status = "skipped"
)

// Valid reports whether s is a known status.
func (s Status) Valid() bool {
	switch s {
	case Pending, InProgress, Done, Blocked, Skipped:
		return true
	}
	return false
}

// Terminal reports whether no further work is expected for this status.
func (s Status) Terminal() bool { return s == Done || s == Skipped }

// Step is one unit of work. A step should fit in a single session.
type Step struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Goal       string   `json:"goal,omitempty"`
	Acceptance []string `json:"acceptance,omitempty"`
	Status     Status   `json:"status"`
	Evidence   []string `json:"evidence,omitempty"`
	Notes      string   `json:"notes,omitempty"`
	SessionIDs []string `json:"session_ids,omitempty"`
	Attempts   int      `json:"attempts"`
}

// Plan is the whole project definition.
type Plan struct {
	Version int    `json:"version"`
	Goal    string `json:"goal"`
	Repo    string `json:"repo"`
	Created string `json:"created"`
	Steps   []Step `json:"steps"`
}

// New returns an empty plan rooted at repo.
func New(goal, repo string) *Plan {
	return &Plan{
		Version: Version,
		Goal:    strings.TrimSpace(goal),
		Repo:    repo,
		Created: time.Now().UTC().Format(time.RFC3339),
		Steps:   []Step{},
	}
}

// Load reads and validates a plan file.
func Load(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if p.Version == 0 {
		p.Version = Version
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &p, nil
}

// Validate checks the invariants the runner relies on.
func (p *Plan) Validate() error {
	if strings.TrimSpace(p.Goal) == "" {
		return fmt.Errorf("plan has no goal")
	}
	seen := make(map[string]bool, len(p.Steps))
	for i := range p.Steps {
		s := &p.Steps[i]
		s.ID = strings.TrimSpace(s.ID)
		s.Title = strings.TrimSpace(s.Title)
		if s.ID == "" {
			s.ID = fmt.Sprintf("%02d", i+1)
		}
		if s.Title == "" {
			return fmt.Errorf("step %q has no title", s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("duplicate step id %q", s.ID)
		}
		seen[s.ID] = true
		if s.Status == "" {
			s.Status = Pending
		}
		if !s.Status.Valid() {
			return fmt.Errorf("step %q has unknown status %q", s.ID, s.Status)
		}
		if s.Attempts < 0 {
			s.Attempts = 0
		}
	}
	return nil
}

// Save writes the plan atomically: a temp file in the same directory, then a
// rename. A crash leaves either the old plan or the new one, never half of one.
func (p *Plan) Save(path string) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return fsutil.WriteAtomic(path, data, 0o644)
}

// Step returns the step with the given id.
func (p *Plan) Step(id string) (*Step, error) {
	for i := range p.Steps {
		if p.Steps[i].ID == id {
			return &p.Steps[i], nil
		}
	}
	return nil, fmt.Errorf("unknown step %q", id)
}

// Next returns the first step that can be worked on, or nil when none can.
// Blocked steps are deliberately excluded: they need a human decision, not
// another automated attempt.
func (p *Plan) Next() *Step {
	for i := range p.Steps {
		switch p.Steps[i].Status {
		case "", Pending, InProgress:
			return &p.Steps[i]
		}
	}
	return nil
}

// NeedsAttention returns the first blocked step, if any.
func (p *Plan) NeedsAttention() *Step {
	for i := range p.Steps {
		if p.Steps[i].Status == Blocked {
			return &p.Steps[i]
		}
	}
	return nil
}

// Finished reports whether every step is done or skipped.
func (p *Plan) Finished() bool {
	for i := range p.Steps {
		if !p.Steps[i].Status.Terminal() {
			return false
		}
	}
	return true
}

// Counts returns the number of steps per status.
func (p *Plan) Counts() map[Status]int {
	out := make(map[Status]int, 5)
	for _, s := range p.Steps {
		out[s.Status]++
	}
	return out
}

// Start marks a step in progress and counts the attempt.
func (p *Plan) Start(id string) (*Step, error) {
	s, err := p.Step(id)
	if err != nil {
		return nil, err
	}
	s.Status = InProgress
	s.Attempts++
	return s, nil
}

// Finish marks a step done and records the evidence that justified it.
func (p *Plan) Finish(id string, evidence []string, notes string) (*Step, error) {
	s, err := p.Step(id)
	if err != nil {
		return nil, err
	}
	s.Status = Done
	if len(evidence) > 0 {
		s.Evidence = evidence
	}
	if notes != "" {
		s.Notes = notes
	}
	return s, nil
}

// Block marks a step blocked and records why.
func (p *Plan) Block(id, reason string) (*Step, error) {
	s, err := p.Step(id)
	if err != nil {
		return nil, err
	}
	s.Status = Blocked
	s.Notes = reason
	return s, nil
}

// Skip marks a step skipped.
func (p *Plan) Skip(id, reason string) (*Step, error) {
	s, err := p.Step(id)
	if err != nil {
		return nil, err
	}
	s.Status = Skipped
	if reason != "" {
		s.Notes = reason
	}
	return s, nil
}

// Reopen puts a blocked step back into the queue with a fresh attempt counter,
// so the next turn starts from a clean brief instead of re-reviewing the
// session that got stuck.
func (p *Plan) Reopen(id string) (*Step, error) {
	s, err := p.Step(id)
	if err != nil {
		return nil, err
	}
	s.Status = Pending
	s.Attempts = 0
	return s, nil
}

// AttachSession records that a session worked on a step.
func (p *Plan) AttachSession(id, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	s, err := p.Step(id)
	if err != nil {
		return err
	}
	for _, existing := range s.SessionIDs {
		if existing == sessionID {
			return nil
		}
	}
	s.SessionIDs = append(s.SessionIDs, sessionID)
	return nil
}

var (
	headingRE = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	boxRE     = regexp.MustCompile(`^\s*[-*]\s*\[([ xX])\]\s*(.+?)\s*$`)
	bulletRE  = regexp.MustCompile(`^\s+[-*]\s+(.+?)\s*$`)
)

// FromMarkdown imports "- [ ]" checkboxes. Headings prefix the step title, and
// indented bullets beneath a step become its acceptance criteria.
func FromMarkdown(text, goal, repo string) (*Plan, error) {
	p := New(goal, repo)
	var (
		heading string
		current *Step
	)
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if m := headingRE.FindStringSubmatch(line); m != nil {
			heading = strings.TrimSpace(m[2])
			continue
		}
		if m := boxRE.FindStringSubmatch(line); m != nil {
			state, title := strings.ToLower(m[1]), strings.TrimSpace(m[2])
			st := Pending
			if state == "x" {
				st = Done
			}
			full := title
			if heading != "" {
				full = heading + ": " + title
			}
			p.Steps = append(p.Steps, Step{
				ID:     fmt.Sprintf("%02d", len(p.Steps)+1),
				Title:  full,
				Status: st,
			})
			current = &p.Steps[len(p.Steps)-1]
			continue
		}
		if current != nil {
			if m := bulletRE.FindStringSubmatch(line); m != nil {
				current.Acceptance = append(current.Acceptance, strings.TrimSpace(m[1]))
			}
		}
	}
	if len(p.Steps) == 0 {
		return nil, fmt.Errorf("no '- [ ]' steps found in markdown")
	}
	return p, nil
}

// writeAtomic was moved to internal/fsutil so the runner can share it.

// NewRunID returns a sortable identifier for a run record.
func NewRunID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}
