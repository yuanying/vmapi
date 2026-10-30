package vm_test

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/vm"
	"github.com/yuanying/vmapi/internal/vm/vmtest"
)

var (
	ctx      = context.Background()
	penelope = vm.Caller{User: "penelope"}
	pi       = vm.Caller{User: "pi"}
	admin    = vm.Caller{User: "admin", Admin: true}
)

func ptr[T any](v T) *T { return &v }

func create(t *testing.T, s *vm.Service, c vm.Caller, name string) api.VM {
	t.Helper()
	v, err := s.Create(ctx, c, api.CreateVMRequest{Name: name, Image: "noble"})
	if err != nil {
		t.Fatalf("Create %s: %v", name, err)
	}
	return v
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var e *api.Error
	if !errors.As(err, &e) {
		t.Fatalf("err = %v, want an api.Error with code %s", err, code)
	}
	if e.Code != code {
		t.Fatalf("code = %s (%s), want %s", e.Code, e.Message, code)
	}
}

func names(vms []api.VM) []string {
	var out []string
	for _, v := range vms {
		out = append(out, v.Name)
	}
	return out
}

// --- Creating ---

func TestCreateReturnsTheRunningVMWithItsIP(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	v := create(t, e.Service, penelope, "k8s-test-cp1")
	want := api.VM{
		Name: "k8s-test-cp1", Owner: "penelope", Image: "noble",
		VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20, IP: "192.168.40.10",
		State:     api.StateRunning,
		CreatedAt: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC),
	}
	if v != want {
		t.Errorf("VM = %+v\nwant %+v", v, want)
	}
	if got, err := e.Service.Get(ctx, penelope, "k8s-test-cp1"); err != nil || got != want {
		t.Errorf("Get = %+v, %v", got, err)
	}
}

func TestCreateTakesTheGivenSizes(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	v, err := e.Service.Create(ctx, penelope, api.CreateVMRequest{
		Name: "a", Image: "jammy", VCPUs: ptr(4), MemoryMiB: ptr(8192), DiskGiB: ptr(50),
	})
	if err != nil {
		t.Fatal(err)
	}
	if v.VCPUs != 4 || v.MemoryMiB != 8192 || v.DiskGiB != 50 || v.Image != "jammy" {
		t.Errorf("VM = %+v", v)
	}
	dir := e.Service.Config.Storage.Dir
	want := vmtest.Overlay{
		Backing: filepath.Join(dir, "images", strings.Repeat("2", 64)+".img"),
		Format:  "qcow2",
		Path:    filepath.Join(dir, "vms", "a", "disk.qcow2"),
		SizeGiB: 50,
	}
	if got := e.Disks.Overlays(); len(got) != 1 || got[0] != want {
		t.Errorf("overlays = %+v, want %+v", got, want)
	}
}

func TestCreateValidatesTheRequest(t *testing.T) {
	tests := []struct {
		name string
		req  api.CreateVMRequest
		code string
	}{
		{"bad name", api.CreateVMRequest{Name: "Bad_Name", Image: "noble"}, api.CodeInvalidArgument},
		{"name ending in a hyphen", api.CreateVMRequest{Name: "a-", Image: "noble"}, api.CodeInvalidArgument},
		{"name with a path", api.CreateVMRequest{Name: "../etc", Image: "noble"}, api.CodeInvalidArgument},
		{"name too long", api.CreateVMRequest{Name: strings.Repeat("a", 64), Image: "noble"}, api.CodeInvalidArgument},
		{"no image", api.CreateVMRequest{Name: "a"}, api.CodeInvalidArgument},
		{"unknown image", api.CreateVMRequest{Name: "a", Image: "plucky"}, api.CodeNotFound},
		{"no vcpus", api.CreateVMRequest{Name: "a", Image: "noble", VCPUs: ptr(0)}, api.CodeInvalidArgument},
		{"too little memory", api.CreateVMRequest{Name: "a", Image: "noble", MemoryMiB: ptr(255)}, api.CodeInvalidArgument},
		{"no disk", api.CreateVMRequest{Name: "a", Image: "noble", DiskGiB: ptr(0)}, api.CodeInvalidArgument},
		{"disk smaller than the image", api.CreateVMRequest{Name: "a", Image: "noble", DiskGiB: ptr(2)}, api.CodeInvalidArgument},
		{"not an IP", api.CreateVMRequest{Name: "a", Image: "noble", IP: ptr("x")}, api.CodeInvalidArgument},
		{"IPv6", api.CreateVMRequest{Name: "a", Image: "noble", IP: ptr("fd00::1")}, api.CodeInvalidArgument},
		{"IP below the range", api.CreateVMRequest{Name: "a", Image: "noble", IP: ptr("192.168.40.9")}, api.CodeInvalidArgument},
		{"IP above the range", api.CreateVMRequest{Name: "a", Image: "noble", IP: ptr("192.168.40.251")}, api.CodeInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := vmtest.New(t, vmtest.Config(t))
			_, err := e.Service.Create(ctx, penelope, tt.req)
			wantCode(t, err, tt.code)
			if calls := e.Hypervisor.Calls(); len(calls) != 0 {
				t.Errorf("libvirt was called: %q", calls)
			}
		})
	}
}

