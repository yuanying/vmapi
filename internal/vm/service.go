// Package vm is what vmapi does: it creates, lists and operates the VMs of
// their owners on libvirt, keeping its state only in the domains' metadata
// (docs/adr/0002, 0003).
package vm

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/command"
	"github.com/yuanying/vmapi/internal/config"
)

// Caller is who a request comes from.
type Caller struct {
	User  string
	Admin bool
}

// Operates tells whether the caller may change the VM: its owner and
// admins may. Everyone may see it (docs/adr/0002).
func (c Caller) Operates(v api.VM) bool {
	return c.Admin || v.Owner == c.User
}

// Images hands out the path of a kept image, downloading it first if needed.
type Images interface {
	Ensure(ctx context.Context, img config.Image) (string, error)
}

// Disks inspects images and makes VM disks.
type Disks interface {
	Inspect(ctx context.Context, path string) (command.ImageInfo, error)
	CreateOverlay(ctx context.Context, backing, backingFormat, path string, sizeGiB int) error
}

// Seeds makes cloud-init seed ISOs.
type Seeds interface {
	Make(ctx context.Context, out string, files command.SeedFiles) error
}

// Defaults of createVM (api/openapi.yaml).
const (
	defaultVCPUs     = 2
	defaultMemoryMiB = 2048
	defaultDiskGiB   = 20
	minMemoryMiB     = 256
)

// Service is vmapi's logic over libvirt.
type Service struct {
	Config     *config.Config
	Hypervisor Hypervisor
	ImageStore Images
	Disks      Disks
	Seeds      Seeds
	// Now is the clock; time.Now when nil.
	Now func() time.Time

	// createMu makes "check the name and the IP, then define" one step
	// across creations (docs/adr/0003).
	createMu sync.Mutex
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) vmDir(name string) string {
	return filepath.Join(s.Config.Storage.Dir, "vms", name)
}

// Images lists the images of the configuration.
func (s *Service) Images() []api.Image {
	out := make([]api.Image, 0, len(s.Config.Images))
	for _, img := range s.Config.Images {
		out = append(out, api.Image{Name: img.Name, Description: img.Description})
	}
	return out
}

// managed is a domain of vmapi's, with what was read from it.
type managed struct {
	domain Domain
	parsed parsedDomain
	meta   *metadata
}

func (m managed) vm() api.VM {
	created, _ := time.Parse(time.RFC3339, m.meta.CreatedAt)
	return api.VM{
		Name:      m.domain.Name,
		Owner:     m.meta.Owner,
		Image:     m.meta.Image,
		VCPUs:     m.parsed.VCPU,
		MemoryMiB: m.parsed.memoryMiB(),
		DiskGiB:   m.meta.DiskGiB,
		IP:        m.meta.IP,
		State:     m.domain.State,
		CreatedAt: created,
	}
}

// readDomain parses a domain. A domain without vmapi's metadata comes back
// with a nil meta.
func readDomain(d Domain) (managed, error) {
	p, err := parseDomain(d.XML)
	if err != nil {
		return managed{}, err
	}
	return managed{domain: d, parsed: p, meta: p.Metadata.VM}, nil
}

func notFound(name string) error {
	return api.Errorf(api.CodeNotFound, "vm %q not found", name)
}

// libvirtError turns an error from the Hypervisor into one of the contract.
func libvirtError(err error) error {
	var e *api.Error
	if errors.As(err, &e) {
		return err
	}
	if errors.Is(err, ErrUnavailable) {
		return api.Errorf(api.CodeUnavailable, "%v", err)
	}
	return api.Errorf(api.CodeInternal, "libvirt: %v", err)
}

func checkName(kind, name string) error {
	if !api.ValidName(name) {
		return api.Errorf(api.CodeInvalidArgument, "%s name %q is not a DNS label: a lowercase letter first, then lowercase letters, digits and hyphens, not ending in a hyphen, 63 characters at most", kind, name)
	}
	return nil
}

