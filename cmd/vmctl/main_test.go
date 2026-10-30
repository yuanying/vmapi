package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/server"
	"github.com/yuanying/vmapi/internal/vm"
	"github.com/yuanying/vmapi/internal/vm/vmtest"
)

const token = "s3cr3t-token-value"

// fakeProxy stands in for devbox proxy: it takes the bearer token of
// "pi" and passes the request on as that user. A bad token is answered
// with the token in the message, as a careless server might.
func fakeProxy(t *testing.T) (*httptest.Server, *vmtest.Env) {
	t.Helper()
	e := vmtest.New(t, vmtest.Config(t))
	h := (&server.Server{Service: e.Service, Logger: slog.New(slog.DiscardHandler)}).Handler()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got != "Bearer "+token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(api.ErrorBody{Error: api.Error{Code: "unauthenticated", Message: "bad token " + got}})
			return
		}
		r.Header.Set("X-Devbox-User", "pi")
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, e
}

type result struct {
	code           int
	stdout, stderr string
}

func vmctl(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(args, strings.NewReader(stdin), &stdout, &stderr)
	r := result{code, stdout.String(), stderr.String()}
	if strings.Contains(r.stdout+r.stderr, token) {
		t.Errorf("the token was written out:\nstdout: %s\nstderr: %s", r.stdout, r.stderr)
	}
	return r
}

