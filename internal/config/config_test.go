package config

import (
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const valid = `
storage:
  dir: /mnt/data/vmapi
network:
  bridge: br0
  range:
    start: 192.168.40.10
    end: 192.168.40.250
  prefix: 16
  gateway: 192.168.0.1
  dns:
    - 192.168.0.1
admins:
  - yuanying
images:
  - name: noble
    description: Ubuntu 24.04 LTS
    url: https://cloud-images.ubuntu.com/releases/noble/release-20261017/ubuntu-24.04-server-cloudimg-amd64.img
    sha256: 04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94
`

func TestParseValid(t *testing.T) {
	c, err := Parse([]byte(valid))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Listen != ":8080" {
		t.Errorf("Listen = %q, want the default :8080", c.Listen)
	}
	if c.Libvirt.URI != "qemu:///system" || c.Libvirt.Socket != "/var/run/libvirt/libvirt-sock" {
		t.Errorf("Libvirt = %+v, want the defaults", c.Libvirt)
	}
	if c.Storage.Dir != "/mnt/data/vmapi" {
		t.Errorf("Storage.Dir = %q", c.Storage.Dir)
	}
	n := c.Network
	if n.Bridge != "br0" || n.Prefix != 16 ||
		n.RangeStart != netip.MustParseAddr("192.168.40.10") ||
		n.RangeEnd != netip.MustParseAddr("192.168.40.250") ||
		n.Gateway != netip.MustParseAddr("192.168.0.1") ||
		len(n.DNS) != 1 || n.DNS[0] != netip.MustParseAddr("192.168.0.1") {
		t.Errorf("Network = %+v", n)
	}
	if !c.IsAdmin("yuanying") || c.IsAdmin("penelope") {
		t.Errorf("IsAdmin is wrong for %v", c.Admins)
	}
	if len(c.Images) != 1 || c.Images[0].Name != "noble" || c.Images[0].Description != "Ubuntu 24.04 LTS" ||
		c.Images[0].SHA256 != "04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94" {
		t.Errorf("Images = %+v", c.Images)
	}
}

func TestParseKeepsGivenDefaults(t *testing.T) {
	c, err := Parse([]byte(valid + "listen: 127.0.0.1:9000\nlibvirt:\n  uri: qemu:///session\n  socket: /run/sock\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Listen != "127.0.0.1:9000" || c.Libvirt.URI != "qemu:///session" || c.Libvirt.Socket != "/run/sock" {
		t.Errorf("got %q %+v", c.Listen, c.Libvirt)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte(valid + "adminz: [x]\n"))
	if err == nil || !strings.Contains(err.Error(), "adminz") {
		t.Fatalf("err = %v, want one naming adminz", err)
	}
}

func TestParseReportsEveryProblem(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		fields []string // each must appear in the error
	}{
		{
			name:   "everything missing",
			yaml:   "admins: []\n",
			fields: []string{"storage.dir", "network.bridge", "network.range.start", "network.range.end", "network.prefix", "network.gateway", "network.dns", "images"},
		},
		{
			name: "bad values",
			yaml: `
storage: {dir: relative/path}
network:
  bridge: br0
  range: {start: 192.168.40.250, end: 192.168.40.10}
  prefix: 40
  gateway: not-an-ip
  dns: [192.168.0.1]
images:
  - {name: Noble, url: "http://example.com/a.img", sha256: xyz}
`,
			fields: []string{"storage.dir", "network.prefix", "network.gateway", "images[0].name", "images[0].url", "images[0].sha256"},
		},
		{
			name: "range reversed",
			yaml: `
storage: {dir: /d}
network:
  bridge: br0
  range: {start: 192.168.40.250, end: 192.168.40.10}
  prefix: 16
  gateway: 192.168.0.1
  dns: [192.168.0.1]
images:
  - {name: noble, url: "https://example.com/a.img", sha256: 04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94}
`,
			fields: []string{"network.range"},
		},
		{
			name: "range outside the subnet, gateway and dns inside the range",
			yaml: `
storage: {dir: /d}
network:
  bridge: br0
  range: {start: 10.0.0.1, end: 192.168.40.10}
  prefix: 16
  gateway: 192.168.0.1
  dns: [192.168.0.1, 10.0.0.5]
images:
  - {name: noble, url: "https://example.com/a.img", sha256: 04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94}
`,
			fields: []string{"network.range.start", "network.dns[1]"},
		},
		{
			name: "gateway inside the range",
			yaml: `
storage: {dir: /d}
network:
  bridge: br0
  range: {start: 192.168.0.1, end: 192.168.0.100}
  prefix: 24
  gateway: 192.168.0.50
  dns: [8.8.8.8]
images:
  - {name: noble, url: "https://example.com/a.img", sha256: 04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94}
  - {name: noble, url: "https://example.com/b.img", sha256: 04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94}
`,
			fields: []string{"network.gateway", "images[1].name"},
		},
		{
			name: "IPv6",
			yaml: `
storage: {dir: /d}
network:
  bridge: br0
  range: {start: "fd00::10", end: "fd00::20"}
  prefix: 64
  gateway: "fd00::1"
  dns: ["fd00::1"]
images:
  - {name: noble, url: "https://example.com/a.img", sha256: 04e8f2461a16a44e78a370284471f29616cdd9baf1893df25807b71f985b6c94}
`,
			fields: []string{"network.range.start", "network.range.end", "network.gateway", "network.dns[0]"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil {
				t.Fatal("Parse succeeded, want an error")
			}
			for _, f := range tt.fields {
				if !strings.Contains(err.Error(), f) {
					t.Errorf("error does not name %s:\n%v", f, err)
				}
			}
		})
	}
}

func TestLoadReadsTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Network.Bridge != "br0" {
		t.Errorf("Bridge = %q", c.Network.Bridge)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("Load of a missing file succeeded")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	t.Setenv("HOME", "/home/someone")
	if got := DefaultPath(); got != "/xdg/vmapi/config.yaml" {
		t.Errorf("with XDG_CONFIG_HOME: %q", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := DefaultPath(); got != "/home/someone/.config/vmapi/config.yaml" {
		t.Errorf("without XDG_CONFIG_HOME: %q", got)
	}
}
