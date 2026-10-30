package main

import (
	"bytes"
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