func TestCreateRejectsATakenName(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "a")

	// Another owner's VM.
	_, err := e.Service.Create(ctx, pi, api.CreateVMRequest{Name: "a", Image: "noble"})
	wantCode(t, err, api.CodeAlreadyExists)

	// A domain vmapi did not create.
	e.Hypervisor.AddDomain("legacy", api.StateRunning)
	_, err = e.Service.Create(ctx, pi, api.CreateVMRequest{Name: "legacy", Image: "noble"})
	wantCode(t, err, api.CodeAlreadyExists)
}

func TestCreateFailsWhenTheImageCannotBeHad(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	e.Images.Err = errors.New("checksum mismatch")
	_, err := e.Service.Create(ctx, penelope, api.CreateVMRequest{Name: "a", Image: "noble"})
	wantCode(t, err, api.CodeUnavailable)
}

func TestCreateUndoesEverythingWhenTheStartFails(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	e.Hypervisor.StartErr = errors.New("no such bridge")
	if _, err := e.Service.Create(ctx, penelope, api.CreateVMRequest{Name: "a", Image: "noble"}); err == nil {
		t.Fatal("Create succeeded")
	}
	if _, err := e.Hypervisor.GetDomain("a"); !errors.Is(err, vm.ErrNoDomain) {
		t.Errorf("the domain is still defined: %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.Service.Config.Storage.Dir, "vms", "a")); !os.IsNotExist(err) {
		t.Errorf("the VM's directory is still there: %v", err)
	}
}

func TestCreateOverwritesFilesLeftByAnUndefinedDomain(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	dir := filepath.Join(e.Service.Config.Storage.Dir, "vms", "a")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "stale"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	create(t, e.Service, penelope, "a")
	if _, err := os.Stat(filepath.Join(dir, "stale")); !os.IsNotExist(err) {
		t.Errorf("stale file kept: %v", err)
	}
}

// --- IP addresses ---

func TestCreateGivesTheLowestFreeIP(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "a")
	create(t, e.Service, penelope, "b")
	create(t, e.Service, pi, "c")
	if err := e.Service.Delete(ctx, penelope, "b"); err != nil {
		t.Fatal(err)
	}
	// A stopped VM keeps its IP.
	if _, _, err := e.Service.Stop(ctx, penelope, "a", true); err != nil {
		t.Fatal(err)
	}
	v := create(t, e.Service, pi, "d")
	if v.IP != "192.168.40.11" {
		t.Errorf("IP = %s, want the one b gave back, 192.168.40.11", v.IP)
	}
	v = create(t, e.Service, pi, "e")
	if v.IP != "192.168.40.13" {
		t.Errorf("IP = %s, want 192.168.40.13", v.IP)
	}
}

func TestCreateTakesAGivenIP(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	v, err := e.Service.Create(ctx, penelope, api.CreateVMRequest{Name: "a", Image: "noble", IP: ptr("192.168.40.250")})
	if err != nil {
		t.Fatal(err)
	}
	if v.IP != "192.168.40.250" {
		t.Errorf("IP = %s", v.IP)
	}
	// Used by another owner's VM: still in use.
	_, err = e.Service.Create(ctx, pi, api.CreateVMRequest{Name: "b", Image: "noble", IP: ptr("192.168.40.250")})
	wantCode(t, err, api.CodeIPInUse)
}

