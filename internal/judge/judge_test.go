package judge

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/statvamasi/torch/internal/opencode"
	"github.com/statvamasi/torch/internal/plan"
)

func TestParseVerdictBare(t *testing.T) {
	v, err := ParseVerdict(`{"verdict":"done","confidence":0.9,"evidence":["tests pass"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != "done" || len(v.Evidence) != 1 {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestParseVerdictFenced(t *testing.T) {
	raw := "Here you go:\n```json\n{\"verdict\":\"partial\",\"next_instruction\":\"do more\"}\n```\n"
	v, err := ParseVerdict(raw)
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != "partial" || v.NextInstruction != "do more" {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestParseVerdictWrappedInProse(t *testing.T) {
	v, err := ParseVerdict(`Sure. {"verdict":"off_track","next_instruction":"stay on task"} done.`)
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != "off_track" {
		t.Fatalf("verdict = %+v", v)
	}
}

func TestParseVerdictRejectsDoneWithoutEvidence(t *testing.T) {
	_, err := ParseVerdict(`{"verdict":"done","evidence":[]}`)
	if err == nil {
		t.Fatal("expected done-without-evidence to be rejected")
	}
}

func TestParseVerdictRequiresNextInstruction(t *testing.T) {
	_, err := ParseVerdict(`{"verdict":"partial"}`)
	if err == nil {
		t.Fatal("expected missing next_instruction to be rejected")
	}
}

func TestParseVerdictRejectsUnknownVerdict(t *testing.T) {
	_, err := ParseVerdict(`{"verdict":"finished","evidence":["x"]}`)
	if err == nil {
		t.Fatal("expected unknown verdict to be rejected")
	}
}

func TestParseVerdictClampsConfidence(t *testing.T) {
	v, err := ParseVerdict(`{"verdict":"partial","next_instruction":"x","confidence":7}`)
	if err != nil {
		t.Fatal(err)
	}
	if v.Confidence != 1 {
		t.Fatalf("confidence = %v", v.Confidence)
	}
}

type fakeGen struct {
	replies []string
	prompts []string
}

func (f *fakeGen) GenerateText(_ context.Context, prompt string, _ *opencode.ModelRef) (string, error) {
	f.prompts = append(f.prompts, prompt)
	if len(f.replies) == 0 {
		return "", errors.New("no replies")
	}
	reply := f.replies[0]
	f.replies = f.replies[1:]
	return reply, nil
}

func fixture() (*plan.Plan, *plan.Step) {
	p := plan.New("goal", "/repo")
	p.Steps = []plan.Step{{ID: "01", Title: "one", Attempts: 1, Status: plan.InProgress}}
	return p, &p.Steps[0]
}

func TestJudgeRetriesOnce(t *testing.T) {
	p, step := fixture()
	gen := &fakeGen{replies: []string{"not json", `{"verdict":"done","evidence":["commit abc"]}`}}
	v, err := Judge(context.Background(), gen, p, step, "evidence", nil)
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != "done" {
		t.Fatalf("verdict = %+v", v)
	}
	if len(gen.prompts) != 2 {
		t.Fatalf("prompts = %d, want 2", len(gen.prompts))
	}
	if !strings.Contains(gen.prompts[1], "could not be parsed") {
		t.Error("retry prompt should include the parse failure")
	}
}

func TestJudgeSurfacesDoubleFailure(t *testing.T) {
	p, step := fixture()
	gen := &fakeGen{replies: []string{"nope", "still nope"}}
	_, err := Judge(context.Background(), gen, p, step, "evidence", nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !IsParseError(err) {
		t.Fatalf("want parse error, got %T: %v", err, err)
	}
}
