package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	want := DefaultConfig()
	want.Logger.Level = "debug"
	if err := Save(path, want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadRejectsNoOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("logger:\n  console: false\n  file: false\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want validation error")
	}
}

func TestNewWritesToFile(t *testing.T) {
	config := DefaultConfig()
	config.Logger.Console = false
	config.Logger.FileConfig.Filename = filepath.Join(t.TempDir(), "thingsmodel.log")
	logger, closer, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	logger.Info("file output test")
	if err := closer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	contents, err := os.ReadFile(config.Logger.FileConfig.Filename)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(contents), "file output test") {
		t.Fatalf("log output = %q, want message", contents)
	}
}
