// Package evidence gathers the facts a reviewer needs: session state, a
// compact transcript, and the state of the working tree.
//
// Evidence is deliberately boring. The judge is not asked to trust the coding
// agent's summary; it is asked to look at what the repository says.
package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ShubhamTatvamasi/torch/internal/opencode"
)

// MaxTranscript caps the transcript tail handed to the judge.
const MaxTranscript = 8000

// Evidence is one snapshot for one checkpoint.
type Evidence struct {
	SessionID    string
	Session      opencode.Session
	Branch       string
	Head         string
	GitStatus    string
	GitLog       string
	DiffStat     string
	ChangedFiles []string
	Transcript   string
}

// Collect builds a snapshot for sessionID rooted at repo.
func Collect(ctx context.Context, repo string, client *opencode.Client, sessionID string) (*Evidence, error) {
	session, err := client.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("read session %s: %w", sessionID, err)
	}
	messages, err := client.Messages(ctx, sessionID, 40, "desc")
	if err != nil {
		// The transcript is useful but not essential; keep going with what we
		// have rather than losing the whole checkpoint.
		messages = nil
	}

	status := git(ctx, repo, "status", "--porcelain=v1")
	return &Evidence{
		SessionID:    sessionID,
		Session:      session,
		Branch:       git(ctx, repo, "branch", "--show-current"),
		Head:         git(ctx, repo, "rev-parse", "--short", "HEAD"),
		GitStatus:    status,
		GitLog:       git(ctx, repo, "log", "--oneline", "-10"),
		DiffStat:     git(ctx, repo, "diff", "--stat"),
		ChangedFiles: changedFiles(status),
		Transcript:   Transcript(messages, MaxTranscript),
	}, nil
}

// Render produces the evidence block for the judge prompt.
func (e *Evidence) Render() string {
	tokens := e.Session.Tokens
	sections := []string{
		"## Session",
		fmt.Sprintf("id: %s", e.SessionID),
		fmt.Sprintf("outcome: %s", orUnknown(e.Session.Outcome)),
		fmt.Sprintf("cost_usd: %.4f", e.Session.Cost),
		fmt.Sprintf("tokens_in: %.0f tokens_out: %.0f reasoning: %.0f cache_read: %.0f",
			tokens.Input, tokens.Output, tokens.Reasoning, tokens.Cache.Read),
		"",
		"## Repository",
		fmt.Sprintf("branch: %s head: %s", orUnknown(e.Branch), orUnknown(e.Head)),
		"",
		"## Working tree",
		orFallback(e.GitStatus, "(clean)"),
		"",
		"## Diff stat",
		orFallback(e.DiffStat, "(no unstaged changes)"),
		"",
		"## Recent commits",
		orFallback(e.GitLog, "(no commits)"),
		"",
		"## Transcript (tail)",
		orFallback(e.Transcript, "(empty)"),
	}
	return strings.Join(sections, "\n")
}

// Transcript renders a compact chronological transcript: user text, assistant
// text, and tool names with status. The API returns newest first, so reverse.
func Transcript(messages []opencode.Message, limit int) string {
	lines := make([]string, 0, len(messages)*2)
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		switch m.Type {
		case "user":
			if text := strings.TrimSpace(m.Text); text != "" {
				lines = append(lines, "USER: "+clip(text, 1200))
			}
		case "assistant":
			var textParts, toolParts []string
			for _, part := range m.Content {
				switch part.Type {
				case "text":
					if t := strings.TrimSpace(part.Text); t != "" {
						textParts = append(textParts, t)
					}
				case "tool":
					toolParts = append(toolParts, toolLabel(part))
				}
			}
			if len(textParts) > 0 {
				lines = append(lines, "ASSISTANT: "+clip(strings.Join(textParts, "\n"), 2000))
			}
			if len(toolParts) > 0 {
				lines = append(lines, "TOOLS: "+strings.Join(toolParts, ", "))
			}
			if m.Error != nil && m.Error.Message != "" {
				lines = append(lines, "ERROR: "+clip(m.Error.Message, 400))
			}
		}
	}

	joined := strings.Join(lines, "\n\n")
	if limit <= 0 || len(joined) <= limit {
		return joined
	}
	return "...[truncated]...\n" + joined[len(joined)-limit:]
}

func toolLabel(part opencode.ContentPart) string {
	label := part.Name
	if label == "" {
		label = "tool"
	}
	if len(part.State) == 0 {
		return label
	}
	var st opencode.ToolState
	if err := json.Unmarshal(part.State, &st); err != nil {
		return label
	}
	switch st.Status {
	case "error":
		msg := st.Error.Message
		if msg == "" {
			msg = "failed"
		}
		return fmt.Sprintf("%s(error: %s)", label, clip(msg, 160))
	case "completed", "running", "streaming":
		return label + "(" + st.Status + ")"
	default:
		return label
	}
}

// changedFiles extracts tracked paths from `git status --porcelain`, skipping
// untracked entries.
func changedFiles(status string) []string {
	var out []string
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 || strings.HasPrefix(line, "??") {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if i := strings.LastIndex(path, " -> "); i >= 0 {
			path = path[i+4:]
		}
		if path != "" {
			out = append(out, path)
		}
	}
	return out
}

func git(ctx context.Context, repo string, args ...string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	full := append([]string{"-C", repo}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func clip(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

func orFallback(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