// find returns the VM of the name. Domains without metadata are not found,
// for admins too.
func (s *Service) find(name string) (managed, error) {
	if err := checkName("vm", name); err != nil {
		return managed{}, err
	}
	d, err := s.Hypervisor.GetDomain(name)
	if errors.Is(err, ErrNoDomain) {
		return managed{}, notFound(name)
	}
	if err != nil {
		return managed{}, libvirtError(err)
	}
	m, err := readDomain(d)
	if err != nil {
		return managed{}, libvirtError(err)
	}
	if m.meta == nil {
		return managed{}, notFound(name)
	}
	return m, nil
}

// operated returns the VM of the name if the caller may change it.
func (s *Service) operated(c Caller, name string) (managed, error) {
	m, err := s.find(name)
	if err != nil {
		return managed{}, err
	}
	if !c.Operates(m.vm()) {
		return managed{}, api.Errorf(api.CodePermissionDenied, "vm %q is %q's: only its owner or an admin can operate it", name, m.meta.Owner)
	}
	return m, nil
}

// get reads the VM again after an operation changed it.
func (s *Service) get(name string) (api.VM, error) {
	m, err := s.find(name)
	if err != nil {
		return api.VM{}, err
	}
	return m.vm(), nil
}

// all reads every domain, vmapi's or not.
func (s *Service) all() ([]managed, error) {
	domains, err := s.Hypervisor.ListDomains()
	if err != nil {
		return nil, libvirtError(err)
	}
	out := make([]managed, 0, len(domains))
	for _, d := range domains {
		m, err := readDomain(d)
		if err != nil {
			return nil, libvirtError(err)
		}
		out = append(out, m)
	}
	return out, nil
}

// List returns every VM, by name. owner narrows them to one owner.
func (s *Service) List(_ context.Context, _ Caller, owner string) ([]api.VM, error) {
	all, err := s.all()
	if err != nil {
		return nil, err
	}
	out := []api.VM{}
	for _, m := range all {
		if m.meta == nil || (owner != "" && m.meta.Owner != owner) {
			continue
		}
		out = append(out, m.vm())
	}
	slices.SortFunc(out, func(a, b api.VM) int { return cmp.Compare(a.Name, b.Name) })
	return out, nil
}

// Get returns one VM.
func (s *Service) Get(_ context.Context, _ Caller, name string) (api.VM, error) {
	return s.get(name)
}

// createSpec is a validated createVM request.
type createSpec struct {
	name      string
	image     config.Image
	vcpus     int
	memoryMiB int
	diskGiB   int
	ip        netip.Addr // invalid when the caller left it to vmapi
	userData  string
}

func (s *Service) validate(req api.CreateVMRequest) (createSpec, error) {
	spec := createSpec{
		name:      req.Name,
		vcpus:     cmp.Or(deref(req.VCPUs), defaultVCPUs),
		memoryMiB: cmp.Or(deref(req.MemoryMiB), defaultMemoryMiB),
		diskGiB:   cmp.Or(deref(req.DiskGiB), defaultDiskGiB),
		userData:  emptyUserData,
	}
	if err := checkName("vm", req.Name); err != nil {
		return spec, err
	}
	if req.Image == "" {
		return spec, api.Errorf(api.CodeInvalidArgument, "image is required")
	}
	img, ok := s.Config.Image(req.Image)
	if !ok {
		return spec, api.Errorf(api.CodeNotFound, "image %q not found", req.Image)
	}
	spec.image = img
	if req.VCPUs != nil && *req.VCPUs < 1 {
		return spec, api.Errorf(api.CodeInvalidArgument, "vcpus must be 1 or more")
	}
	if req.MemoryMiB != nil && *req.MemoryMiB < minMemoryMiB {
		return spec, api.Errorf(api.CodeInvalidArgument, "memory_mib must be %d or more", minMemoryMiB)
	}
	if req.DiskGiB != nil && *req.DiskGiB < 1 {
		return spec, api.Errorf(api.CodeInvalidArgument, "disk_gib must be 1 or more")
	}
	if req.IP != nil {
		ip, err := netip.ParseAddr(*req.IP)
		if err != nil || !ip.Is4() {
			return spec, api.Errorf(api.CodeInvalidArgument, "ip %q is not an IPv4 address", *req.IP)
		}
		n := s.Config.Network
		if !n.InRange(ip) {
			return spec, api.Errorf(api.CodeInvalidArgument, "ip %s is outside the range %s-%s", ip, n.RangeStart, n.RangeEnd)
		}
		spec.ip = ip
	}
	if req.UserData != nil {
		spec.userData = *req.UserData
	}
	return spec, nil
}

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

