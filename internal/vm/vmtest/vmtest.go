// Package vmtest has stand-ins for libvirt, the image store and the external
// commands, so that vm.Service is tested without any of them.
package vmtest

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/command"
	"github.com/yuanying/vmapi/internal/config"
	"github.com/yuanying/vmapi/internal/vm"
)

// Hypervisor is an in-memory libvirt.
type Hypervisor struct {
	// ListDelay is slept inside ListDomains, to widen the window in which
	// two creations could see the same free IP.
	ListDelay time.Duration
	// StartErr, when set, is returned by StartDomain.
	StartErr error

	mu      sync.Mutex
	domains map[string]*domain
	calls   []string
	clock   int64

	snapshotXML map[string]string
}

type domain struct {
	xml       string
	state     string
	snapshots []snapshot
}

type snapshot struct {
	name    string
	state   string // libvirt's word: running, shutoff
	created int64
}

// AddDomain puts a domain there directly, as if someone had defined it
// without vmapi.
func (h *Hypervisor) AddDomain(name, state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	h.domains[name] = &domain{
		xml:   fmt.Sprintf("<domain type='kvm'><name>%s</name><memory unit='KiB'>1048576</memory><vcpu>1</vcpu></domain>", name),
		state: state,
	}
}

// Calls lists the operations that changed something, as "verb name".
func (h *Hypervisor) Calls() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.calls)
}

// XML is the definition of the domain.
func (h *Hypervisor) XML(name string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if d := h.domains[name]; d != nil {
		return d.xml
	}
	return ""
}

// SetState changes the state of a domain, as the guest would.
func (h *Hypervisor) SetState(name, state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.domains[name].state = state
}

func (h *Hypervisor) init() {
	if h.domains == nil {
		h.domains = map[string]*domain{}
	}
}

func (h *Hypervisor) record(verb, name string) {
	h.calls = append(h.calls, verb+" "+name)
}

func (h *Hypervisor) ListDomains() ([]vm.Domain, error) {
	h.mu.Lock()
	h.init()
	var out []vm.Domain
	for name, d := range h.domains {
		out = append(out, vm.Domain{Name: name, XML: d.xml, State: d.state})
	}
	h.mu.Unlock()
	time.Sleep(h.ListDelay)
	return out, nil
}

func (h *Hypervisor) GetDomain(name string) (vm.Domain, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	d := h.domains[name]
	if d == nil {
		return vm.Domain{}, vm.ErrNoDomain
	}
	return vm.Domain{Name: name, XML: d.xml, State: d.state}, nil
}

func (h *Hypervisor) DefineDomain(def string) error {
	var x struct {
		Name string `xml:"name"`
	}
	if err := xml.Unmarshal([]byte(def), &x); err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	if h.domains[x.Name] != nil {
		return fmt.Errorf("domain %s already exists", x.Name)
	}
	h.record("define", x.Name)
	h.domains[x.Name] = &domain{xml: def, state: api.StateShutOff}
	return nil
}

func (h *Hypervisor) op(verb, name string, f func(d *domain) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	d := h.domains[name]
	if d == nil {
		return vm.ErrNoDomain
	}
	h.record(verb, name)
	return f(d)
}

func (h *Hypervisor) UndefineDomain(name string) error {
	return h.op("undefine", name, func(*domain) error {
		delete(h.domains, name)
		return nil
	})
}

func (h *Hypervisor) StartDomain(name string) error {
	return h.op("start", name, func(d *domain) error {
		if h.StartErr != nil {
			return h.StartErr
		}
		if d.state == api.StateRunning {
			return fmt.Errorf("domain %s is already running", name)
		}
		d.state = api.StateRunning
		return nil
	})
}

func (h *Hypervisor) ShutdownDomain(name string) error {
	return h.op("shutdown", name, func(d *domain) error {
		d.state = api.StateShuttingDown
		return nil
	})
}

func (h *Hypervisor) DestroyDomain(name string) error {
	return h.op("destroy", name, func(d *domain) error {
		d.state = api.StateShutOff
		return nil
	})
}

func (h *Hypervisor) ListSnapshots(name string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.init()
	d := h.domains[name]
	if d == nil {
		return nil, vm.ErrNoDomain
	}
	var out []string
	for _, s := range d.snapshots {
		out = append(out, fmt.Sprintf("<domainsnapshot><name>%s</name><state>%s</state><creationTime>%d</creationTime></domainsnapshot>", s.name, s.state, s.created))
	}
	return out, nil
}

// SnapshotXML is the definition the named snapshot was created with.
func (h *Hypervisor) SnapshotXML(domainName, snap string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshotXML[domainName+"/"+snap]
}

