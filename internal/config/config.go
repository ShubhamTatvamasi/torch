// Package config discovers the running OpenCode service and reads .torch
// settings.
//
// Discovery order, lowest precedence first:
//
//	~/.config/opencode/service.json
//	~/.local/state/opencode/service.json
//	<repo>/.torch/config.json          (judge model, agent, budgets)
//	environment variables              (highest)
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Settings is everything the supervisor needs to talk to OpenCode and to
// decide how much autonomy to grant.
type Settings struct {
	URL        string  `json:"url"`
	Username   string  `json:"username"`
	Password   string  `json:"password"`
	Model      string  `json:"judge_model,omitempty"`
	Agent      string  `json:"agent,omitempty"`
	MaxSteps   int     `json:"max_steps,omitempty"`
	MaxCost    float64 `json:"max_cost_usd,omitempty"`
	MaxMinutes float64 `json:"max_minutes,omitempty"`
}

type serviceFile struct {
	URL      string `json:"url"`
	Username string `json:"username"`
	Password string `json:"password"`
}

type projectConfig struct {
	JudgeModel string  `json:"judge_model"`
	Agent      string  `json:"agent"`
	MaxSteps   int     `json:"max_steps"`
	MaxCost    float64 `json:"max_cost_usd"`
	MaxMinutes float64 `json:"max_minutes"`
}

// Paths resolves the conventional locations. It is a variable so tests can
// redirect discovery.
var Paths = func() (stateFile, privateFile, openCodeConfig string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", ""
	}
	stateFile = filepath.Join(home, ".local", "state", "opencode", "service.json")
	privateFile = filepath.Join(home, ".config", "opencode", "service.json")
	openCodeConfig = filepath.Join(home, ".config", "opencode", "opencode.json")
	return
}

// Defaults applied when the project does not pin a value.
const (
	DefaultMaxSteps   = 10
	DefaultMaxMinutes = 240
)

// Load builds Settings for a repository root.
func Load(repo string) (Settings, error) {
	stateFile, privateFile, openCodeConfig := Paths()

	state := readService(stateFile)
	private := readService(privateFile)

	s := Settings{
		URL:        firstNonEmpty(state.URL, private.URL),
		Username:   firstNonEmpty(state.Username, private.Username, "opencode"),
		Password:   firstNonEmpty(state.Password, private.Password),
		MaxSteps:   DefaultMaxSteps,
		MaxMinutes: DefaultMaxMinutes,
	}

	if pc, ok := readProject(repo); ok {
		s.Model = pc.JudgeModel
		s.Agent = pc.Agent
		if pc.MaxSteps > 0 {
			s.MaxSteps = pc.MaxSteps
		}
		if pc.MaxCost > 0 {
			s.MaxCost = pc.MaxCost
		}
		if pc.MaxMinutes > 0 {
			s.MaxMinutes = pc.MaxMinutes
		}
	}

	if s.Model == "" {
		s.Model = globalModel(openCodeConfig)
	}

	// Environment wins over every file.
	s.URL = firstNonEmpty(os.Getenv("OPENCODE_URL"), s.URL)
	s.Password = firstNonEmpty(os.Getenv("OPENCODE_PASSWORD"), s.Password)
	s.Username = firstNonEmpty(os.Getenv("OPENCODE_USERNAME"), s.Username)
	s.Model = firstNonEmpty(os.Getenv("TORCH_MODEL"), os.Getenv("SUPER_MODEL"), s.Model)
	s.Agent = firstNonEmpty(os.Getenv("TORCH_AGENT"), s.Agent)

	s.URL = strings.TrimRight(strings.TrimSpace(s.URL), "/")
	if s.URL == "" {
		return Settings{}, fmt.Errorf(
			"no OpenCode service found: start it with `opencode service start` or set OPENCODE_URL")
	}
	if s.Password == "" {
		return Settings{}, fmt.Errorf(
			"no service password found: set OPENCODE_PASSWORD or check %s", stateFile)
	}
	if s.MaxSteps <= 0 {
		s.MaxSteps = DefaultMaxSteps
	}
	if s.MaxMinutes <= 0 {
		s.MaxMinutes = DefaultMaxMinutes
	}
	return s, nil
}

func readService(path string) serviceFile {
	if path == "" {
		return serviceFile{}
	}
	var out serviceFile
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	_ = json.Unmarshal(data, &out)
	return out
}

func readProject(repo string) (projectConfig, bool) {
	var pc projectConfig
	path := filepath.Join(repo, ".torch", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return pc, false
	}
	if err := json.Unmarshal(data, &pc); err != nil {
		return projectConfig{}, false
	}
	return pc, true
}

// globalModel reads OpenCode's global default model, which may be a string
// ("provider/model") or an object ({"providerID":..., "id":...}).
func globalModel(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Model json.RawMessage `json:"model"`
	}
	if err := json.Unmarshal(data, &doc); err != nil || len(doc.Model) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(doc.Model, &asString); err == nil {
		return asString
	}
	var asObject struct {
		ProviderID string `json:"providerID"`
		ID         string `json:"id"`
	}
	if err := json.Unmarshal(doc.Model, &asObject); err == nil && asObject.ID != "" {
		if asObject.ProviderID != "" {
			return asObject.ProviderID + "/" + asObject.ID
		}
		return asObject.ID
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