// Create makes, defines and starts a VM owned by the caller. The image is
// downloaded outside the creation lock; inside it, the name and the IP are
// checked against every domain, and the files are made and the domain
// defined. A failure after that undoes it all.
func (s *Service) Create(ctx context.Context, c Caller, req api.CreateVMRequest) (api.VM, error) {
	spec, err := s.validate(req)
	if err != nil {
		return api.VM{}, err
	}

	backing, err := s.ImageStore.Ensure(ctx, spec.image)
	if err != nil {
		return api.VM{}, api.Errorf(api.CodeUnavailable, "%v", err)
	}
	info, err := s.Disks.Inspect(ctx, backing)
	if err != nil {
		return api.VM{}, api.Errorf(api.CodeInternal, "%v", err)
	}
	if size := int64(spec.diskGiB) << 30; size < info.VirtualSize {
		return api.VM{}, api.Errorf(api.CodeInvalidArgument, "disk_gib %d is smaller than the image %s (%d bytes)", spec.diskGiB, spec.image.Name, info.VirtualSize)
	}

	if err := s.define(ctx, c, spec, backing, info.Format); err != nil {
		return api.VM{}, err
	}
	if err := s.Hypervisor.StartDomain(spec.name); err != nil {
		return api.VM{}, errors.Join(libvirtError(err), s.remove(spec.name))
	}
	return s.get(spec.name)
}

func (s *Service) define(ctx context.Context, c Caller, spec createSpec, backing, backingFormat string) (err error) {
	s.createMu.Lock()
	defer s.createMu.Unlock()

	all, err := s.all()
	if err != nil {
		return err
	}
	used := map[netip.Addr]bool{}
	macs := map[string]bool{}
	for _, m := range all {
		if m.domain.Name == spec.name {
			return api.Errorf(api.CodeAlreadyExists, "vm %q already exists", spec.name)
		}
		for _, iface := range m.parsed.Interfaces {
			macs[iface.MAC.Address] = true
		}
		if m.meta == nil {
			continue
		}
		if ip, err := netip.ParseAddr(m.meta.IP); err == nil {
			used[ip] = true
		}
	}
	ip, err := s.pickIP(spec.ip, used)
	if err != nil {
		return err
	}

	dir := s.vmDir(spec.name)
	// No domain has the name, so whatever is there was left by one that
	// was undefined behind vmapi's back (docs/adr/0003).
	if err := os.RemoveAll(dir); err != nil {
		return api.Errorf(api.CodeInternal, "%v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return api.Errorf(api.CodeInternal, "%v", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(dir)
		}
	}()

	uuid, mac := newUUID(), newMAC(macs)
	disk := filepath.Join(dir, "disk.qcow2")
	seed := filepath.Join(dir, "seed.iso")
	if err := s.Disks.CreateOverlay(ctx, backing, backingFormat, disk, spec.diskGiB); err != nil {
		return api.Errorf(api.CodeInternal, "%v", err)
	}
	meta, err := metaData(uuid, spec.name)
	if err != nil {
		return api.Errorf(api.CodeInternal, "%v", err)
	}
	netcfg, err := networkConfig(s.Config.Network, mac, ip)
	if err != nil {
		return api.Errorf(api.CodeInternal, "%v", err)
	}
	if err := s.Seeds.Make(ctx, seed, command.SeedFiles{UserData: []byte(spec.userData), MetaData: meta, NetworkConfig: netcfg}); err != nil {
		return api.Errorf(api.CodeInternal, "%v", err)
	}
	def := domainXML(domainSpec{
		Name: spec.name, UUID: uuid, VCPUs: spec.vcpus, MemoryMiB: spec.memoryMiB,
		Disk: disk, Seed: seed, Bridge: s.Config.Network.Bridge, MAC: mac,
		Meta: metadata{
			Owner: c.User, IP: ip.String(),
			Image: spec.image.Name, ImageSHA256: spec.image.SHA256,
			DiskGiB: spec.diskGiB, CreatedAt: s.now().UTC().Format(time.RFC3339),
		},
	})
	if err := s.Hypervisor.DefineDomain(def); err != nil {
		return libvirtError(err)
	}
	return nil
}

