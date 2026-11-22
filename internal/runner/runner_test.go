package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/statvamasi/torch/internal/config"
	"github.com/statvamasi/torch/internal/opencode"
	"github.com/statvamasi/torch/internal/plan"
)

type fakeServer struct {
	t           *testing.T
	repo        string
	verdict     string
	prompts     []string
	sessions    []string
	createID    string
	sessionCost float64
}

func (f *fakeServer) handler() http.Handler {
	mux := http.NewServeMux()

	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("/api/session/active", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{}})
	})

	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			id := f.createID
			if id == "" {
				id = "ses_created"
			}
			f.sessions = append(f.sessions, id)
			writeJSON(w, map[string]any{"data": map[string]any{
				"id":       id,
				"cost":     0,
				"tokens":   map[string]any{"input": 0, "output": 0, "reasoning": 0, "cache": map[string]any{"read": 0, "write": 0}},
				"time":     map[string]any{"created": 1, "updated": 1},
				"location": map[string]any{"directory": f.repo},
			}})
			return
		}
		writeJSON(w, map[string]any{"data": []any{}, "cursor": map[string]any{}})
	})

	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/message"):
			writeJSON(w, map[string]any{"data": []any{
				map[string]any{"id": "m1", "type": "user", "text": "do the work", "time": map[string]any{"created": 1}},
				map[string]any{
					"id": "m2", "type": "assistant", "agent": "build",
					"model": map[string]any{"id": "model", "providerID": "provider"},
					"time":  map[string]any{"created": 2},
					"content": []any{
						map[string]any{"type": "text", "text": "Implemented and tested."},
						map[string]any{"type": "tool", "name": "shell", "state": map[string]any{"status": "completed", "input": map[string]any{}}},
					},
				},
			}})
		case strings.HasSuffix(path, "/prompt"):
			var body struct {
				Text string `json:"text"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.prompts = append(f.prompts, body.Text)
			writeJSON(w, map[string]any{"data": map[string]any{"id": "msg", "sessionID": "ses", "type": "user"}})
		default:
			id := filepath.Base(path)
			writeJSON(w, map[string]any{"data": map[string]any{
				"id":       id,
				"cost":     f.sessionCost,
				"tokens":   map[string]any{"input": 100, "output": 50, "reasoning": 5, "cache": map[string]any{"read": 0, "write": 0}},
				"time":     map[string]any{"created": 1, "updated": 2},
				"outcome":  "succeeded",
				"location": map[string]any{"directory": f.repo},
				"model":    map[string]any{"id": "model", "providerID": "provider"},
			}})
		}
	})

	mux.HandleFunc("/api/experimental/generate/text", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"data": map[string]any{"text": f.verdict}})
	})

	return mux
}

func newFixture(t *testing.T, verdict string) (*Workspace, *opencode.Client, *fakeServer) {
	t.Helper()
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := plan.New("Ship the thing", dir)
	p.Steps = []plan.Step{{
		ID: "01", Title: "Build it", Goal: "It builds",
		Acceptance: []string{"go build succeeds"},
		Status:     plan.InProgress, Attempts: 1,
	}}
	if err := ws.SavePlan(p); err != nil {
		t.Fatal(err)
	}
	if err := ws.SaveState(&State{LastSessionID: "ses_1"}); err != nil {
		t.Fatal(err)
	}

	f := &fakeServer{t: t, repo: dir, verdict: verdict, sessionCost: 0.25}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	client, err := opencode.New(config.Settings{URL: srv.URL, Username: "opencode", Password: "x"})
	if err != nil {
		t.Fatal(err)
	}
	return ws, client, f
}

func TestAdvanceRejectsEmptyPlan(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.SavePlan(plan.New("goal", dir)); err != nil {
		t.Fatal(err)
	}
	if _, err := Advance(context.Background(), ws, nil, config.Settings{}, AdvanceOptions{}); err == nil {
		t.Fatal("expected an error for a plan with no steps")
	}
}

func TestAdvanceStopsWhenAPlanIsBlocked(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := plan.New("goal", dir)
	p.Steps = []plan.Step{{ID: "01", Title: "one", Status: plan.Blocked, Notes: "needs a key"}}
	if err := ws.SavePlan(p); err != nil {
		t.Fatal(err)
	}

	d, err := Advance(context.Background(), ws, nil, config.Settings{}, AdvanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionBlocked {
		t.Fatalf("action = %s, want blocked", d.Action)
	}
	if d.Step == nil || d.Step.Notes != "needs a key" {
		t.Fatalf("step = %+v", d.Step)
	}
}

func TestAdvanceMarksStepDone(t *testing.T) {
	ws, client, _ := newFixture(t, `{"verdict":"done","confidence":0.9,"evidence":["build passes"],"notes":"looks good"}`)

	d, err := Advance(context.Background(), ws, client, config.Settings{}, AdvanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionComplete {
		t.Fatalf("action = %s, want complete", d.Action)
	}
	if d.Verdict == nil || d.Verdict.Verdict != "done" {
		t.Fatalf("verdict = %+v", d.Verdict)
	}

	p, err := ws.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps[0].Status == plan.Done {
		t.Fatal("advance must not persist before commit")
	}

	if err := d.Commit(ws); err != nil {
		t.Fatal(err)
	}
	p, err = ws.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps[0].Status != plan.Done {
		t.Fatalf("status = %s, want done", p.Steps[0].Status)
	}
	state, err := ws.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.TotalCost != 0.25 {
		t.Errorf("total cost = %v, want 0.25", state.TotalCost)
	}

	entries, err := os.ReadDir(ws.RunsPath)
	if err != nil || len(entries) != 1 {
		t.Fatalf("run records = %v, err %v", entries, err)
	}
}

func TestAdvanceContinuesPartialWork(t *testing.T) {
	ws, client, _ := newFixture(t, `{"verdict":"partial","confidence":0.5,"remaining":["POST /items"],"next_instruction":"add POST /items and test it"}`)

	d, err := Advance(context.Background(), ws, client, config.Settings{}, AdvanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionContinue {
		t.Fatalf("action = %s, want continue", d.Action)
	}
	if d.Instruction != "add POST /items and test it" {
		t.Fatalf("instruction = %q", d.Instruction)
	}
}

func TestAdvanceBlocksRepeatedInstruction(t *testing.T) {
	ws, client, _ := newFixture(t, `{"verdict":"partial","next_instruction":"add POST /items"}`)

	state, err := ws.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	state.LastInstructionRef = instructionRef("01", "add POST /items")
	state.Repeated = 2
	if err := ws.SaveState(state); err != nil {
		t.Fatal(err)
	}

	d, err := Advance(context.Background(), ws, client, config.Settings{}, AdvanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionBlocked {
		t.Fatalf("action = %s, want blocked", d.Action)
	}
}

func TestAdvanceBriefsFreshStep(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := plan.New("Ship the thing", dir)
	p.Steps = []plan.Step{{ID: "01", Title: "Build it", Acceptance: []string{"it builds"}}}
	if err := ws.SavePlan(p); err != nil {
		t.Fatal(err)
	}

	f := &fakeServer{t: t, repo: dir}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	client, err := opencode.New(config.Settings{URL: srv.URL, Username: "opencode", Password: "x"})
	if err != nil {
		t.Fatal(err)
	}

	d, err := Advance(context.Background(), ws, client, config.Settings{}, AdvanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != ActionBrief {
		t.Fatalf("action = %s, want brief", d.Action)
	}
	if !strings.Contains(d.Instruction, "Build it") || !strings.Contains(d.Instruction, "it builds") {
		t.Fatalf("brief missing content:\n%s", d.Instruction)
	}
	if err := d.Commit(ws); err != nil {
		t.Fatal(err)
	}
	p, err = ws.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if p.Steps[0].Attempts != 1 || p.Steps[0].Status != plan.InProgress {
		t.Fatalf("step = %+v", p.Steps[0])
	}
}

func TestDispatchCreatesSessionPromptsAndCommits(t *testing.T) {
	ws, client, f := newFixture(t, `{"verdict":"partial","next_instruction":"add POST /items"}`)

	d, err := Advance(context.Background(), ws, client, config.Settings{}, AdvanceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sessionID, err := Dispatch(context.Background(), ws, client, d, DispatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// A partial step continues in the reviewed session, preserving context.
	if sessionID != "ses_1" {
		t.Fatalf("session = %q", sessionID)
	}
	if len(f.prompts) != 1 || f.prompts[0] != "add POST /items" {
		t.Fatalf("prompts = %v", f.prompts)
	}

	p, err := ws.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Steps[0].SessionIDs) != 1 || p.Steps[0].SessionIDs[0] != "ses_1" {
		t.Fatalf("session ids = %v", p.Steps[0].SessionIDs)
	}
	state, err := ws.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.LastSessionID != "ses_1" || state.Instructions != 1 {
		t.Fatalf("state = %+v", state)
	}
}

func TestDispatchCreatesNewSessionForBrief(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := plan.New("goal", dir)
	p.Steps = []plan.Step{{ID: "01", Title: "one", Attempts: 1, Status: plan.InProgress}}
	if err := ws.SavePlan(p); err != nil {
		t.Fatal(err)
	}

	f := &fakeServer{t: t, repo: dir, createID: "ses_created"}
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	client, err := opencode.New(config.Settings{URL: srv.URL, Username: "opencode", Password: "x"})
	if err != nil {
		t.Fatal(err)
	}

	decision := &Decision{
		Action: ActionBrief, Instruction: "start work",
		Step: &p.Steps[0], plan: p, state: &State{},
	}
	sessionID, err := Dispatch(context.Background(), ws, client, decision, DispatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != "ses_created" {
		t.Fatalf("session = %q, want ses_created", sessionID)
	}
	if len(f.prompts) != 1 || f.prompts[0] != "start work" {
		t.Fatalf("prompts = %v", f.prompts)
	}
}

func TestDispatchDoesNotCommitOnSendFailure(t *testing.T) {
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	p := plan.New("goal", dir)
	p.Steps = []plan.Step{{ID: "01", Title: "one", Status: plan.Pending}}
	if err := ws.SavePlan(p); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"data":{"id":"ses_x","cost":0,"tokens":{"input":0,"output":0,"reasoning":0,"cache":{"read":0,"write":0}},"time":{"created":1,"updated":1},"location":{"directory":"`+dir+`"}}}`)
	})
	mux.HandleFunc("/api/session/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"nope"}`, http.StatusConflict)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	client, err := opencode.New(config.Settings{URL: srv.URL, Username: "opencode", Password: "x"})
	if err != nil {
		t.Fatal(err)
	}

	decision := &Decision{
		Action: ActionBrief, Instruction: "do it",
		Step: &p.Steps[0], plan: p, state: &State{},
	}
	if _, err := Dispatch(context.Background(), ws, client, decision, DispatchOptions{}); err == nil {
		t.Fatal("expected a send failure")
	}
	reloaded, err := ws.LoadPlan()
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Steps[0].SessionIDs) != 0 {
		t.Fatalf("failed send must not persist session ids: %v", reloaded.Steps[0].SessionIDs)
	}
}