func TestCreateFailsWhenTheRangeIsUsedUp(t *testing.T) {
	cfg := vmtest.Config(t)
	cfg.Network.RangeEnd = netip.MustParseAddr("192.168.40.11")
	e := vmtest.New(t, cfg)
	create(t, e.Service, penelope, "a")
	create(t, e.Service, pi, "b")
	_, err := e.Service.Create(ctx, penelope, api.CreateVMRequest{Name: "c", Image: "noble"})
	wantCode(t, err, api.CodeIPExhausted)
}

func TestCreateKeepsIPsOutsideANarrowedRangeInUse(t *testing.T) {
	cfg := vmtest.Config(t)
	e := vmtest.New(t, cfg)
	create(t, e.Service, penelope, "a") // .10
	cfg.Network.RangeStart = netip.MustParseAddr("192.168.40.11")
	v := create(t, e.Service, penelope, "b")
	if v.IP != "192.168.40.11" {
		t.Errorf("IP = %s", v.IP)
	}
	got, err := e.Service.Get(ctx, penelope, "a")
	if err != nil || got.IP != "192.168.40.10" {
		t.Errorf("a = %+v, %v", got, err)
	}
}

func TestConcurrentCreationsNeverShareAnIP(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	e.Hypervisor.ListDelay = 2 * time.Millisecond
	const n = 20
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ips = map[string]string{}
	)
	for i := range n {
		wg.Go(func() {
			name := fmt.Sprintf("vm%d", i)
			v, err := e.Service.Create(ctx, penelope, api.CreateVMRequest{Name: name, Image: "noble"})
			if err != nil {
				t.Errorf("Create %s: %v", name, err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if other, ok := ips[v.IP]; ok {
				t.Errorf("%s and %s were both given %s", name, other, v.IP)
			}
			ips[v.IP] = name
		})
	}
	wg.Wait()
	if len(ips) != n {
		t.Errorf("%d distinct IPs, want %d", len(ips), n)
	}
}

func TestConcurrentCreationsOfOneNameMakeOneVM(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	e.Hypervisor.ListDelay = 2 * time.Millisecond
	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)
	for range 5 {
		wg.Go(func() {
			_, err := e.Service.Create(ctx, penelope, api.CreateVMRequest{Name: "same", Image: "noble"})
			if err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
				return
			}
			var ae *api.Error
			if !errors.As(err, &ae) || ae.Code != api.CodeAlreadyExists {
				t.Errorf("err = %v, want already_exists", err)
			}
		})
	}
	wg.Wait()
	if ok != 1 {
		t.Errorf("%d creations succeeded, want 1", ok)
	}
}

// --- Owners ---

func TestEveryoneSeesEveryVM(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "p1")
	create(t, e.Service, penelope, "p2")
	create(t, e.Service, pi, "x1")

	for _, c := range []vm.Caller{penelope, pi, {User: "nobody"}} {
		got, err := e.Service.List(ctx, c, "")
		if err != nil || !slices.Equal(names(got), []string{"p1", "p2", "x1"}) {
			t.Errorf("%s sees %q, %v", c.User, names(got), err)
		}
	}
	got, err := e.Service.List(ctx, penelope, "penelope")
	if err != nil || !slices.Equal(names(got), []string{"p1", "p2"}) {
		t.Errorf("penelope filtering on herself sees %q, %v", names(got), err)
	}
	got, err = e.Service.List(ctx, penelope, "pi")
	if err != nil || !slices.Equal(names(got), []string{"x1"}) {
		t.Errorf("penelope filtering on pi sees %q, %v", names(got), err)
	}
	got, err = e.Service.List(ctx, penelope, "nobody")
	if err != nil || len(got) != 0 {
		t.Errorf("penelope filtering on nobody sees %q, %v", names(got), err)
	}

	v, err := e.Service.Get(ctx, pi, "p1")
	if err != nil || v.Owner != "penelope" {
		t.Errorf("pi getting p1: %+v, %v", v, err)
	}
	if _, err := e.Service.CreateSnapshot(ctx, penelope, "p1", "s1"); err != nil {
		t.Fatal(err)
	}
	snaps, err := e.Service.Snapshots(ctx, pi, "p1")
	if err != nil || len(snaps) != 1 || snaps[0].Name != "s1" {
		t.Errorf("pi listing p1's snapshots: %+v, %v", snaps, err)
	}
}

