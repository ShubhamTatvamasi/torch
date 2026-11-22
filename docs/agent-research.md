# Building an AI Agent in Go — Research Notes

_Compiled November 2026. Sources are linked inline. Framework details move
fast; re-check versions before committing._

> **Applied here:** this repository implements the research as `torch`, a
> plan-driven session supervisor for OpenCode, written in Go with no external
> dependencies. See [`README.md`](../README.md) for usage and design.

---

## 1. The ten decisions that matter

Most agent projects fail on architecture and operations, not model choice. In
roughly descending order of impact:

1. **Build the simplest thing that can work.** A single model call with good
   context beats an agent for most "agent" requests. A fixed workflow beats an
   autonomous agent whenever the steps are known.
2. **An agent is a loop, not a prompt.** `model + tools + context + control
   loop + state`. Everything else is implementation detail.
3. **Context engineering is the moat.** Curate the smallest set of high-signal
   tokens each turn; compact, offload to files, isolate with subagents.
4. **Design the agent–computer interface like a human UI.** Tool names,
   descriptions, argument shapes, and error messages decide reliability.
5. **Evals and tracing come before features.** If you cannot see the trajectory
   and score the outcome, you are guessing.
6. **Bound the loop.** Max iterations, token budget, wall-clock timeout, cost
   cap, and a defined escalation path.
7. **Assume prompt injection is unsolved.** Least privilege, sandboxing, egress
   allowlists, and human approval for irreversible actions.
8. **Design for crashes and pauses.** Checkpoint after every step. Approvals may
   arrive days later. Processes die.
9. **Start read-only, widen autonomy gradually.** Copilot, then supervised
   agent, then autonomous agent.
10. **Measure cost, latency, and success per step**, not per request. Route
    cheap models to cheap steps.

The recurring finding across Anthropic, Google, AWS, and LangChain's 2026
survey: **quality and reliability are the top blockers; cost is not.**
Complexity is easy to add and hard to remove.

---

## 2. What an agent actually is

An agent is a program in which an LLM decides the control flow:

```
loop:
  context  = assemble(system, memory, history, tool results, retrieved docs)
  response = model(context, tools)
  if response is final_answer: return
  result   = execute(tool_call)          # sandboxed, permissioned
  history += (tool_call, result)
  if budget_exhausted: escalate_or_summarize
```

Three layers have different failure modes:

| Layer | What it is | Where bugs live |
|---|---|---|
| **Model** | The reasoning engine | Wrong tool choice, hallucinated args, instruction drift |
| **Harness** | Loop, context manager, tool executor, permissions, compaction | Context bloat, bad truncation, missing retries, unsafe tools |
| **Product** | UI, auth, tenancy, queues, storage, billing | Everything operational |

A surprising amount of "the model is dumb" is a harness bug: truncated tool
output, an ambiguous tool description, history summarized too aggressively, or
an error message that gives the model nothing to recover from.

### Workflow or agent?