// pickIP checks the requested IP, or picks the lowest free one.
func (s *Service) pickIP(requested netip.Addr, used map[netip.Addr]bool) (netip.Addr, error) {
	if requested.IsValid() {
		if used[requested] {
			return netip.Addr{}, api.Errorf(api.CodeIPInUse, "ip %s is used by another vm", requested)
		}
		return requested, nil
	}
	n := s.Config.Network
	for ip := n.RangeStart; ip.IsValid() && !n.RangeEnd.Less(ip); ip = ip.Next() {
		if !used[ip] {
			return ip, nil
		}
	}
	return netip.Addr{}, api.Errorf(api.CodeIPExhausted, "no ip is left in %s-%s", n.RangeStart, n.RangeEnd)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// newMAC is a MAC in QEMU's range that no domain has.
func newMAC(taken map[string]bool) string {
	for {
		var b [3]byte
		_, _ = rand.Read(b[:])
		mac := fmt.Sprintf("52:54:00:%02x:%02x:%02x", b[0], b[1], b[2])
		if !taken[mac] {
			return mac
		}
	}
}

// Start starts the VM. A running VM is left as it is.
func (s *Service) Start(_ context.Context, c Caller, name string) (api.VM, error) {
	m, err := s.operated(c, name)
	if err != nil {
		return api.VM{}, err
	}
	if m.domain.State != api.StateRunning {
		if err := s.Hypervisor.StartDomain(name); err != nil {
			return api.VM{}, libvirtError(err)
		}
	}
	return s.get(name)
}

// Stop turns the VM off at once with force, and otherwise asks the guest to
// shut down without waiting. accepted tells the second case, in which the
// VM may not be off yet. A VM that is off is left as it is.
func (s *Service) Stop(_ context.Context, c Caller, name string, force bool) (v api.VM, accepted bool, err error) {
	m, err := s.operated(c, name)
	if err != nil {
		return api.VM{}, false, err
	}
	if m.domain.State == api.StateShutOff {
		return m.vm(), false, nil
	}
	if force {
		err = s.Hypervisor.DestroyDomain(name)
	} else {
		err = s.Hypervisor.ShutdownDomain(name)
	}
	if err != nil {
		return api.VM{}, false, libvirtError(err)
	}
	v, err = s.get(name)
	return v, !force, err
}

// Delete turns the VM off if it runs, undefines it with its snapshots and
// removes its files.
func (s *Service) Delete(_ context.Context, c Caller, name string) error {
	m, err := s.operated(c, name)
	if err != nil {
		return err
	}
	if m.domain.State != api.StateShutOff {
		if err := s.Hypervisor.DestroyDomain(name); err != nil {
			return libvirtError(err)
		}
	}
	return s.remove(name)
}

// remove undefines the domain and removes its files.
func (s *Service) remove(name string) error {
	if err := s.Hypervisor.UndefineDomain(name); err != nil && !errors.Is(err, ErrNoDomain) {
		return libvirtError(err)
	}
	if err := os.RemoveAll(s.vmDir(name)); err != nil {
		return api.Errorf(api.CodeInternal, "%v", err)
	}
	return nil
}

func (s *Service) snapshots(name string) ([]api.Snapshot, error) {
	defs, err := s.Hypervisor.ListSnapshots(name)
	if err != nil {
		return nil, libvirtError(err)
	}
	type entry struct {
		snap    api.Snapshot
		created int64
	}
	entries := make([]entry, 0, len(defs))
	for _, def := range defs {
		snap, created, err := parseSnapshot(def)
		if err != nil {
			return nil, libvirtError(err)
		}
		entries = append(entries, entry{snap, created})
	}
	slices.SortFunc(entries, func(a, b entry) int {
		return cmp.Or(cmp.Compare(a.created, b.created), cmp.Compare(a.snap.Name, b.snap.Name))
	})
	out := make([]api.Snapshot, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.snap)
	}
	return out, nil
}

