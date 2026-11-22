package opencode

import "encoding/json"

// ModelRef identifies a provider model, optionally with a variant.
type ModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

// String renders provider/model, including the variant when present.
func (m ModelRef) String() string {
	s := m.ProviderID + "/" + m.ID
	if m.Variant != "" {
		s += "#" + m.Variant
	}
	return s
}

// IsZero reports whether the ref is unset.
func (m ModelRef) IsZero() bool { return m.ID == "" && m.ProviderID == "" }

// CacheUsage is the prompt-cache accounting reported per session/message.
type CacheUsage struct {
	Read  float64 `json:"read"`
	Write float64 `json:"write"`
}

// Tokens is OpenCode's token accounting.
type Tokens struct {
	Input     float64    `json:"input"`
	Output    float64    `json:"output"`
	Reasoning float64    `json:"reasoning"`
	Cache     CacheUsage `json:"cache"`
}

// Location identifies where a session runs.
type Location struct {
	Directory string `json:"directory,omitempty"`
}

// SessionTime is epoch milliseconds for lifecycle timestamps.
type SessionTime struct {
	Created  float64 `json:"created"`
	Updated  float64 `json:"updated"`
	Busy     float64 `json:"busy,omitempty"`
	Idle     float64 `json:"idle,omitempty"`
	Viewed   float64 `json:"viewed,omitempty"`
	Archived float64 `json:"archived,omitempty"`
}

// Session is a conversation on the OpenCode server.
type Session struct {
	ID        string      `json:"id"`
	ParentID  string      `json:"parentID,omitempty"`
	ProjectID string      `json:"projectID,omitempty"`
	Agent     string      `json:"agent,omitempty"`
	Model     *ModelRef   `json:"model,omitempty"`
	Cost      float64     `json:"cost"`
	Tokens    Tokens      `json:"tokens"`
	Outcome   string      `json:"outcome,omitempty"`
	Time      SessionTime `json:"time"`
	Title     string      `json:"title,omitempty"`
	Location  Location    `json:"location,omitempty"`
	Archived  bool        `json:"-"`
}

// Active is the value returned for a running session.
type Active struct {
	Type string `json:"type"`
}

// ContentPart is one block of an assistant message: text, reasoning, or a tool
// call. Unused fields stay empty.
type ContentPart struct {
	Type  string          `json:"type"`
	Text  string          `json:"text,omitempty"`
	Name  string          `json:"name,omitempty"`
	State json.RawMessage `json:"state,omitempty"`
}

// ToolState is the part of a tool call's state the transcript needs.
type ToolState struct {
	Status string `json:"status"`
	Error  struct {
		Name    string `json:"name,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
}

// Message is a superset of the message variants OpenCode returns. Only the
// fields the supervisor uses are declared.
type Message struct {
	ID      string        `json:"id"`
	Type    string        `json:"type"`
	Text    string        `json:"text,omitempty"`
	Time    SessionTime   `json:"time"`
	Content []ContentPart `json:"content,omitempty"`
	Agent   string        `json:"agent,omitempty"`
	Model   *ModelRef     `json:"model,omitempty"`
	Finish  string        `json:"finish,omitempty"`
	Error   *struct {
		Name    string `json:"name,omitempty"`
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
}

// Model is the subset of /api/model the client uses to resolve references.
type Model struct {
	ID         string    `json:"id"`
	ModelID    string    `json:"modelID"`
	ProviderID string    `json:"providerID"`
	Name       string    `json:"name"`
	Enabled    bool      `json:"enabled"`
	Variants   []Variant `json:"variants"`
}

// Variant is a model variant (for example a reasoning level).
type Variant struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// Agent is an OpenCode agent definition.
type Agent struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Mode        string    `json:"mode"`
	Description string    `json:"description,omitempty"`
	Model       *ModelRef `json:"model,omitempty"`
	Hidden      bool      `json:"hidden"`
}

// Info is the server description.
type Info struct {
	Version string   `json:"version"`
	PID     int      `json:"pid"`
	URLs    []string `json:"urls"`
}