func TestAnAdminSeesAndOperatesEveryVM(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "p1")
	create(t, e.Service, pi, "x1")
	create(t, e.Service, admin, "a1")

	got, err := e.Service.List(ctx, admin, "")
	if err != nil || !slices.Equal(names(got), []string{"a1", "p1", "x1"}) {
		t.Errorf("admin sees %q, %v", names(got), err)
	}
	got, err = e.Service.List(ctx, admin, "pi")
	if err != nil || !slices.Equal(names(got), []string{"x1"}) {
		t.Errorf("admin filtering on pi sees %q, %v", names(got), err)
	}
	// An admin's VM is the admin's.
	if v, _ := e.Service.Get(ctx, admin, "a1"); v.Owner != "admin" {
		t.Errorf("owner = %q", v.Owner)
	}
	// And an admin operates others' VMs.
	if _, err := e.Service.CreateSnapshot(ctx, admin, "p1", "s1"); err != nil {
		t.Errorf("admin CreateSnapshot: %v", err)
	}
	if _, err := e.Service.RevertSnapshot(ctx, admin, "p1", "s1"); err != nil {
		t.Errorf("admin RevertSnapshot: %v", err)
	}
	if err := e.Service.DeleteSnapshot(ctx, admin, "p1", "s1"); err != nil {
		t.Errorf("admin DeleteSnapshot: %v", err)
	}
	if _, _, err := e.Service.Stop(ctx, admin, "p1", true); err != nil {
		t.Errorf("admin Stop: %v", err)
	}
	if _, err := e.Service.Start(ctx, admin, "p1"); err != nil {
		t.Errorf("admin Start: %v", err)
	}
	if err := e.Service.Delete(ctx, admin, "x1"); err != nil {
		t.Errorf("admin Delete: %v", err)
	}
}

func TestOnlyTheOwnerOperatesAVM(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "p1")
	if _, err := e.Service.CreateSnapshot(ctx, penelope, "p1", "s1"); err != nil {
		t.Fatal(err)
	}
	before := e.Hypervisor.Calls()

	for _, op := range changes("p1", "s1") {
		t.Run(op.name, func(t *testing.T) {
			wantCode(t, op.do(e.Service, pi), api.CodePermissionDenied)
		})
	}
	if calls := e.Hypervisor.Calls(); !slices.Equal(calls, before) {
		t.Errorf("libvirt was changed: %q", calls[len(before):])
	}
}

type operation struct {
	name string
	do   func(*vm.Service, vm.Caller) error
}

// reads are every call that names a VM and changes nothing.
func reads(name string) []operation {
	return []operation{
		{"get", func(s *vm.Service, c vm.Caller) error { _, err := s.Get(ctx, c, name); return err }},
		{"list snapshots", func(s *vm.Service, c vm.Caller) error { _, err := s.Snapshots(ctx, c, name); return err }},
	}
}

// changes are every call that names a VM and changes it.
func changes(name, snap string) []operation {
	return []operation{
		{"start", func(s *vm.Service, c vm.Caller) error { _, err := s.Start(ctx, c, name); return err }},
		{"stop", func(s *vm.Service, c vm.Caller) error { _, _, err := s.Stop(ctx, c, name, false); return err }},
		{"force stop", func(s *vm.Service, c vm.Caller) error { _, _, err := s.Stop(ctx, c, name, true); return err }},
		{"delete", func(s *vm.Service, c vm.Caller) error { return s.Delete(ctx, c, name) }},
		{"create snapshot", func(s *vm.Service, c vm.Caller) error { _, err := s.CreateSnapshot(ctx, c, name, "new"); return err }},
		{"delete snapshot", func(s *vm.Service, c vm.Caller) error { return s.DeleteSnapshot(ctx, c, name, snap) }},
		{"revert snapshot", func(s *vm.Service, c vm.Caller) error { _, err := s.RevertSnapshot(ctx, c, name, snap); return err }},
	}
}

// --- Domains without metadata ---

