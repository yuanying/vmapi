package vm

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/yuanying/vmapi/internal/api"
)

// metadataNS is the namespace of vmapi's element in a domain's <metadata>
// (docs/adr/0003). Its last segment is the version of the element's shape.
const metadataNS = "https://github.com/yuanying/vmapi/metadata/v1"

// metadata is what vmapi remembers of a VM.
type metadata struct {
	Owner       string `xml:"owner"`
	IP          string `xml:"ip"`
	Image       string `xml:"image"`
	ImageSHA256 string `xml:"image-sha256"`
	DiskGiB     int    `xml:"disk-gib"`
	CreatedAt   string `xml:"created-at"`
}

// parsedDomain is what vmapi reads back from a domain's definition.
type parsedDomain struct {
	Name   string `xml:"name"`
	UUID   string `xml:"uuid"`
	Memory struct {
		Unit  string `xml:"unit,attr"`
		Value int64  `xml:",chardata"`
	} `xml:"memory"`
	VCPU       int `xml:"vcpu"`
	Interfaces []struct {
		MAC struct {
			Address string `xml:"address,attr"`
		} `xml:"mac"`
	} `xml:"devices>interface"`
	Metadata struct {
		VM *metadata `xml:"https://github.com/yuanying/vmapi/metadata/v1 vm"`
	} `xml:"metadata"`
}

func parseDomain(def string) (parsedDomain, error) {
	var d parsedDomain
	if err := xml.Unmarshal([]byte(def), &d); err != nil {
		return d, fmt.Errorf("domain XML: %w", err)
	}
	return d, nil
}

// memoryMiB converts libvirt's memory element, KiB unless said otherwise.
func (d parsedDomain) memoryMiB() int {
	v := d.Memory.Value
	switch strings.ToLower(d.Memory.Unit) {
	case "b", "bytes":
		v /= 1 << 20
	case "", "k", "kib":
		v /= 1 << 10
	case "m", "mib":
	case "g", "gib":
		v <<= 10
	case "t", "tib":
		v <<= 20
	case "kb":
		v = v * 1000 / (1 << 20)
	case "mb":
		v = v * 1000 * 1000 / (1 << 20)
	case "gb":
		v = v * 1000 * 1000 * 1000 / (1 << 20)
	}
	return int(v)
}

func esc(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// domainSpec is everything a new domain's definition is made from.
type domainSpec struct {
	Name      string
	UUID      string
	VCPUs     int
	MemoryMiB int
	Disk      string
	Seed      string
	Bridge    string
	MAC       string
	Meta      metadata
}

// domainXML is the definition of a new domain. Nothing in it comes from the
// caller but the name (a DNS label) and the owner, which is escaped
// (docs/adr/0008). The metadata's children carry the prefix too, as libvirt
// keeps only the elements of the element's own namespace.
func domainXML(s domainSpec) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("<domain type='kvm'>\n")
	w("  <name>%s</name>\n", esc(s.Name))
	w("  <uuid>%s</uuid>\n", esc(s.UUID))
	w("  <metadata>\n")
	w("    <vmapi:vm xmlns:vmapi='%s'>\n", metadataNS)
	w("      <vmapi:owner>%s</vmapi:owner>\n", esc(s.Meta.Owner))
	w("      <vmapi:ip>%s</vmapi:ip>\n", esc(s.Meta.IP))
	w("      <vmapi:image>%s</vmapi:image>\n", esc(s.Meta.Image))
	w("      <vmapi:image-sha256>%s</vmapi:image-sha256>\n", esc(s.Meta.ImageSHA256))
	w("      <vmapi:disk-gib>%d</vmapi:disk-gib>\n", s.Meta.DiskGiB)
	w("      <vmapi:created-at>%s</vmapi:created-at>\n", esc(s.Meta.CreatedAt))
	w("    </vmapi:vm>\n")
	w("  </metadata>\n")
	w("  <memory unit='MiB'>%d</memory>\n", s.MemoryMiB)
	w("  <vcpu>%d</vcpu>\n", s.VCPUs)
	w("  <os><type arch='x86_64' machine='q35'>hvm</type><boot dev='hd'/></os>\n")
	w("  <features><acpi/><apic/></features>\n")
	w("  <cpu mode='host-passthrough'/>\n")
	w("  <on_poweroff>destroy</on_poweroff>\n")
	w("  <on_reboot>restart</on_reboot>\n")
	w("  <on_crash>destroy</on_crash>\n")
	w("  <devices>\n")
	w("    <disk type='file' device='disk'>\n")
	w("      <driver name='qemu' type='qcow2'/>\n")
	w("      <source file='%s'/>\n", esc(s.Disk))
	w("      <target dev='vda' bus='virtio'/>\n")
	w("    </disk>\n")
	w("    <disk type='file' device='cdrom'>\n")
	w("      <driver name='qemu' type='raw'/>\n")
	w("      <source file='%s'/>\n", esc(s.Seed))
	w("      <target dev='sda' bus='sata'/>\n")
	w("      <readonly/>\n")
	w("    </disk>\n")
	w("    <interface type='bridge'>\n")
	w("      <source bridge='%s'/>\n", esc(s.Bridge))
	w("      <mac address='%s'/>\n", esc(s.MAC))
	w("      <model type='virtio'/>\n")
	w("    </interface>\n")
	w("    <serial type='pty'><target port='0'/></serial>\n")
	w("    <console type='pty'><target type='serial' port='0'/></console>\n")
	w("    <rng model='virtio'><backend model='random'>/dev/urandom</backend></rng>\n")
	w("  </devices>\n")
	w("</domain>\n")
	return b.String()
}

// snapshotXML is the definition of a new internal snapshot. The seed ISO is
// left out: it is read-only and never changes (docs/adr/0006). A running
// VM's snapshot holds its memory too.
func snapshotXML(name string, running bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<domainsnapshot>\n  <name>%s</name>\n", esc(name))
	if running {
		b.WriteString("  <memory snapshot='internal'/>\n")
	}
	b.WriteString("  <disks>\n    <disk name='vda' snapshot='internal'/>\n    <disk name='sda' snapshot='no'/>\n  </disks>\n")
	b.WriteString("</domainsnapshot>\n")
	return b.String()
}

func parseSnapshot(def string) (api.Snapshot, int64, error) {
	var x struct {
		Name         string `xml:"name"`
		State        string `xml:"state"`
		CreationTime string `xml:"creationTime"`
	}
	if err := xml.Unmarshal([]byte(def), &x); err != nil {
		return api.Snapshot{}, 0, fmt.Errorf("snapshot XML: %w", err)
	}
	created, err := strconv.ParseInt(x.CreationTime, 10, 64)
	if err != nil {
		return api.Snapshot{}, 0, fmt.Errorf("snapshot %s: creation time %q: %w", x.Name, x.CreationTime, err)
	}
	return api.Snapshot{
		Name:      x.Name,
		VMState:   snapshotState(x.State),
		CreatedAt: time.Unix(created, 0).UTC(),
	}, created, nil
}

// snapshotState maps the state words of a snapshot's XML to api states.
func snapshotState(s string) string {
	switch s {
	case "running", "blocked":
		return api.StateRunning
	case "paused":
		return api.StatePaused
	case "shutdown":
		return api.StateShuttingDown
	case "shutoff":
		return api.StateShutOff
	case "crashed":
		return api.StateCrashed
	case "pmsuspended":
		return api.StateSuspended
	default:
		return api.StateUnknown
	}
}
