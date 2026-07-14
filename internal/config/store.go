// Package config persists the user's alarm settings.
//
// store.go must never import Fyne. Only prefs.go may.
package config

import (
	"fmt"
	"os"
	"time"

	"claudealarm/internal/schedule"
)

// Defaults.
const (
	DefaultModel  = "haiku"
	DefaultPrompt = "Hello world"
	DefaultHour   = 7
	DefaultMinute = 30
	DefaultOffset = 5 * time.Minute
	DefaultGrace  = 5 * time.Minute
)

// State is everything that survives a restart.
//
// FireAt and Target are persisted alongside the Spec so that a restart can tell
// the difference between "the alarm is still pending" and "the alarm came due
// while we were not running". They are always recomputed from the Spec on load;
// the stored copies exist only to detect a missed alarm.
type State struct {
	Spec schedule.Spec

	WorkDir string
	Model   string
	Prompt  string

	Armed  bool
	FireAt time.Time
	Target time.Time
}

// DefaultState returns a valid, disarmed State for a fresh install.
func DefaultState(workDir string) State {
	return State{
		Spec: schedule.Spec{
			Hour:   DefaultHour,
			Minute: DefaultMinute,
			Zone:   "", // system local
			Offset: DefaultOffset,
			Grace:  DefaultGrace,
		},
		WorkDir: workDir,
		Model:   DefaultModel,
		Prompt:  DefaultPrompt,
		Armed:   false,
	}
}

// Validate reports whether the State is well-formed and armable.
func (s State) Validate() error {
	if err := s.Spec.Validate(); err != nil {
		return err
	}
	if s.WorkDir == "" {
		return fmt.Errorf("working directory is empty")
	}
	if s.Model == "" {
		return fmt.Errorf("model is empty")
	}
	if s.Prompt == "" {
		return fmt.Errorf("prompt is empty")
	}
	return nil
}

// ValidateWorkDir checks the working directory exists and is a directory. It is
// separate from Validate because it touches the filesystem, and we only want to
// pay for that at arm time.
func (s State) ValidateWorkDir() error {
	fi, err := os.Stat(s.WorkDir)
	if err != nil {
		return fmt.Errorf("working directory %q: %w", s.WorkDir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("working directory %q is not a directory", s.WorkDir)
	}
	return nil
}

// Store persists State. It is the seam that lets the app be tested without disk.
type Store interface {
	Load() (State, error)
	Save(State) error
}

// MemStore is an in-memory Store for tests.
type MemStore struct{ state State }

// NewMemStore returns a MemStore seeded with the given State.
func NewMemStore(s State) *MemStore { return &MemStore{state: s} }

func (m *MemStore) Load() (State, error) { return m.state, nil }

func (m *MemStore) Save(s State) error { m.state = s; return nil }

// MustLoad loads the State, panicking on error. It exists for tests that want
// a State without threading an error check through every call site.
func (m *MemStore) MustLoad() State {
	s, err := m.Load()
	if err != nil {
		panic(err)
	}
	return s
}
