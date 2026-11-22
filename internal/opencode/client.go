// Package opencode is a small, dependency-free client for the OpenCode HTTP
// API. It exposes only the verbs the supervisor needs.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/statvamasi/torch/internal/config"
)

// APIError is a non-2xx response from the server.
type APIError struct {
	Status int
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	body := strings.TrimSpace(e.Body)
	if len(body) > 400 {
		body = body[:400] + "…"
	}
	return fmt.Sprintf("%s -> HTTP %d: %s", e.Path, e.Status, body)
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// Client talks to one OpenCode service.
type Client struct {
	base     *url.URL
	http     *http.Client
	username string
	password string
	maxTries int
}

// New builds a client from discovered settings.
func New(s config.Settings) (*Client, error) {
	base, err := url.Parse(s.URL)
	if err != nil {
		return nil, fmt.Errorf("invalid OpenCode URL %q: %w", s.URL, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid OpenCode URL %q", s.URL)
	}
	return &Client{
		base:     base,
		http:     &http.Client{},
		username: s.Username,
		password: s.Password,
		maxTries: 3,
	}, nil
}

// URL reports the service base URL.
func (c *Client) URL() string { return c.base.String() }

// -- plumbing ---------------------------------------------------------------

// defaultTimeout bounds ordinary calls. Blocking calls (wait) opt out.
const defaultTimeout = 60 * time.Second

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	return c.doWithTimeout(ctx, true, method, path, query, body, out)
}

func (c *Client) doWithTimeout(ctx context.Context, bounded bool, method, path string,
	query url.Values, body, out any) error {
	if bounded {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, defaultTimeout)
			defer cancel()
		}
	}
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}

	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + path
	if query != nil {
		u.RawQuery = query.Encode()
	}

	var lastErr error
	for attempt := 0; attempt < c.maxTries; attempt++ {
		if attempt > 0 {
			// 300ms, 900ms: short enough to feel interactive, long enough to
			// ride out a service restart.
			d := time.Duration(300*math.Pow(3, float64(attempt-1))) * time.Millisecond
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
			}
		}

		var reader io.Reader
		if payload != nil {
			reader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
		if err != nil {
			return err
		}
		req.SetBasicAuth(c.username, c.password)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode >= 400 {
			ae := &APIError{Status: resp.StatusCode, Path: path, Body: string(data)}
			if retryable(resp.StatusCode) && attempt < c.maxTries-1 {
				lastErr = ae
				continue
			}
			return ae
		}
		if out == nil || resp.StatusCode == http.StatusNoContent || len(data) == 0 {
			return nil
		}
		if err := decodeEnvelope(data, out); err != nil {
			return fmt.Errorf("%s: decode response: %w", path, err)
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return lastErr
}

func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// decodeEnvelope unwraps the {"data": ...} envelope most endpoints use, while
// still accepting endpoints that return a bare object (for example /api/info).
func decodeEnvelope(data []byte, out any) error {
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err == nil && len(env.Data) > 0 &&
		string(env.Data) != "null" {
		return json.Unmarshal(env.Data, out)
	}
	return json.Unmarshal(data, out)
}

// -- server -----------------------------------------------------------------

// Info returns server metadata.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	err := c.do(ctx, http.MethodGet, "/api/info", nil, nil, &out)
	return out, err
}

// Agents lists configured agents.
func (c *Client) Agents(ctx context.Context) ([]Agent, error) {
	var out []Agent
	err := c.do(ctx, http.MethodGet, "/api/agent", nil, nil, &out)
	return out, err
}

// Models lists provider models.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	var out []Model
	err := c.do(ctx, http.MethodGet, "/api/model", nil, nil, &out)
	return out, err
}

// ResolveModel turns "provider/model" or "provider/model#variant" into a
// ModelRef. Provider and model IDs may both contain slashes, so match on the
// model ID and on a suffix rather than splitting naively.
func (c *Client) ResolveModel(ctx context.Context, ref string) (*ModelRef, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	variant := ""
	if i := strings.LastIndex(ref, "#"); i > 0 {
		variant = ref[i+1:]
		ref = ref[:i]
	}
	provider, rest, ok := strings.Cut(ref, "/")
	if !ok || provider == "" || rest == "" {
		return nil, fmt.Errorf("model must look like provider/model, got %q", ref)
	}

	models, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}

	var exact, suffix []Model
	for _, m := range models {
		if m.ProviderID != provider {
			continue
		}
		switch {
		case m.ModelID == rest || m.ID == rest || m.ID == ref:
			exact = append(exact, m)
		case strings.HasSuffix(m.ModelID, "/"+rest):
			suffix = append(suffix, m)
		}
	}

	pick := exact
	if len(pick) == 0 {
		pick = suffix
	}
	switch len(pick) {
	case 0:
		var available []string
		for _, m := range models {
			if m.ProviderID == provider && len(available) < 10 {
				available = append(available, m.ProviderID+"/"+m.ModelID)
			}
		}
		if len(available) == 0 {
			return nil, fmt.Errorf("provider %q is not available", provider)
		}
		return nil, fmt.Errorf("cannot resolve %q; provider offers: %s",
			ref, strings.Join(available, ", "))
	case 1:
		out := &ModelRef{ID: pick[0].ModelID, ProviderID: pick[0].ProviderID}
		if variant != "" {
			out.Variant = variant
		}
		return out, nil
	default:
		var choices []string
		for _, m := range pick {
			choices = append(choices, m.ProviderID+"/"+m.ModelID)
		}
		return nil, fmt.Errorf("%q is ambiguous: %s", ref, strings.Join(choices, ", "))
	}
}

