// Package config owns everything the CLI persists outside the state database:
// XDG paths, config.toml (hand-edited, world-readable) and credentials.toml
// (sessions, 0600). The two files are kept apart so that sharing or committing
// a config never leaks a session.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/adrg/xdg"
	"github.com/pelletier/go-toml/v2"
)

const appDir = "wallapop-cli"

// Paths resolves where each file lives. WALLAPOP_CONFIG moves only config.toml;
// credentials and state follow XDG so a relocated config cannot point the CLI
// at someone else's secrets.
type Paths struct {
	ConfigFile      string
	CredentialsFile string
	StateDB         string
	StateDir        string
}

func DefaultPaths() Paths {
	// adrg/xdg snapshots the environment at init; re-read it so XDG_* set by
	// the caller's environment (or a test) is honoured at call time.
	xdg.Reload()
	p := Paths{
		ConfigFile:      filepath.Join(xdg.ConfigHome, appDir, "config.toml"),
		CredentialsFile: filepath.Join(xdg.ConfigHome, appDir, "credentials.toml"),
		StateDB:         filepath.Join(xdg.DataHome, appDir, "state.db"),
		StateDir:        filepath.Join(xdg.StateHome, appDir),
	}
	if v := os.Getenv("WALLAPOP_CONFIG"); v != "" {
		p.ConfigFile = v
	}
	return p
}

// Location is a search centre. RadiusKm 0 means "let Wallapop decide".
type Location struct {
	Lat      float64 `toml:"lat" json:"lat"`
	Lng      float64 `toml:"lng" json:"lng"`
	RadiusKm int     `toml:"radius_km,omitempty" json:"radius_km,omitempty"`
}

type ProfileConfig struct {
	Location *Location `toml:"location,omitempty" json:"location,omitempty"`
}

// SinkConfig is one notification destination. Type selects which fields matter:
// ntfy uses URL+Topic(+TokenFile), webhook uses URL(+Template), exec uses Command.
type SinkConfig struct {
	Type      string   `toml:"type" json:"type"`
	URL       string   `toml:"url,omitempty" json:"url,omitempty"`
	Topic     string   `toml:"topic,omitempty" json:"topic,omitempty"`
	TokenFile string   `toml:"token_file,omitempty" json:"token_file,omitempty"`
	Template  string   `toml:"template,omitempty" json:"template,omitempty"`
	Command   []string `toml:"command,omitempty" json:"command,omitempty"`
}

type WatchConfig struct {
	Interval string `toml:"interval,omitempty" json:"interval,omitempty"`
}

type Config struct {
	DefaultProfile string                   `toml:"default_profile,omitempty" json:"default_profile,omitempty"`
	Profiles       map[string]ProfileConfig `toml:"profiles,omitempty" json:"profiles,omitempty"`
	Watch          WatchConfig              `toml:"watch,omitempty" json:"watch,omitempty"`
	Sinks          map[string]SinkConfig    `toml:"sinks,omitempty" json:"sinks,omitempty"`
}

// Load reads config.toml. A missing file is an empty config, not an error.
// Unknown keys are errors so typos surface instead of silently doing nothing.
func Load(path string) (Config, error) {
	var cfg Config
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	dec := toml.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

func Save(path string, cfg Config) error {
	out, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeAtomic(path, out, 0o644)
}

// Session is what lets the CLI act as one Wallapop account. The cookie is the
// only secret; access tokens are minted from it and never stored.
type Session struct {
	SessionCookie string    `toml:"session_cookie" json:"-"`
	DeviceID      string    `toml:"device_id" json:"device_id"`
	UserHash      string    `toml:"user_hash" json:"user_hash"`
	Name          string    `toml:"name" json:"name"`
	UpdatedAt     time.Time `toml:"updated_at" json:"updated_at"`
}

type Credentials struct {
	Profiles map[string]Session `toml:"profiles"`
}

func LoadCredentials(path string) (Credentials, error) {
	creds := Credentials{Profiles: map[string]Session{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return creds, nil
	}
	if err != nil {
		return creds, err
	}
	if err := toml.Unmarshal(raw, &creds); err != nil {
		return creds, fmt.Errorf("%s: %w", path, err)
	}
	if creds.Profiles == nil {
		creds.Profiles = map[string]Session{}
	}
	return creds, nil
}

func SaveCredentials(path string, creds Credentials) error {
	out, err := toml.Marshal(creds)
	if err != nil {
		return err
	}
	return writeAtomic(path, out, 0o600)
}

// writeAtomic writes to a sibling temp file and renames, so a crash never
// leaves a half-written config or credentials file. The mode is applied before
// any bytes land, and again afterwards in case the file already existed.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return err
	}
	return os.Chmod(path, mode)
}

// ResolveProfile picks the active profile name: flag, then env, then config
// default, then "default".
func ResolveProfile(flag string, cfg Config) string {
	switch {
	case flag != "":
		return flag
	case os.Getenv("WALLAPOP_PROFILE") != "":
		return os.Getenv("WALLAPOP_PROFILE")
	case cfg.DefaultProfile != "":
		return cfg.DefaultProfile
	}
	return "default"
}

// UserHome is the home directory, or "." if it cannot be determined.
func UserHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}
