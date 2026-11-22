package plan

import (
	"os"
	"path/filepath"
	"testing"
)

const sample = `# Foundation

- [ ] Create the schema
  - a migration creates an items table
  - migrations run cleanly

# API

- [x] Add the items API
  - GET /items returns 200

- [ ] Add error handling
  - invalid input returns 422
`

func TestFromMarkdown(t *testing.T) {
	p, err := FromMarkdown(sample, "Ship the items service", "/tmp/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(p.Steps))
	}
	if got := p.Steps[0].Title; got != "Foundation: Create the schema" {
		t.Errorf("title = %q", got)
	}
	if len(p.Steps[0].Acceptance) != 2 {
		t.Errorf("acceptance = %v", p.Steps[0].Acceptance)
	}
	if p.Steps[1].Status != Done {
		t.Errorf("checked step status = %q, want done", p.Steps[1].Status)
	}
	if p.Steps[2].ID != "03" {
		t.Errorf("id = %q, want 03", p.Steps[2].ID)
	}
	if p.Repo != "/tmp/repo" || p.Goal != "Ship the items service" {
		t.Errorf("plan header = %q %q", p.Goal, p.Repo)
	}
}

func TestFromMarkdownRequiresSteps(t *testing.T) {
	if _, err := FromMarkdown("# Nothing\n\nsome prose", "g", "/tmp"); err == nil {
		t.Fatal("expected an error for a plan with no steps")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".torch", "plan.json")
	p := New("goal", dir)
	p.Steps = []Step{{ID: "01", Title: "one", Status: Pending}}
	if err := p.Save(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps) != 1 || got.Steps[0].Title != "one" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if got.Created == "" {
		t.Error("created timestamp missing")
	}
}

func TestTransitions(t *testing.T) {
	p := New("goal", "/tmp")
	p.Steps = []Step{
		{ID: "01", Title: "one"},
		{ID: "02", Title: "two"},
	}

	if p.Finished() {
		t.Fatal("a pending plan must not be finished")
	}
	if next := p.Next(); next == nil || next.ID != "01" {
		t.Fatalf("next = %+v", next)
	}

	step, err := p.Start("01")
	if err != nil {
		t.Fatal(err)
	}
	if step.Attempts != 1 || step.Status != InProgress {
		t.Errorf("start = %+v", step)
	}
	if next := p.Next(); next.ID != "01" {
		t.Errorf("in-progress step must remain next, got %s", next.ID)
	}

	if _, err := p.Finish("01", []string{"test passed"}, "ok"); err != nil {
		t.Fatal(err)
	}
	if next := p.Next(); next == nil || next.ID != "02" {
		t.Fatalf("next after finish = %+v", next)
	}

	if _, err := p.Block("02", "no credentials"); err != nil {
		t.Fatal(err)
	}
	if p.Finished() {
		t.Error("a blocked plan is not finished")
	}
	if next := p.Next(); next != nil {
		t.Errorf("blocked steps must not be retried automatically, got %s", next.ID)
	}
	if attention := p.NeedsAttention(); attention == nil || attention.ID != "02" {
		t.Errorf("blocked step should need attention: %+v", attention)
	}
}

func TestReopenResetsAttempts(t *testing.T) {
	p := New("goal", "/tmp")
	p.Steps = []Step{{ID: "01", Title: "one", Status: Blocked, Attempts: 3, Notes: "no key"}}
	step, err := p.Reopen("01")
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != Pending || step.Attempts != 0 {
		t.Fatalf("reopen = %+v", step)
	}
	if step.Notes != "no key" {
		t.Error("reopen should preserve the note for the operator")
	}
	if next := p.Next(); next == nil || next.ID != "01" {
		t.Fatalf("reopened step should be next: %+v", next)
	}
}

func TestAttachSessionIsIdempotent(t *testing.T) {
	p := New("goal", "/tmp")
	p.Steps = []Step{{ID: "01", Title: "one"}}
	if err := p.AttachSession("01", "ses_a"); err != nil {
		t.Fatal(err)
	}
	if err := p.AttachSession("01", "ses_a"); err != nil {
		t.Fatal(err)
	}
	if len(p.Steps[0].SessionIDs) != 1 {
		t.Fatalf("session_ids = %v", p.Steps[0].SessionIDs)
	}
}

func TestValidateRejectsDuplicates(t *testing.T) {
	p := New("goal", "/tmp")
	p.Steps = []Step{{ID: "01", Title: "a"}, {ID: "01", Title: "b"}}
	if err := p.Validate(); err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestValidateAssignsMissingIDs(t *testing.T) {
	p := New("goal", "/tmp")
	p.Steps = []Step{{Title: "a"}, {Title: "b"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	if p.Steps[0].ID != "01" || p.Steps[1].ID != "02" {
		t.Fatalf("ids = %q %q", p.Steps[0].ID, p.Steps[1].ID)
	}
}
