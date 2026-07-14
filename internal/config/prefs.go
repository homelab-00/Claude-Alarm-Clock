package config

import (
	"os"
	"time"

	"fyne.io/fyne/v2"

	"claudealarm/internal/schedule"
)

// Preference keys. Namespaced so they cannot collide with Fyne's own.
const (
	keyHour    = "alarm.hour"
	keyMinute  = "alarm.minute"
	keyZone    = "alarm.zone"
	keyOffset  = "alarm.offsetSeconds"
	keyGrace   = "alarm.graceSeconds"
	keyWorkDir = "alarm.workDir"
	keyModel   = "alarm.model"
	keyPrompt  = "alarm.prompt"
	keyArmed   = "alarm.armed"
	keyFireAt  = "alarm.fireAtRFC3339"
	keyTarget  = "alarm.targetRFC3339"
)

// PrefsStore persists State via fyne.Preferences, which writes
// $XDG_CONFIG_HOME/<appID>/preferences.json.
//
// Requires the app to have been created with app.NewWithID -- Preferences()
// does not work otherwise.
//
// Durations are stored as whole seconds and times as RFC3339 strings, because
// fyne.Preferences only handles bool/int/float/string.
type PrefsStore struct{ p fyne.Preferences }

// NewPrefsStore returns a Store backed by Fyne's preferences.
func NewPrefsStore(p fyne.Preferences) *PrefsStore { return &PrefsStore{p: p} }

// Load reads the stored State, falling back to defaults for anything absent.
// A fresh install returns a valid default State, not an error.
func (s *PrefsStore) Load() (State, error) {
	def := DefaultState(defaultWorkDir())

	st := State{
		Spec: schedule.Spec{
			Hour:   s.p.IntWithFallback(keyHour, def.Spec.Hour),
			Minute: s.p.IntWithFallback(keyMinute, def.Spec.Minute),
			Zone:   s.p.StringWithFallback(keyZone, def.Spec.Zone),
			Offset: time.Duration(s.p.IntWithFallback(keyOffset, int(def.Spec.Offset/time.Second))) * time.Second,
			Grace:  time.Duration(s.p.IntWithFallback(keyGrace, int(def.Spec.Grace/time.Second))) * time.Second,
		},
		WorkDir: s.p.StringWithFallback(keyWorkDir, def.WorkDir),
		Model:   s.p.StringWithFallback(keyModel, def.Model),
		Prompt:  s.p.StringWithFallback(keyPrompt, def.Prompt),
		Armed:   s.p.BoolWithFallback(keyArmed, false),
	}

	st.FireAt = parseTime(s.p.StringWithFallback(keyFireAt, ""))
	st.Target = parseTime(s.p.StringWithFallback(keyTarget, ""))

	return st, nil
}

// Save writes the State.
func (s *PrefsStore) Save(st State) error {
	s.p.SetInt(keyHour, st.Spec.Hour)
	s.p.SetInt(keyMinute, st.Spec.Minute)
	s.p.SetString(keyZone, st.Spec.Zone)
	s.p.SetInt(keyOffset, int(st.Spec.Offset/time.Second))
	s.p.SetInt(keyGrace, int(st.Spec.Grace/time.Second))
	s.p.SetString(keyWorkDir, st.WorkDir)
	s.p.SetString(keyModel, st.Model)
	s.p.SetString(keyPrompt, st.Prompt)
	s.p.SetBool(keyArmed, st.Armed)
	s.p.SetString(keyFireAt, formatTime(st.FireAt))
	s.p.SetString(keyTarget, formatTime(st.Target))
	return nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// defaultWorkDir is the process's current directory -- "this current dir", per
// the spec. Falls back to $HOME if that somehow fails.
func defaultWorkDir() string {
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	home, _ := os.UserHomeDir()
	return home
}
