# torch

**Pass the torch.** A plan-driven session supervisor for
[OpenCode](https://opencode.ai), written in Go.

You keep a plan on disk. OpenCode works one step at a time in its own session.
When that session goes idle, `torch` reads the evidence — transcript, git
status, diff, commits — asks a model one narrow question ("is this step
actually done?"), and writes the next self-contained instruction. It can stop
there and let you press send, or drive the whole plan itself.

```
PLAN ──▶ instruction ──▶ OpenCode session ──▶ evidence ──▶ verdict
  ▲                                                          │
  └──────────────── next instruction ◀──────────────────────┘
```

## Why this shape

The outer loop is deterministic Go code. The model only judges and writes the
next brief. That is deliberate: most agent failures come from unbounded
autonomy and context drift, not from weak models.

- **One step at a time.** No agent decides to redesign your architecture
  mid-task.
- **Evidence over claims.** A step is done when the diff, tests, and commit say
  so, not when the assistant says "done".
- **A fresh session per step.** Each step starts with a self-contained brief, so
  context never has to survive dozens of tool calls.
- **Cheap judgment.** One structured model call per checkpoint, through your
  existing OpenCode provider.
- **Read-only by default.** `torch next` prints the instruction. `--send` is the
  only flag that creates sessions or spends tokens on work.
- **Crash-safe.** Plan and state are written atomically. A decision is persisted
  only after OpenCode admits the instruction, so a failed send leaves the plan
  exactly where it was.

The design follows the long-running-agent research: an initializer sets up the
environment, every later session reads the progress note and git log, makes
incremental progress on one item, verifies it end to end, and leaves a clean
commit plus a progress update. See [`docs/agent-research.md`](docs/agent-research.md).

## Requirements

- Go 1.24+ (developed on 1.27)
- OpenCode V2 running locally (`opencode service start`)

No third-party Go dependencies.

## Build and test

[Task](https://taskfile.dev) drives the development workflow:

```sh
task          # list tasks
task build    # build bin/torch
task check    # gofmt + go vet + tests
task ci       # what CI should run
```

Without Task, the same commands work directly:

```sh
go build -o bin/torch ./cmd/torch
go test ./...
go install ./cmd/torch
```

Or install the latest pushed version:

```sh
go install github.com/ShubhamTatvamasi/torch/cmd/torch@latest
```

## Quickstart

1. Write the plan as Markdown in your repo:

   ```markdown
   # Foundation
   - [ ] Create the database schema
     - migrations run cleanly
   - [ ] Add the items API
     - GET /items returns 200
     - covered by a test
   ```

2. Import it:

   ```sh
   torch init --goal "Ship the items service" --from PLAN.md
   ```

   This writes `.torch/plan.json` — the canonical, machine-editable plan — and
   `.torch/PROGRESS.md`, the running log every session appends to. Keep editing
   the JSON from here; Markdown was only the seed.

3. Check the wiring:

   ```sh
   torch doctor
   torch status
   ```

4. Get the first instruction (no session created, no writes):

   ```sh
   torch next
   ```

5. Send it and work:

   ```sh
   torch next --send
   ```

6. When you finish working in OpenCode, review and continue:

   ```sh
   torch next
   ```

   or wait for the current session to finish first:

   ```sh
   torch watch
   ```

7. Drive the whole plan:

   ```sh
   torch run --send --max-steps 10
   ```

## Commands

| Command | What it does |
|---|---|
| `torch init` | Create `.torch/plan.json`, optionally importing Markdown |
| `torch status` | Plan progress, attempts, sessions, spend |
| `torch unblock` | Put a blocked step back into the queue with a fresh attempt |
| `torch next` | Review the last session and print the next instruction |
| `torch next --send` | Same, then create a session and send it |
| `torch watch` | Wait for the current session to go idle, then review |
| `torch run --send` | Loop: instruct, wait, review, advance |
| `torch doctor` | Check the service, auth, model, plan, and repo |
| `torch version` | Print the version |

Common flags: `-C/--dir` to point at a repository, `--session ses_...` to review
a specific session, `--model provider/model` to choose the judge model,
`--agent` to choose the OpenCode agent, `--json` for machine-readable output.

Bound a run with `--max-steps`, `--max-minutes`, and `--max-cost`.

## How a turn works

1. **Resolve the step.** The first non-terminal step in the plan.
2. **First contact?** Start it and render a self-contained brief. No session is
   created until you pass `--send`.
3. **Otherwise, collect evidence.** Session outcome, cost and tokens, a compact
   transcript, branch and head, `git status`, `git diff --stat`, recent commits.
4. **Judge once.** One `generate/text` call returns strict JSON: verdict,
   confidence, evidence, remaining work, and the next instruction.
5. **Decide.**
   - `done` → mark complete, brief the next step (new session).
   - `partial` / `off_track` → keep going in the same session with a correction.
   - `blocked` → stop and hand it back to you. Fix the cause, then
     `torch unblock <step-id>` queues a fresh attempt.
6. **Commit after sending.** The plan and checkpoint are written only once the
   prompt is admitted by OpenCode.
7. **Wait.** The server's blocking wait route, with polling as a fallback, so an
   idle transition is never missed.

## The plan format

```json
{
  "version": 1,
  "goal": "Ship the items service",
  "repo": "/absolute/path",
  "created": "2026-11-22T14:00:00Z",
  "steps": [
    {
      "id": "01",
      "title": "Foundation: Create the database schema",
      "goal": "A migration creates the items table",
      "acceptance": ["migrations run cleanly", "indexes exist"],
      "status": "pending",
      "evidence": [],
      "notes": "",
      "session_ids": [],
      "attempts": 0
    }
  ]
}
```

Statuses: `pending`, `in_progress`, `done`, `blocked`, `skipped`.

Acceptance criteria are the contract. If they are vague, the judge will be
vague. Write them so a stranger could check them with a command.

## Configuration

`.torch/config.json` pins the judge model, agent, and budgets:

```json
{
  "judge_model": "gpustack/deepseek-v4.1-flash-1017",
  "agent": "build",
  "max_steps": 10,
  "max_cost_usd": 5.0,
  "max_minutes": 240
}
```

Environment variables override files:

| Variable | Meaning |
|---|---|
| `OPENCODE_URL` | Service base URL |
| `OPENCODE_PASSWORD` | Basic-auth password |
| `OPENCODE_USERNAME` | Basic-auth username (default `opencode`) |
| `TORCH_MODEL` | Judge model, `provider/model` |
| `TORCH_AGENT` | OpenCode agent for new sessions |

Without a pinned model, `torch` uses your OpenCode global default, then the
reviewed session's model. `torch doctor` confirms a model resolves before you
spend anything.

## How it talks to OpenCode

`torch` uses the local HTTP API with the service credentials from
`~/.local/state/opencode/service.json`. Nothing is scraped from the TUI.

- `POST /api/session` — create a session at the repo location
- `POST /api/session/{id}/prompt` — deliver the instruction
- `POST /api/experimental/session/{id}/wait` — block until idle
- `GET /api/session/active` — polling fallback and status
- `GET /api/session/{id}/message` — read the transcript
- `GET /api/session/{id}` — outcome, cost, tokens
- `POST /api/experimental/generate/text` — the judge call

## Design notes

- **Blocking wait, polling fallback.** The event stream is live-only and a
  reconnect can miss the idle event. The server's wait route cannot, and a
  cheap poll covers older servers or transient failures.
- **The judge grades outcomes.** It is told to treat "it works" as weak evidence
  and a test run, diff, or commit as strong evidence. A `done` verdict with no
  cited evidence is rejected and retried.
- **Invalid judge output is retried once** with the validation error fed back,
  then surfaced to the operator with the raw text.
- **Loop breaker.** The same instruction for the same step twice means the last
  attempt did not move the needle. `torch` blocks the step instead of burning
  tokens forever.
- **Every decision is logged** to `.torch/runs/` with the verdict, evidence,
  session, cost, tokens, and instruction hash. That log is the seed of an
  offline eval set.
- **Plan writes are atomic** (temp file + rename).
- **Dry run never mutates the plan.** Reviewing is free of side effects.

## Relationship to baton

`torch` is a Go rewrite of the earlier Python supervisor **baton**. Same loop,
same plan format, sharper edges:

| baton | torch |
|---|---|
| Python + venv + four dependencies | One static binary, standard library only |
| Saves plan before sending | Commits only after OpenCode admits the prompt |
| Polling only | Blocking wait with polling fallback |
| Unbounded repeats | Loop breaker, step/time/cost budgets |
| Model string split naively | Resolves provider/model IDs that contain slashes and variants |
| `rich` tables | Plain, pipe-friendly output with optional color |

## Roadmap

- Parallel steps in isolated worktrees
- A TUI dashboard for runs in flight
- `torch plan add/remove/edit` instead of hand-editing JSON
- Test-command execution as first-class evidence
- Notifications on block/failure
- OpenTelemetry spans per run

## Name

`torch` is the working name: the supervisor passes the torch from one session to
the next. Alternatives if you want something else: **Relay** (same metaphor,
more literal), **Gaffer** (the person who runs the crew), **Tempo** (it keeps
the pace). Renaming is a module path and one directory away.

## Research

The decisions here are grounded in
[`docs/agent-research.md`](docs/agent-research.md), which covers the agent loop,
workflows vs agents, context engineering, harness design for long-running
coding agents, the Go agent ecosystem (Eino, ADK for Go, LangChainGo), state and
durability, evaluation, and security.
