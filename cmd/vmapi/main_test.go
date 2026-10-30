package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunPrintsVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d, stderr = %q", code, stderr.String())
	}
	if got, want := strings.TrimSpace(stdout.String()), "vmapi "+version; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestRunRejectsUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--no-such-flag"}, &stdout, &stderr); code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
}

func TestRunRefusesAnInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("storage: {dir: relative}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--config", path}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	for _, field := range []string{"storage.dir", "network.bridge", "images"} {
		if !strings.Contains(stderr.String(), field) {
			t.Errorf("stderr does not name %s:\n%s", field, stderr.String())
		}
	}
}

func TestRunReadsTheDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "vmapi", "config.yaml")) {
		t.Errorf("stderr does not name the default path:\n%s", stderr.String())
	}
}