func TestDomainsWithoutMetadataAreNotThere(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	e.Hypervisor.AddDomain("legacy", api.StateRunning)
	create(t, e.Service, penelope, "p1")

	for _, c := range []vm.Caller{penelope, admin} {
		got, err := e.Service.List(ctx, c, "")
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(names(got), "legacy") {
			t.Errorf("%s sees the legacy domain", c.User)
		}
	}
	before := e.Hypervisor.Calls()
	for _, op := range append(reads("legacy"), changes("legacy", "s1")...) {
		t.Run(op.name, func(t *testing.T) {
			for _, c := range []vm.Caller{penelope, admin} {
				wantCode(t, op.do(e.Service, c), api.CodeNotFound)
			}
		})
	}
	if calls := e.Hypervisor.Calls(); !slices.Equal(calls, before) {
		t.Errorf("libvirt was changed: %q", calls[len(before):])
	}
}

func TestANameThatCannotExistIsInvalid(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	for _, op := range append(reads("Bad_Name"), changes("Bad_Name", "s1")...) {
		t.Run(op.name, func(t *testing.T) {
			wantCode(t, op.do(e.Service, admin), api.CodeInvalidArgument)
		})
	}
}

// --- Start, stop, delete ---

func TestStopAsksTheGuestOrForcesIt(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "a")

	v, accepted, err := e.Service.Stop(ctx, penelope, "a", false)
	if err != nil || !accepted || v.State != api.StateShuttingDown {
		t.Errorf("Stop = %+v, accepted %v, %v", v, accepted, err)
	}
	v, accepted, err = e.Service.Stop(ctx, penelope, "a", true)
	if err != nil || accepted || v.State != api.StateShutOff {
		t.Errorf("forced Stop = %+v, accepted %v, %v", v, accepted, err)
	}
	// Already off: success, nothing sent to the guest.
	before := len(e.Hypervisor.Calls())
	v, accepted, err = e.Service.Stop(ctx, penelope, "a", false)
	if err != nil || accepted || v.State != api.StateShutOff {
		t.Errorf("Stop when off = %+v, accepted %v, %v", v, accepted, err)
	}
	if calls := e.Hypervisor.Calls()[before:]; len(calls) != 0 {
		t.Errorf("libvirt was called: %q", calls)
	}
}

func TestStartSucceedsWhenAlreadyRunning(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "a")
	v, err := e.Service.Start(ctx, penelope, "a")
	if err != nil || v.State != api.StateRunning {
		t.Errorf("Start = %+v, %v", v, err)
	}
	if _, _, err := e.Service.Stop(ctx, penelope, "a", true); err != nil {
		t.Fatal(err)
	}
	v, err = e.Service.Start(ctx, penelope, "a")
	if err != nil || v.State != api.StateRunning {
		t.Errorf("Start = %+v, %v", v, err)
	}
}

func TestDeleteForcesOffAndRemovesTheFiles(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "a")
	if err := e.Service.Delete(ctx, penelope, "a"); err != nil {
		t.Fatal(err)
	}
	calls := e.Hypervisor.Calls()
	if !slices.Equal(calls[len(calls)-2:], []string{"destroy a", "undefine a"}) {
		t.Errorf("calls = %q", calls)
	}
	if _, err := os.Stat(filepath.Join(e.Service.Config.Storage.Dir, "vms", "a")); !os.IsNotExist(err) {
		t.Errorf("files left: %v", err)
	}
	_, err := e.Service.Get(ctx, penelope, "a")
	wantCode(t, err, api.CodeNotFound)
}

// --- Snapshots ---