Anthropic's distinction remains the most useful framing
([Building effective agents](https://www.anthropic.com/engineering/building-effective-agents)):

- **Workflow** — model calls orchestrated through predefined code paths.
  Predictable, cheap, testable.
- **Agent** — the model dynamically directs its own process and tool use.
  Flexible, expensive, non-deterministic.

Five workflow patterns cover most needs before an agent is required:

1. **Prompt chaining** — fixed sequential steps with gates.
2. **Routing** — classify, then dispatch to a specialist prompt or model.
3. **Parallelization** — sectioning or voting.
4. **Orchestrator–workers** — a planner delegates to workers.
5. **Evaluator–optimizer** — generate, critique, revise until a bar is met.

**Rule of thumb:** if you can draw the flowchart, build the workflow. Use an
agent only where the number and nature of steps genuinely cannot be predicted:
coding, research, open-ended support, computer use.

---

## 3. The complexity ladder

Climb one rung at a time, and only when the rung below demonstrably fails:

```
1. Single prompt + structured output
2. Prompt + retrieval (RAG)
3. Fixed workflow (chain / route / parallel / evaluator)
4. Single agent with a small, sharp toolset
5. Single agent + memory + subagents (context isolation)
6. Multi-agent system (coordinator / specialists)
```

Every rung adds latency, cost, and a failure surface. Multi-agent is justified
when subtasks need genuinely different tools, parallel exploration pays for
itself, or context isolation is the point. It is **not** justified by "it sounds
more advanced."

`torch` deliberately sits at rung 3 with a model-shaped gap: a deterministic
supervisor loop, one structured judgment per checkpoint. The model judges and
writes the next brief; Go decides what happens next.

---

## 4. Context engineering for long-running work

Context is a **finite attention budget**. Recall degrades as context grows
("context rot") across models; more tokens do not mean more capability
([Anthropic: Effective context engineering](https://www.anthropic.com/research/effective-context-engineering-for-ai-agents)).

### Principles

- **Smallest set of high-signal tokens.** Every turn, ask what the model needs
  to take the next correct action.
- **Just-in-time retrieval.** Keep identifiers (paths, IDs, URLs) and let the
  agent pull detail with tools.
- **Structure the system prompt like a spec:** role, task, constraints, tool
  guidance, output contract, escalation rules.
- **Few-shot examples beat adjectives.** Show one good call and one good
  refusal.

### The four levers

| Lever | What it does | When |
|---|---|---|
| **Compaction** | Summarize near the window limit, keep recent turns + artifacts | Long conversations |
| **Tool-result clearing** | Drop raw output once consumed, keep references | Tool-heavy loops |
| **Structured note-taking** | Progress file / to-do list outside context | Multi-session work |
| **Subagents** | Fresh context per subtask, return a distilled summary | Research, broad search |

Anthropic's long-running harness work adds a fifth: an **initializer run** that
scaffolds the environment, then every later session reads state, picks one item,
works incrementally, tests end-to-end, and leaves a clean commit plus a progress
note
([Effective harnesses](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)).

That article documents four concrete failure modes and fixes that map directly
onto this project:

| Failure | Fix used by `torch` |
|---|---|
| Tries to one-shot the whole project, runs out of context mid-feature | One plan step per session; step-scoped brief |
| Declares the project done after seeing some progress | Machine-checked plan state; judge grades acceptance criteria |
| Leaves the tree buggy or undocumented | Commit + progress note required; judge reads git evidence |
| Marks work done without testing | Brief requires verification commands; judge treats claims as weak evidence |

### Memory tiers

1. **Working memory** — current context window; managed by compaction.
2. **Episodic memory** — conversation history, checkpoints, transcripts (your
   database).
3. **Semantic memory** — facts and preferences across sessions (files, vector
   store, knowledge graph).

Start with files plus a database. Add a memory product only when cross-session
recall is the measured bottleneck. Memory adds a second retrieval failure mode
and a privacy surface.

### Anti-patterns

- Stuffing the whole repo into the prompt "just in case."
- Untuned summarization that drops the one constraint that mattered.
- Subagents returning raw tool output.
- Memory that grows without dedupe, contradiction handling, or a delete path.

---

## 5. Tools and the agent–computer interface

Tool design is prompt engineering for actions. Anthropic reports spending more
time optimizing tools than prompts on their SWE-bench agent.

Good tools:

- **One obvious way to do a thing.** Overlapping tools cause dithering.
- **Self-explanatory names and arguments.** `search_orders(email, since)` beats
  `query(q)`.
- **Absolute paths and opaque IDs** over inferred state.
- **Descriptions with an example, edge cases, and boundaries.**
- **Structured, actionable errors:** `{"error":"not_found","hint":"try
  search_customers first"}`.
- **Idempotency keys** for mutations.
- **Bounded outputs.** Paginate, cap rows, return summary plus handle.
- **Declared side effects.** Read vs write vs destructive, so the harness gates
  them.

MCP is now the default integration layer, but it is a convenience, not an
architecture ([spec](https://modelcontextprotocol.io/specification/2026-07-28/server/tools)).
Treat every MCP server as untrusted third-party code: pin versions, review,
sandbox, strip secrets, restrict egress. Audit tool descriptions before trusting
a capable model with them.

`torch`'s interface to OpenCode is the HTTP API, not MCP. The models are narrow
and single-purpose: list sessions, read messages, send a prompt, wait, collect
git evidence, one-shot generate. Fewer, sharper verbs.

---

## 6. Frameworks: what to use in Go

The agent loop is small. In Go, the decision is mostly about whether you want a
graph runtime, provider adapters, and tracing handed to you.

| Option | Language | Sweet spot | Watch out |
|---|---|---|---|
| **stdlib** | Go | Small, deterministic harnesses; single binary; zero deps | You build durability, retries, observability yourself |
| **Eino** (`cloudwego/eino`) | Go | LangChain-style components, graph workflows, ADK, interrupt/resume | Young ecosystem; conceptual surface is large |
| **Google ADK for Go** (`google.golang.org/adk/v2`) | Go | Gemini/Vertex, LLM agents, graph workflows, A2A, deployment | Google ecosystem gravity; v2 API still moving |
| **LangChainGo** | Go | If you already think in LangChain primitives | Port lags Python; abstraction tax |
| **MCP Go SDK** | Go | Building MCP servers/clients | MCP only; not an agent runtime |
| **OpenCode SDK / HTTP API** | any | Driving coding sessions, tools, permissions, compaction | Experimental surface; pin to a version |

Sources: [Eino README](https://github.com/cloudwego/eino),
[ADK Go quickstart](https://google.github.io/adk-docs/get-started/go/),
[LangChainGo](https://github.com/tmc/langchaingo).

### Why this project uses stdlib only

- **The loop is ~300 lines.** Eino/ADK would replace it with concepts we do not
  need and cannot see through.
- **Durability is already ours.** Plan and state are files written atomically;
  OpenCode owns the session transcript and tool execution.
- **Deployment is trivial.** `go build` produces one static binary; no venv, no
  Python runtime, no dependency resolution at install time.
- **The model stays behind an interface.** One `GenerateText` call today; swap
  in a provider SDK later without touching the control loop.

Adopt a framework when a concrete need appears: durable workflow replay,
multi-agent coordination, or provider-specific streaming. Prompts, tools, evals,
and state outlive frameworks.

---

## 7. State, durability, and human-in-the-loop

"Resumable" means three different things:

1. **Saved conversation** — history survives; a step restarts from the last
   message.
2. **Checkpointed state** — state saved after each node; resume from the
   checkpoint.
3. **Durable execution** — every step recorded and replayed; survives crashes
   mid-step (Temporal, DBOS, Restate, Inngest).

`torch` targets (2): the plan and runner state are checkpointed after every
transition, so a crash loses at most the in-flight session. OpenCode's session
store holds the conversation; resuming is reading the plan and re-judging.

Failure modes to design around:

- Interrupted nodes may replay side effects — make writes idempotent.
- Tools may run more than once on resume.
- In-memory state is lost when the process exits.
- Long approvals cannot hold a process open.

Human approval is a primitive, not a prompt instruction:

- Gate **irreversible or high-blast-radius** actions.
- Show the reviewer the action, arguments, reasoning, and confidence.
- Store the paused run outside the process.
- Log every decision with who and why.
- Default to **ask** on ambiguity.

`torch` gates the only expensive, mutating action (sending a prompt / creating a
session) behind `--send`. Review is free of side effects. Commits are requested
from the coding agent, not executed by `torch`.

---

## 8. Evaluation and observability

Observability is table stakes; evals lag. Instrument first:

- One trace per run, spans for model call, tool call, retrieval, compaction,
  approval wait.
- Log tokens, cost, model, prompt version, run ID.
- Vendor-neutral OTel keeps you portable.

Evaluate at three levels:

| Level | Question |
|---|---|
| **Run (step)** | Right tool, right args? |
| **Trace (turn)** | Task completed by a sane path, with the right side effects? |
| **Thread (conversation)** | Problem resolved over time, memory intact, recovery sound? |

Grade the **outcome and environment state**, not the exact tool sequence. Use
partial credit, multiple trials, binary pass/fail for LLM judges, and code-based
graders wherever an objective check exists. Keep an offline regression set and
sample production traces online
([Demystifying evals](https://www.anthropic.com/research/demystifying-evals-for-ai-agents)).

`torch` writes one JSON run record per checkpoint under `.torch/runs/`:
verdict, confidence, evidence, cost, tokens, session, and instruction hash. That
is the seed of an offline eval set.

---

## 9. Security and guardrails

There is no reliable defense against prompt injection. Assume the model can be
convinced to misuse any capability you give it.

1. **Least privilege per tool and per user.** The agent's credentials should be
   narrower than the user's. Never let model output decide authorization.
2. **Sandbox execution.** Containers or microVMs for code and shell; no host
   filesystem, no inherited secrets, non-root, default-deny egress.
3. **Trust boundaries.** Web pages, emails, documents, MCP servers, and tool
   output are untrusted data. They never redefine the system prompt or tool
   permissions.
4. **Action gating.** Read-only by default; destructive tools require approval.
5. **Graduated autonomy.** Read-only → suggest → act-with-approval →
   act-with-audit.
6. **Runtime guardrails.** Input/output filtering, PII detection, schema
   validation. Prefer deterministic checks.
7. **Kill switch and rate limits** per user, tool, and agent.
8. **Audit trail.** Every tool call, approval, and config change, attributable
   and retained.

References: [OWASP AI Agent Security Cheat
Sheet](https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html),
[NSA/CISA MCP security
guidance](https://media.defense.gov/2026/Jun/02/2003943289/-1/-1/0/CSI_MCP_SECURITY.PDF).

Practical notes for `torch`:

- The OpenCode service password is read from the local service registration or
  environment; it is never sent anywhere else and never logged.
- The judge prompt contains repository evidence. Treat repo content as data:
  the review prompt says so explicitly.
- The judge is read-only (one-shot generation, no tools).

---

## 10. Reliability, cost, and latency

Bound everything:

- Max iterations, tokens, wall-clock, tool calls.
- On exhaustion: summarize, return partial results, escalate — do not spin.
- Timeouts and retries with backoff on every external call; distinguish
  transient from permanent.
- Circuit breakers for flaky dependencies; fallback model.

Model routing: small/fast models for classification, routing, extraction; a
frontier model for planning and synthesis; cheap models for judging and
summarizing. Measure on your own traffic.

Other levers: prompt caching, streaming, parallel independent tool calls,
caching idempotent lookups, compressing tool output at the boundary.

`torch` enforces `--max-steps`, `--max-minutes`, and a `max_cost_usd` budget in
`.torch/config.json`. It also detects a repeated identical instruction and
blocks the step instead of burning tokens in a loop.

---

## 11. Deployment and operations

Minimum viable production shape:

```
API/UI → queue → agent worker (stateless) → tools (sandboxed) → DB/object store
                     ↓
              trace exporter → observability backend
```

- Stateless workers, externalized state.
- Queue for long runs; HTTP requests are the wrong unit for a 10-minute agent.
- Version prompts, tool schemas, models, config. A trace should say exactly what
  ran.
- Canary and roll back prompts like code.
- Per-step budgets and alerts.
- Shadow mode for new versions.
- Retention and deletion paths for transcripts and memory.

For a local developer tool, the deployment is the binary plus the OpenCode
service. The same discipline applies: everything versionable lives in the repo.

---

## 12. Anti-patterns

- **Monolithic agent** with 40 tools and a 5,000-word prompt.
- **Unbounded loops.** A confused agent will burn the budget.
- **Trust-later architecture.** Guardrails, tracing, and evals bolted on later
  cost more and break more.
- **Framework-first.** Choosing the stack before the task.
- **Vibe-check evals.**
- **Over-summarization.** Compaction that drops the constraint that mattered.
- **Exact-trajectory tests.** They punish valid creativity.
- **Memory maximalism.**
- **Treating retrieved text as instructions.**
- **Autonomy without observability.**

---

## 13. What `torch` deliberately does

| Decision | Why |
|---|---|
| Deterministic outer loop, model only judges | Bounds autonomy and context drift |
| One plan step per session | Prevent one-shotting and mid-feature context exhaustion |
| Fresh, self-contained brief per step | Context never has to survive dozens of tool calls |
| Evidence over claims | Done means diff, tests, commit — not "it works" |
| Blocking wait endpoint, polling fallback | Cannot miss the idle transition; cheap |
| Atomic plan/state writes | Crash-safe checkpoints |
| JSON plan, not Markdown, as canonical | Models are less likely to rewrite JSON than prose |
| `--send` as the only mutating flag | Read-only by default; human in the loop |
| Zero dependencies | One binary; auditable; trivial to install |

---

## 14. Source list

**Foundational**

- Anthropic — [Building effective agents](https://www.anthropic.com/engineering/building-effective-agents)
- Anthropic — [Effective context engineering for AI agents](https://www.anthropic.com/research/effective-context-engineering-for-ai-agents)
- Anthropic — [Effective harnesses for long-running agents](https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents)
- Anthropic — [Demystifying evals for AI agents](https://www.anthropic.com/research/demystifying-evals-for-ai-agents)
- Google Cloud — [Choose a design pattern for your agentic AI system](https://docs.cloud.google.com/architecture/choose-design-pattern-agentic-ai-system)
- AWS — [Inside an enterprise AI agent: a governed, observable loop architecture](https://aws.amazon.com/blogs/architecture/inside-an-enterprise-ai-agent-a-governed-observable-loop-architecture/)

**Go ecosystem**

- [Eino](https://github.com/cloudwego/eino) — Go LLM framework with ADK, graphs, interrupt/resume
- [Google ADK for Go](https://google.github.io/adk-docs/get-started/go/) — LLM agents, workflows, A2A
- [LangChainGo](https://github.com/tmc/langchaingo) — LangChain port for Go
- [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) — MCP client/server
- [OpenCode API](https://opencode.ai/docs/api) — sessions, prompts, messages, generate, events

**Evaluation and operations**

- LangChain — [State of Agent Engineering](https://www.langchain.com/state-of-agent-engineering)
- LangChain — [Evaluating AI agents at run, trace, and thread level](https://www.langchain.com/resources/agent-evals)
- Google — [Evaluating AI agents: moving past the vibe check](https://developers.googleblog.com/evaluating-ai-agents-moving-past-the-vibe-check/)

**Security**

- OWASP — [AI Agent Security Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/AI_Agent_Security_Cheat_Sheet.html)
- NSA/CISA — [MCP security design considerations](https://media.defense.gov/2026/Jun/02/2003943289/-1/-1/0/CSI_MCP_SECURITY.PDF)