func setup(t *testing.T) (*httptest.Server, *vmtest.Env) {
	t.Helper()
	srv, e := fakeProxy(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("VMCTL_SERVER", srv.URL)
	t.Setenv("VMCTL_TOKEN", token)
	t.Setenv("VMCTL_TOKEN_FILE", "")
	return srv, e
}

func ok(t *testing.T, r result) result {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	return r
}

func TestRunPrintsVersion(t *testing.T) {
	r := vmctl(t, "", "--version")
	if r.code != 0 || strings.TrimSpace(r.stdout) != "vmctl "+version {
		t.Errorf("%+v", r)
	}
}

func TestRunRejectsUnknownFlagsAndCommands(t *testing.T) {
	setup(t)
	if r := vmctl(t, "", "--no-such-flag"); r.code != 2 {
		t.Errorf("unknown flag: %+v", r)
	}
	if r := vmctl(t, "", "reboot", "a"); r.code != 2 {
		t.Errorf("unknown command: %+v", r)
	}
	if r := vmctl(t, ""); r.code != 2 {
		t.Errorf("no command: %+v", r)
	}
	if r := vmctl(t, "", "get"); r.code != 2 {
		t.Errorf("get without a name: %+v", r)
	}
	if r := vmctl(t, "", "create", "a"); r.code != 2 {
		t.Errorf("create without --image: %+v", r)
	}
}

func TestWhoamiAndImages(t *testing.T) {
	setup(t)
	r := ok(t, vmctl(t, "", "whoami"))
	if !strings.Contains(r.stdout, "pi") {
		t.Errorf("whoami: %s", r.stdout)
	}
	r = ok(t, vmctl(t, "", "-o", "json", "whoami"))
	var w api.Whoami
	if err := json.Unmarshal([]byte(r.stdout), &w); err != nil || w.User != "pi" {
		t.Errorf("whoami json: %s %v", r.stdout, err)
	}
	r = ok(t, vmctl(t, "", "images"))
	if !strings.Contains(r.stdout, "noble") || !strings.Contains(r.stdout, "Ubuntu 24.04 LTS") {
		t.Errorf("images: %s", r.stdout)
	}
}

func TestTheVMLifecycle(t *testing.T) {
	_, e := setup(t)
	userData := filepath.Join(t.TempDir(), "user-data")
	if err := os.WriteFile(userData, []byte("#cloud-config\npackages: [jq]\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := ok(t, vmctl(t, "", "create", "k8s-cp1", "--image", "noble", "--vcpus", "4", "--memory-mib", "4096", "--disk-gib", "30", "--ip", "192.168.40.20", "--user-data", userData))
	for _, want := range []string{"k8s-cp1", "192.168.40.20", "running"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("create output lacks %q: %s", want, r.stdout)
		}
	}
	v, err := e.Service.Get(t.Context(), adminCaller, "k8s-cp1")
	if err != nil || v.VCPUs != 4 || v.MemoryMiB != 4096 || v.DiskGiB != 30 || v.Owner != "pi" {
		t.Errorf("created %+v, %v", v, err)
	}
	seed, _ := e.Seeds.Seed(filepath.Join(e.Service.Config.Storage.Dir, "vms", "k8s-cp1", "seed.iso"))
	if string(seed.UserData) != "#cloud-config\npackages: [jq]\n" {
		t.Errorf("user-data = %q", seed.UserData)
	}

	// user-data from stdin, flags before the name.
	ok(t, vmctl(t, "#!/bin/sh\necho hi\n", "create", "--image", "jammy", "--user-data", "-", "k8s-w1"))
	seed, _ = e.Seeds.Seed(filepath.Join(e.Service.Config.Storage.Dir, "vms", "k8s-w1", "seed.iso"))
	if string(seed.UserData) != "#!/bin/sh\necho hi\n" {
		t.Errorf("user-data from stdin = %q", seed.UserData)
	}

	r = ok(t, vmctl(t, "", "list"))
	if !strings.Contains(r.stdout, "k8s-cp1") || !strings.Contains(r.stdout, "k8s-w1") {
		t.Errorf("list: %s", r.stdout)
	}
	r = ok(t, vmctl(t, "", "-o", "json", "list"))
	var list api.VMList
	if err := json.Unmarshal([]byte(r.stdout), &list); err != nil || len(list.VMs) != 2 {
		t.Errorf("list json: %s %v", r.stdout, err)
	}

	r = ok(t, vmctl(t, "", "get", "k8s-cp1"))
	if !strings.Contains(r.stdout, "192.168.40.20") {
		t.Errorf("get: %s", r.stdout)
	}

	r = ok(t, vmctl(t, "", "stop", "k8s-cp1"))
	if !strings.Contains(r.stdout, "shutting_down") {
		t.Errorf("stop: %s", r.stdout)
	}
	r = ok(t, vmctl(t, "", "stop", "--force", "k8s-cp1"))
	if !strings.Contains(r.stdout, "shut_off") {
		t.Errorf("stop --force: %s", r.stdout)
	}
	r = ok(t, vmctl(t, "", "start", "k8s-cp1"))
	if !strings.Contains(r.stdout, "running") {
		t.Errorf("start: %s", r.stdout)
	}

	ok(t, vmctl(t, "", "snapshot", "create", "k8s-cp1", "before"))
	r = ok(t, vmctl(t, "", "snapshot", "list", "k8s-cp1"))
	if !strings.Contains(r.stdout, "before") {
		t.Errorf("snapshot list: %s", r.stdout)
	}
	ok(t, vmctl(t, "", "snapshot", "revert", "k8s-cp1", "before"))
	ok(t, vmctl(t, "", "snapshot", "delete", "k8s-cp1", "before"))

	ok(t, vmctl(t, "", "delete", "k8s-cp1"))
	r = vmctl(t, "", "get", "k8s-cp1")
	if r.code != 1 || !strings.Contains(r.stderr, "not_found") {
		t.Errorf("get after delete: %+v", r)
	}
}

func TestOthersVMs(t *testing.T) {
	_, e := setup(t)
	if _, err := e.Service.Create(t.Context(), vm.Caller{User: "penelope"}, api.CreateVMRequest{Name: "p1", Image: "noble"}); err != nil {
		t.Fatal(err)
	}
	r := ok(t, vmctl(t, "", "list", "--owner", "penelope"))
	if !strings.Contains(r.stdout, "OWNER") || !strings.Contains(r.stdout, "p1") || !strings.Contains(r.stdout, "penelope") {
		t.Errorf("list --owner: %s", r.stdout)
	}
	r = ok(t, vmctl(t, "", "list", "--owner", "pi"))
	if strings.Contains(r.stdout, "p1") {
		t.Errorf("list --owner pi: %s", r.stdout)
	}
	r = vmctl(t, "", "delete", "p1")
	if r.code != 1 || !strings.Contains(r.stderr, "permission_denied") {
		t.Errorf("delete p1: %+v", r)
	}
}

func TestErrorsCarryTheCode(t *testing.T) {
	setup(t)
	r := vmctl(t, "", "create", "a", "--image", "plucky")
	if r.code != 1 || !strings.Contains(r.stderr, "not_found") || !strings.Contains(r.stderr, "plucky") {
		t.Errorf("%+v", r)
	}
}

func TestTheTokenComesFromAFileOfTheUsersOwn(t *testing.T) {
	setup(t)
	t.Setenv("VMCTL_TOKEN", "")
	dir := os.Getenv("XDG_CONFIG_HOME")
	path := filepath.Join(dir, "vmctl", "token")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}

	// None at all.
	r := vmctl(t, "", "whoami")
	if r.code != 1 || !strings.Contains(r.stderr, path) {
		t.Errorf("no token: %+v", r)
	}

	// The default file, with the newline an editor leaves.
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ok(t, vmctl(t, "", "whoami"))

	// Readable by others: refused.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	r = vmctl(t, "", "whoami")
	if r.code != 1 || !strings.Contains(r.stderr, "0600") {
		t.Errorf("0644 token file: %+v", r)
	}

	// Another file, by flag or by environment.
	other := filepath.Join(t.TempDir(), "pi.token")
	if err := os.WriteFile(other, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	ok(t, vmctl(t, "", "--token-file", other, "whoami"))
	t.Setenv("VMCTL_TOKEN_FILE", other)
	ok(t, vmctl(t, "", "whoami"))
}

func TestABadTokenIsNotEchoed(t *testing.T) {
	setup(t)
	t.Setenv("VMCTL_TOKEN", token+"-wrong")
	var stdout, stderr bytes.Buffer
	code := run([]string{"whoami"}, strings.NewReader(""), &stdout, &stderr)
	if code != 1 || strings.Contains(stdout.String()+stderr.String(), token) {
		t.Errorf("exit %d, output %q %q", code, stdout.String(), stderr.String())
	}
}

func TestTheServerFlag(t *testing.T) {
	srv, _ := setup(t)
	t.Setenv("VMCTL_SERVER", "http://127.0.0.1:1")
	ok(t, vmctl(t, "", "--server", srv.URL, "whoami"))
}

var adminCaller = vm.Caller{User: "admin", Admin: true}

func TestErrorsWithoutATokenAreLeftAlone(t *testing.T) {
	setup(t)
	t.Setenv("VMCTL_TOKEN", "")
	r := vmctl(t, "", "whoami")
	if r.code != 1 || strings.Contains(r.stderr, "[token]") {
		t.Errorf("%+v", r)
	}
}

func TestHelp(t *testing.T) {
	r := vmctl(t, "", "-h")
	if r.code != 0 || !strings.Contains(r.stdout, "snapshot create") {
		t.Errorf("%+v", r)
	}
}