func TestSnapshots(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "a")

	s1, err := e.Service.CreateSnapshot(ctx, penelope, "a", "before-upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if s1.Name != "before-upgrade" || s1.VMState != api.StateRunning || s1.CreatedAt.IsZero() {
		t.Errorf("snapshot = %+v", s1)
	}
	_, err = e.Service.CreateSnapshot(ctx, penelope, "a", "before-upgrade")
	wantCode(t, err, api.CodeAlreadyExists)
	_, err = e.Service.CreateSnapshot(ctx, penelope, "a", "Bad")
	wantCode(t, err, api.CodeInvalidArgument)

	if _, _, err := e.Service.Stop(ctx, penelope, "a", true); err != nil {
		t.Fatal(err)
	}
	s2, err := e.Service.CreateSnapshot(ctx, penelope, "a", "off")
	if err != nil || s2.VMState != api.StateShutOff {
		t.Fatalf("snapshot = %+v, %v", s2, err)
	}

	list, err := e.Service.Snapshots(ctx, penelope, "a")
	if err != nil || len(list) != 2 || list[0].Name != "before-upgrade" || list[1].Name != "off" {
		t.Errorf("snapshots = %+v, %v", list, err)
	}

	v, err := e.Service.RevertSnapshot(ctx, penelope, "a", "before-upgrade")
	if err != nil || v.State != api.StateRunning {
		t.Errorf("revert = %+v, %v", v, err)
	}
	_, err = e.Service.RevertSnapshot(ctx, penelope, "a", "nope")
	wantCode(t, err, api.CodeNotFound)

	if err := e.Service.DeleteSnapshot(ctx, penelope, "a", "off"); err != nil {
		t.Fatal(err)
	}
	wantCode(t, e.Service.DeleteSnapshot(ctx, penelope, "a", "off"), api.CodeNotFound)
	list, _ = e.Service.Snapshots(ctx, penelope, "a")
	if len(list) != 1 {
		t.Errorf("snapshots = %+v", list)
	}
}

func TestSnapshotsLeaveTheSeedOut(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, penelope, "a")
	if _, err := e.Service.CreateSnapshot(ctx, penelope, "a", "running"); err != nil {
		t.Fatal(err)
	}
	var x struct {
		Disks []struct {
			Name     string `xml:"name,attr"`
			Snapshot string `xml:"snapshot,attr"`
		} `xml:"disks>disk"`
		Memory *struct {
			Snapshot string `xml:"snapshot,attr"`
		} `xml:"memory"`
	}
	if err := xml.Unmarshal([]byte(e.Hypervisor.SnapshotXML("a", "running")), &x); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, d := range x.Disks {
		got[d.Name] = d.Snapshot
	}
	if got["vda"] != "internal" || got["sda"] != "no" {
		t.Errorf("disks = %v, want vda internal and sda (the seed) no", got)
	}
	if x.Memory == nil || x.Memory.Snapshot != "internal" {
		t.Errorf("a running VM's snapshot should hold its memory: %+v", x.Memory)
	}
}

// --- What is written to libvirt and the seed ---

type domainXML struct {
	Name   string `xml:"name"`
	UUID   string `xml:"uuid"`
	Memory struct {
		Unit  string `xml:"unit,attr"`
		Value int    `xml:",chardata"`
	} `xml:"memory"`
	VCPU  int `xml:"vcpu"`
	Disks []struct {
		Device string `xml:"device,attr"`
		Source struct {
			File string `xml:"file,attr"`
		} `xml:"source"`
		Target struct {
			Dev string `xml:"dev,attr"`
		} `xml:"target"`
		ReadOnly *struct{} `xml:"readonly"`
	} `xml:"devices>disk"`
	Interfaces []struct {
		Type   string `xml:"type,attr"`
		Source struct {
			Bridge string `xml:"bridge,attr"`
		} `xml:"source"`
		MAC struct {
			Address string `xml:"address,attr"`
		} `xml:"mac"`
		Model struct {
			Type string `xml:"type,attr"`
		} `xml:"model"`
	} `xml:"devices>interface"`
	Serial *struct{} `xml:"devices>serial"`
	Meta   struct {
		VM *struct {
			Owner string `xml:"owner"`
			IP    string `xml:"ip"`
		} `xml:"https://github.com/yuanying/vmapi/metadata/v1 vm"`
	} `xml:"metadata"`
}