// -- sessions ---------------------------------------------------------------

// CreateSession creates a session rooted at directory.
func (c *Client) CreateSession(ctx context.Context, directory, title, agent string, model *ModelRef) (Session, error) {
	body := map[string]any{"location": Location{Directory: directory}}
	if title != "" {
		body["title"] = title
	}
	if agent != "" {
		body["agent"] = agent
	}
	if model != nil && !model.IsZero() {
		body["model"] = model
	}
	var out Session
	err := c.do(ctx, http.MethodPost, "/api/session", nil, body, &out)
	return out, err
}

// GetSession fetches one session.
func (c *Client) GetSession(ctx context.Context, id string) (Session, error) {
	var out Session
	err := c.do(ctx, http.MethodGet, "/api/session/"+url.PathEscape(id), nil, nil, &out)
	return out, err
}

// ListSessions lists sessions, newest first. When directory is non-empty the
// server filters by location.
func (c *Client) ListSessions(ctx context.Context, directory string, limit int) ([]Session, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	q.Set("order", "desc")
	if directory != "" {
		q.Set("directory", directory)
	}
	var out []Session
	err := c.do(ctx, http.MethodGet, "/api/session", q, nil, &out)
	return out, err
}

// ActiveSessions returns the set of session IDs currently draining.
func (c *Client) ActiveSessions(ctx context.Context) (map[string]bool, error) {
	var raw map[string]json.RawMessage
	if err := c.do(ctx, http.MethodGet, "/api/session/active", nil, nil, &raw); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(raw))
	for id := range raw {
		out[id] = true
	}
	return out, nil
}

// Prompt sends text to a session. The server admits it durably and schedules
// the agent loop.
func (c *Client) Prompt(ctx context.Context, sessionID, text string) error {
	body := map[string]any{"text": text}
	return c.do(ctx, http.MethodPost, "/api/session/"+url.PathEscape(sessionID)+"/prompt", nil, body, nil)
}

// Messages reads a page of session messages. order is "asc" or "desc".
func (c *Client) Messages(ctx context.Context, sessionID string, limit int, order string) ([]Message, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprintf("%d", limit))
	}
	if order != "" {
		q.Set("order", order)
	}
	var out []Message
	err := c.do(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID)+"/message", q, nil, &out)
	return out, err
}

// Diff returns the session's diff payload as raw JSON.
func (c *Client) Diff(ctx context.Context, sessionID string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.do(ctx, http.MethodGet, "/api/session/"+url.PathEscape(sessionID)+"/diff", nil, nil, &out)
	return out, err
}

// Wait blocks using the server's wait route, which knows about queued inputs.
// It is intentionally unbounded by the client timeout; the caller's context is
// the deadline.
func (c *Client) Wait(ctx context.Context, sessionID string) error {
	return c.doWithTimeout(ctx, false, http.MethodPost,
		"/api/experimental/session/"+url.PathEscape(sessionID)+"/wait", nil, nil, nil)
}

// WaitIdle returns when the session is no longer running.
//
// It prefers the server's blocking wait, which accounts for queued inputs, so a
// prompt that has been admitted but not yet started cannot be mistaken for an
// idle session. On an unavailable route or a transient failure it falls back to
// polling. Polling cannot miss the transition and is cheap at a few seconds. The
// grace period covers the window between sending a prompt and the session
// appearing in the active set.
func (c *Client) WaitIdle(ctx context.Context, sessionID string, poll time.Duration) error {
	if poll <= 0 {
		poll = 2 * time.Second
	}

	err := c.Wait(ctx, sessionID)
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	started := false
	grace := time.Now().Add(30 * time.Second)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		active, aerr := c.ActiveSessions(ctx)
		if aerr == nil {
			if active[sessionID] {
				started = true
			} else if started || time.Now().After(grace) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// -- one-shot generation ----------------------------------------------------

// GenerateText runs one stateless model call. model may be nil to use the
// server default.
func (c *Client) GenerateText(ctx context.Context, prompt string, model *ModelRef) (string, error) {
	body := map[string]any{"prompt": prompt}
	if model != nil && !model.IsZero() {
		body["model"] = model
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/experimental/generate/text", nil, body, &out); err != nil {
		return "", err
	}
	return out.Text, nil
}
