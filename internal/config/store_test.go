package config

import (
	"testing"
	"time"

	"claudealarm/internal/schedule"
)

func TestDefaultState(t *testing.T) {
	s := DefaultState("/tmp/project")

	if s.WorkDir != "/tmp/project" {
		t.Fatalf("WorkDir = %q", s.WorkDir)
	}
	if s.Model != "haiku" {
		t.Fatalf("Model = %q, want haiku", s.Model)
	}
	if s.Prompt != "Hello world" {
		t.Fatalf("Prompt = %q, want Hello world", s.Prompt)
	}
	if s.Spec.Grace != 5*time.Minute {
		t.Fatalf("Grace = %v, want 5m", s.Spec.Grace)
	}
	if s.Armed {
		t.Fatal("a default state must not be armed")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("the default state must be valid: %v", err)
	}
}

func TestStateValidate(t *testing.T) {
	valid := DefaultState("/tmp")

	tests := []struct {
		name    string
		mutate  func(*State)
		wantErr bool
	}{
		{"default is valid", func(*State) {}, false},
		{"empty workdir", func(s *State) { s.WorkDir = "" }, true},
		{"empty model", func(s *State) { s.Model = "" }, true},
		{"empty prompt", func(s *State) { s.Prompt = "" }, true},
		{"bad hour propagates from Spec", func(s *State) { s.Spec.Hour = 99 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := valid
			tt.mutate(&s)
			if err := s.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMemStoreRoundTrips(t *testing.T) {
	st := NewMemStore(DefaultState("/tmp"))

	want := State{
		Spec:    schedule.Spec{Hour: 7, Minute: 30, Zone: "Europe/Athens", Offset: 20 * time.Minute, Grace: 5 * time.Minute},
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
	if got.Armed != want.Armed {
		t.Fatalf("Armed = %v, want %v", got.Armed, want.Armed)
	}
	if !got.FireAt.Equal(want.FireAt) {
		t.Fatalf("FireAt = %v, want %v", got.FireAt, want.FireAt)
	}
	if !got.Target.Equal(want.Target) {
		t.Fatalf("Target = %v, want %v", got.Target, want.Target)
	}
	if got.WorkDir != want.WorkDir {
		t.Fatalf("WorkDir = %q, want %q", got.WorkDir, want.WorkDir)
	}
}