func TestTheDomainDefinition(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	create(t, e.Service, vm.Caller{User: `<odd & "name">`}, "a")
	var d domainXML
	if err := xml.Unmarshal([]byte(e.Hypervisor.XML("a")), &d); err != nil {
		t.Fatalf("the definition is not XML: %v", err)
	}
	dir := e.Service.Config.Storage.Dir
	if d.Name != "a" || d.UUID == "" || d.VCPU != 2 || d.Memory.Value != 2048 || d.Memory.Unit != "MiB" {
		t.Errorf("domain = %+v", d)
	}
	if len(d.Disks) != 2 ||
		d.Disks[0].Device != "disk" || d.Disks[0].Source.File != filepath.Join(dir, "vms", "a", "disk.qcow2") || d.Disks[0].Target.Dev != "vda" ||
		d.Disks[1].Device != "cdrom" || d.Disks[1].Source.File != filepath.Join(dir, "vms", "a", "seed.iso") || d.Disks[1].Target.Dev != "sda" || d.Disks[1].ReadOnly == nil {
		t.Errorf("disks = %+v", d.Disks)
	}
	if len(d.Interfaces) != 1 || d.Interfaces[0].Type != "bridge" || d.Interfaces[0].Source.Bridge != "br0" ||
		d.Interfaces[0].Model.Type != "virtio" || !strings.HasPrefix(d.Interfaces[0].MAC.Address, "52:54:00:") {
		t.Errorf("interfaces = %+v", d.Interfaces)
	}
	if d.Serial == nil {
		t.Error("no serial console")
	}
	if d.Meta.VM == nil || d.Meta.VM.Owner != `<odd & "name">` || d.Meta.VM.IP != "192.168.40.10" {
		t.Errorf("metadata = %+v", d.Meta.VM)
	}
}

func TestTheSeed(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	userData := "#!/bin/sh\necho hello\n"
	if _, err := e.Service.Create(ctx, penelope, api.CreateVMRequest{Name: "a", Image: "noble", UserData: &userData}); err != nil {
		t.Fatal(err)
	}
	create(t, e.Service, penelope, "b")

	var d domainXML
	if err := xml.Unmarshal([]byte(e.Hypervisor.XML("a")), &d); err != nil {
		t.Fatal(err)
	}
	seed, ok := e.Seeds.Seed(filepath.Join(e.Service.Config.Storage.Dir, "vms", "a", "seed.iso"))
	if !ok {
		t.Fatal("no seed made at vms/a/seed.iso")
	}
	if string(seed.UserData) != userData {
		t.Errorf("user-data = %q, want it as sent", seed.UserData)
	}

	var meta map[string]string
	if err := yaml.Unmarshal(seed.MetaData, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["instance-id"] != d.UUID || meta["local-hostname"] != "a" {
		t.Errorf("meta-data = %v, want instance-id %s", meta, d.UUID)
	}

	var net struct {
		Version   int `yaml:"version"`
		Ethernets map[string]struct {
			Match struct {
				MACAddress string `yaml:"macaddress"`
			} `yaml:"match"`
			Addresses []string `yaml:"addresses"`
			Routes    []struct {
				To  string `yaml:"to"`
				Via string `yaml:"via"`
			} `yaml:"routes"`
			Nameservers struct {
				Addresses []string `yaml:"addresses"`
			} `yaml:"nameservers"`
		} `yaml:"ethernets"`
	}
	if err := yaml.Unmarshal(seed.NetworkConfig, &net); err != nil {
		t.Fatal(err)
	}
	if net.Version != 2 || len(net.Ethernets) != 1 {
		t.Fatalf("network-config = %s", seed.NetworkConfig)
	}
	for _, eth := range net.Ethernets {
		if eth.Match.MACAddress != d.Interfaces[0].MAC.Address {
			t.Errorf("matches %q, want the domain's MAC %q", eth.Match.MACAddress, d.Interfaces[0].MAC.Address)
		}
		if !slices.Equal(eth.Addresses, []string{"192.168.40.10/16"}) ||
			len(eth.Routes) != 1 || eth.Routes[0].To != "default" || eth.Routes[0].Via != "192.168.0.1" ||
			!slices.Equal(eth.Nameservers.Addresses, []string{"192.168.0.1"}) {
			t.Errorf("network-config = %s", seed.NetworkConfig)
		}
	}

	// Without user-data, an empty #cloud-config.
	seed, _ = e.Seeds.Seed(filepath.Join(e.Service.Config.Storage.Dir, "vms", "b", "seed.iso"))
	if strings.TrimSpace(string(seed.UserData)) != "#cloud-config" {
		t.Errorf("default user-data = %q", seed.UserData)
	}
}

func TestImages(t *testing.T) {
	e := vmtest.New(t, vmtest.Config(t))
	got := e.Service.Images()
	want := []api.Image{{Name: "noble", Description: "Ubuntu 24.04 LTS"}, {Name: "jammy", Description: "Ubuntu 22.04 LTS"}}
	if !slices.Equal(got, want) {
		t.Errorf("Images = %+v", got)
	}
}
