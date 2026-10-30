package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRunner records the commands and answers with what respond returns.
type fakeRunner struct {
	calls   [][]string
	respond func(name string, args []string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.calls = append(f.calls, append([]string{name}, args...))
	if f.respond == nil {
		return nil, nil
	}
	return f.respond(name, args)
}

func TestInspect(t *testing.T) {
	r := &fakeRunner{respond: func(string, []string) ([]byte, error) {
		return []byte(`{"virtual-size": 3758096384, "filename": "/d/images/x.img", "format": "qcow2", "actual-size": 600000000}`), nil
	}}
	info, err := Disks{Runner: r}.Inspect(context.Background(), "/d/images/x.img")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if info.Format != "qcow2" || info.VirtualSize != 3758096384 {
		t.Errorf("info = %+v", info)
	}
	want := []string{"qemu-img", "info", "--output=json", "/d/images/x.img"}
	if len(r.calls) != 1 || !slices.Equal(r.calls[0], want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

func TestInspectRejectsAnImageWithABackingFile(t *testing.T) {
	r := &fakeRunner{respond: func(string, []string) ([]byte, error) {
		return []byte(`{"virtual-size": 1, "format": "qcow2", "backing-filename": "/etc/shadow"}`), nil
	}}
	if _, err := (Disks{Runner: r}).Inspect(context.Background(), "/d/images/x.img"); err == nil {
		t.Fatal("Inspect accepted an image with a backing file")
	}
}

func TestInspectPassesOnFailure(t *testing.T) {
	r := &fakeRunner{respond: func(string, []string) ([]byte, error) { return nil, errors.New("boom") }}
	if _, err := (Disks{Runner: r}).Inspect(context.Background(), "/x"); err == nil {
		t.Fatal("Inspect succeeded")
	}
}

func TestCreateOverlay(t *testing.T) {
	r := &fakeRunner{}
	err := Disks{Runner: r}.CreateOverlay(context.Background(), "/d/images/abc.img", "qcow2", "/d/vms/a/disk.qcow2", 20)
	if err != nil {
		t.Fatalf("CreateOverlay: %v", err)
	}
	want := []string{"qemu-img", "create", "-q", "-f", "qcow2", "-b", "/d/images/abc.img", "-F", "qcow2", "/d/vms/a/disk.qcow2", "20G"}
	if len(r.calls) != 1 || !slices.Equal(r.calls[0], want) {
		t.Errorf("calls = %q, want %q", r.calls, want)
	}
}

func TestMakeSeed(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "seed.iso")
	seen := map[string]string{}
	r := &fakeRunner{respond: func(_ string, args []string) ([]byte, error) {
		// The files exist while xorriso runs; read them as it would.
		for _, a := range args {
			base := filepath.Base(a)
			if base == "user-data" || base == "meta-data" || base == "network-config" {
				b, err := os.ReadFile(a)
				if err != nil {
					t.Errorf("reading %s: %v", a, err)
				}
				seen[base] = string(b)
			}
		}
		return nil, nil
	}}
	files := SeedFiles{UserData: []byte("#cloud-config\n"), MetaData: []byte("instance-id: x\n"), NetworkConfig: []byte("version: 2\n")}
	if err := (Seeds{Runner: r}).Make(context.Background(), out, files); err != nil {
		t.Fatalf("Make: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls = %q", r.calls)
	}
	call := r.calls[0]
	prefix := []string{"xorriso", "-as", "mkisofs", "-output", out, "-volid", "cidata", "-joliet", "-rock"}
	if !slices.Equal(call[:len(prefix)], prefix) {
		t.Errorf("call = %q, want it to start with %q", call, prefix)
	}
	if got := len(call) - len(prefix); got != 3 {
		t.Errorf("call = %q, want three files after the options", call)
	}
	if seen["user-data"] != "#cloud-config\n" || seen["meta-data"] != "instance-id: x\n" || seen["network-config"] != "version: 2\n" {
		t.Errorf("files = %q", seen)
	}
	// The staging files are gone afterwards; only what xorriso wrote stays.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		t.Errorf("left behind: %s", e.Name())
	}
}

// TestMain lets the test binary stand in for an external command, so that
// Exec is tested without anything but the Go toolchain.
func TestMain(m *testing.M) {
	switch os.Getenv("VMAPI_TEST_HELPER") {
	case "fail":
		fmt.Println("out")
		fmt.Fprintln(os.Stderr, "bad thing")
		os.Exit(3)
	case "hello":
		fmt.Println("hello")
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestExecReportsStderr(t *testing.T) {
	t.Setenv("VMAPI_TEST_HELPER", "fail")
	_, err := Exec{}.Run(context.Background(), os.Args[0])
	if err == nil || !strings.Contains(err.Error(), "bad thing") {
		t.Fatalf("err = %v, want it to carry stderr", err)
	}
	t.Setenv("VMAPI_TEST_HELPER", "hello")
	out, err := Exec{}.Run(context.Background(), os.Args[0])
	if err != nil || strings.TrimSpace(string(out)) != "hello" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
}
