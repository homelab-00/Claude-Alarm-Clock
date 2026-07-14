package config

import (
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"claudealarm/internal/schedule"
)

func TestPrefsStoreRoundTripsThroughFynePreferences(t *testing.T) {
	a := test.NewApp()
	defer test.NewApp() // reset global app state for other tests

	st := NewPrefsStore(a.Preferences())

	want := State{
		Spec: schedule.Spec{
			Hour: 7, Minute: 30,
			Zone:   "Europe/Athens",
			Offset: 20 * time.Minute,
			Grace:  5 * time.Minute,
		},
		WorkDir: "/home/Bill/project",
		Model:   "haiku",
		Prompt:  "Hello world",
		Armed:   true,
		FireAt:  time.Date(2026, 7, 15, 7, 10, 0, 0, time.UTC),
		Target:  time.Date(2026, 7, 15, 7, 30, 0, 0, time.UTC),
	}
	if err := st.Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := st.Load()
	if err != nil {
		t.Fatal(err)
	}

	if got.Spec != want.Spec {
		t.Fatalf("Spec = %+v, want %+v", got.Spec, want.Spec)
	}
	if got.WorkDir != want.WorkDir || got.Model != want.Model || got.Prompt != want.Prompt {
		t.Fatalf("strings did not round-trip: %+v", got)
	}
	if !got.Armed {
		t.Fatal("Armed did not round-trip")
	}
	if !got.FireAt.Equal(want.FireAt) {
		t.Fatalf("FireAt = %v, want %v", got.FireAt, want.FireAt)
	}
	if !got.Target.Equal(want.Target) {
		t.Fatalf("Target = %v, want %v", got.Target, want.Target)
	}
}

// A fresh install has nothing stored. Load must return usable defaults, not an
// error and not a zero State.
func TestPrefsStoreLoadOnFreshInstallReturnsDefaults(t *testing.T) {
	a := test.NewApp()
	defer test.NewApp()

	got, err := NewPrefsStore(a.Preferences()).Load()
	if err != nil {
		t.Fatalf("Load() on a fresh install must not error: %v", err)
	}

	if got.Model != "haiku" {
		t.Fatalf("Model = %q, want the default haiku", got.Model)
	}
	if got.Prompt != "Hello world" {
		t.Fatalf("Prompt = %q, want the default", got.Prompt)
	}
	if got.Spec.Grace != 5*time.Minute {
		t.Fatalf("Grace = %v, want the default 5m", got.Spec.Grace)
	}
	if got.Armed {
		t.Fatal("a fresh install must not be armed")
	}
	if got.WorkDir == "" {
		t.Fatal("WorkDir must default to something, not empty")
	}
}
