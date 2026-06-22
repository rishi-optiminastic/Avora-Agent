// Package config persists the agent's state — the device token, the monotonic
// sequence high-water mark, and the Avora base URLs — in ~/.avora/agent.json
// with owner-only permissions (0600). The token is a per-device secret, so the
// file is treated like a credential.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Overridable at build time so release binaries ship pointing at production:
//
//	go build -ldflags "-X avora-agent/internal/config.defaultFEURL=https://… \
//	                   -X avora-agent/internal/config.defaultAPIURL=https://…"
//
// Left at localhost for local dev. AVORA_FE_URL / AVORA_API_URL still override
// at runtime.
var (
	defaultFEURL  = "http://localhost:3000"
	defaultAPIURL = "http://localhost:8000"
)

// Config is the on-disk agent state.
type Config struct {
	DeviceToken string `json:"device_token"`
	Sequence    int    `json:"sequence"`
	FEBaseURL   string `json:"fe_base_url"`
	APIBaseURL  string `json:"api_base_url"`
	// PersonalMode pauses all capture (activity + screenshots) when true. Set
	// from server mode_* commands and persisted so it survives a restart. The
	// server also drops anything captured while the employee is in personal
	// mode, so this is an optimization, not the privacy boundary.
	PersonalMode bool `json:"personal_mode"`
}

// Dir is ~/.avora.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".avora"), nil
}

func path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agent.json"), nil
}

// Load reads persisted state (device token + sequence). A missing file is not
// an error — it yields a fresh config.
//
// The FE/API URLs are ALWAYS taken from the build (ldflags defaults) + env
// override, never from the persisted file — so a binary always talks to the
// backend it was built for, even if an older config on disk points elsewhere
// (e.g. a leftover localhost config from dev).
func Load() (*Config, error) {
	cfg := &Config{}
	p, err := path()
	if err != nil {
		return nil, err
	}
	switch data, err := os.ReadFile(p); {
	case err == nil:
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	cfg.FEBaseURL = envOr("AVORA_FE_URL", defaultFEURL)
	cfg.APIBaseURL = envOr("AVORA_API_URL", defaultAPIURL)
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Save writes the config back with 0600 perms (0700 dir).
func (c *Config) Save() error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "agent.json"), data, 0o600)
}

// Enrolled reports whether a device token is present.
func (c *Config) Enrolled() bool { return c.DeviceToken != "" }