func (h *Hypervisor) CreateSnapshot(name, def string) error {
	var x struct {
		Name string `xml:"name"`
	}
	if err := xml.Unmarshal([]byte(def), &x); err != nil {
		return err
	}
	return h.op("snapshot", name, func(d *domain) error {
		for _, s := range d.snapshots {
			if s.name == x.Name {
				return fmt.Errorf("snapshot %s exists", x.Name)
			}
		}
		h.clock++
		state := "shutoff"
		if d.state == api.StateRunning {
			state = "running"
		}
		d.snapshots = append(d.snapshots, snapshot{name: x.Name, state: state, created: 1_700_000_000 + h.clock})
		if h.snapshotXML == nil {
			h.snapshotXML = map[string]string{}
		}
		h.snapshotXML[name+"/"+x.Name] = def
		return nil
	})
}

func (h *Hypervisor) DeleteSnapshot(name, snap string) error {
	return h.op("delete-snapshot", name, func(d *domain) error {
		for i, s := range d.snapshots {
			if s.name == snap {
				d.snapshots = slices.Delete(d.snapshots, i, i+1)
				return nil
			}
		}
		return vm.ErrNoSnapshot
	})
}

func (h *Hypervisor) RevertSnapshot(name, snap string) error {
	return h.op("revert", name, func(d *domain) error {
		for _, s := range d.snapshots {
			if s.name == snap {
				d.state = api.StateShutOff
				if s.state == "running" {
					d.state = api.StateRunning
				}
				return nil
			}
		}
		return vm.ErrNoSnapshot
	})
}

// Images hands out a path for every image without downloading anything.
type Images struct {
	Dir string
	Err error
}

func (i *Images) Ensure(_ context.Context, img config.Image) (string, error) {
	if i.Err != nil {
		return "", i.Err
	}
	return filepath.Join(i.Dir, img.SHA256+".img"), nil
}

// Disks pretends to be qemu-img. Every image is VirtualSize bytes.
type Disks struct {
	VirtualSize int64

	mu       sync.Mutex
	overlays []Overlay
}

// Overlay is a CreateOverlay call.
type Overlay struct {
	Backing, Format, Path string
	SizeGiB               int
}

func (d *Disks) Inspect(context.Context, string) (command.ImageInfo, error) {
	return command.ImageInfo{Format: "qcow2", VirtualSize: d.VirtualSize}, nil
}

func (d *Disks) CreateOverlay(_ context.Context, backing, format, path string, sizeGiB int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.overlays = append(d.overlays, Overlay{backing, format, path, sizeGiB})
	return os.WriteFile(path, []byte("overlay"), 0o600)
}

// Overlays lists the CreateOverlay calls.
func (d *Disks) Overlays() []Overlay {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.overlays)
}

// Seeds pretends to be xorriso and keeps the files of every seed by path.
type Seeds struct {
	mu    sync.Mutex
	seeds map[string]command.SeedFiles
}

func (s *Seeds) Make(_ context.Context, out string, files command.SeedFiles) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seeds == nil {
		s.seeds = map[string]command.SeedFiles{}
	}
	s.seeds[out] = files
	return os.WriteFile(out, []byte("iso"), 0o600)
}

// Seed is the files of the seed made at path.
func (s *Seeds) Seed(path string) (command.SeedFiles, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.seeds[path]
	return f, ok
}

// Config is a valid configuration with a small range, the admin "admin",
// and the images "noble" and "jammy".
func Config(t testing.TB) *config.Config {
	t.Helper()
	return &config.Config{
		Listen:  ":8080",
		Storage: config.Storage{Dir: t.TempDir()},
		Network: config.Network{
			Bridge:     "br0",
			RangeStart: netip.MustParseAddr("192.168.40.10"),
			RangeEnd:   netip.MustParseAddr("192.168.40.250"),
			Prefix:     16,
			Gateway:    netip.MustParseAddr("192.168.0.1"),
			DNS:        []netip.Addr{netip.MustParseAddr("192.168.0.1")},
		},
		Admins: []string{"admin"},
		Images: []config.Image{
			{Name: "noble", Description: "Ubuntu 24.04 LTS", URL: "https://example.com/noble.img", SHA256: "1111111111111111111111111111111111111111111111111111111111111111"},
			{Name: "jammy", Description: "Ubuntu 22.04 LTS", URL: "https://example.com/jammy.img", SHA256: "2222222222222222222222222222222222222222222222222222222222222222"},
		},
	}
}

// Env is a Service wired to the stand-ins.
type Env struct {
	Service    *vm.Service
	Hypervisor *Hypervisor
	Images     *Images
	Disks      *Disks
	Seeds      *Seeds
}

// New wires a Service to fresh stand-ins over cfg.
func New(t testing.TB, cfg *config.Config) *Env {
	t.Helper()
	e := &Env{
		Hypervisor: &Hypervisor{},
		Images:     &Images{Dir: filepath.Join(cfg.Storage.Dir, "images")},
		Disks:      &Disks{VirtualSize: 3 << 30},
		Seeds:      &Seeds{},
	}
	e.Service = &vm.Service{
		Config:     cfg,
		Hypervisor: e.Hypervisor,
		ImageStore: e.Images,
		Disks:      e.Disks,
		Seeds:      e.Seeds,
		Now:        func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) },
	}
	return e
}