// Snapshots lists the VM's snapshots, oldest first.
func (s *Service) Snapshots(_ context.Context, _ Caller, name string) ([]api.Snapshot, error) {
	if _, err := s.find(name); err != nil {
		return nil, err
	}
	return s.snapshots(name)
}

func snapshotNotFound(name, snap string) error {
	return api.Errorf(api.CodeNotFound, "snapshot %q of vm %q not found", snap, name)
}

// CreateSnapshot takes an internal snapshot of the VM.
func (s *Service) CreateSnapshot(_ context.Context, c Caller, name, snap string) (api.Snapshot, error) {
	m, err := s.operated(c, name)
	if err != nil {
		return api.Snapshot{}, err
	}
	if err := checkName("snapshot", snap); err != nil {
		return api.Snapshot{}, err
	}
	existing, err := s.snapshots(name)
	if err != nil {
		return api.Snapshot{}, err
	}
	if slices.ContainsFunc(existing, func(e api.Snapshot) bool { return e.Name == snap }) {
		return api.Snapshot{}, api.Errorf(api.CodeAlreadyExists, "snapshot %q of vm %q already exists", snap, name)
	}
	if err := s.Hypervisor.CreateSnapshot(name, snapshotXML(snap, m.domain.State == api.StateRunning)); err != nil {
		return api.Snapshot{}, libvirtError(err)
	}
	all, err := s.snapshots(name)
	if err != nil {
		return api.Snapshot{}, err
	}
	for _, e := range all {
		if e.Name == snap {
			return e, nil
		}
	}
	return api.Snapshot{}, api.Errorf(api.CodeInternal, "snapshot %q of vm %q is missing after it was taken", snap, name)
}

// DeleteSnapshot deletes one of the VM's snapshots.
func (s *Service) DeleteSnapshot(_ context.Context, c Caller, name, snap string) error {
	if _, err := s.operated(c, name); err != nil {
		return err
	}
	if err := checkName("snapshot", snap); err != nil {
		return err
	}
	err := s.Hypervisor.DeleteSnapshot(name, snap)
	if errors.Is(err, ErrNoSnapshot) {
		return snapshotNotFound(name, snap)
	}
	if err != nil {
		return libvirtError(err)
	}
	return nil
}

// RevertSnapshot returns the VM to the snapshot, running or off as it was.
func (s *Service) RevertSnapshot(_ context.Context, c Caller, name, snap string) (api.VM, error) {
	if _, err := s.operated(c, name); err != nil {
		return api.VM{}, err
	}
	if err := checkName("snapshot", snap); err != nil {
		return api.VM{}, err
	}
	err := s.Hypervisor.RevertSnapshot(name, snap)
	if errors.Is(err, ErrNoSnapshot) {
		return api.VM{}, snapshotNotFound(name, snap)
	}
	if err != nil {
		return api.VM{}, libvirtError(err)
	}
	return s.get(name)
}
