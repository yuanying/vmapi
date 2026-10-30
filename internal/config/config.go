// Package config reads the vmapi configuration file (docs/adr/0009,
// docs/config.md).
package config

import (
	"bytes"
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/yuanying/vmapi/internal/api"
)

// Config is the validated configuration.
type Config struct {
	Listen  string
	Libvirt Libvirt
	Storage Storage
	Network Network
	Admins  []string
	Images  []Image
}

// Libvirt is where libvirt is reached.
type Libvirt struct {
	URI    string
	Socket string
}

// Storage is where images and VM files are kept.
type Storage struct {
	Dir string
}

// Network is what VMs are connected to and the addresses they are given.
type Network struct {
	Bridge     string
	RangeStart netip.Addr
	RangeEnd   netip.Addr
	Prefix     int
	Gateway    netip.Addr
	DNS        []netip.Addr
}

// Image is an OS image VMs can be created from.
type Image struct {
	Name        string
	Description string
	URL         string
	SHA256      string
}

// IsAdmin reports whether user is one of the admins.
func (c *Config) IsAdmin(user string) bool {
	return slices.Contains(c.Admins, user)
}

// Image returns the image of the name.
func (c *Config) Image(name string) (Image, bool) {
	for _, img := range c.Images {
		if img.Name == name {
			return img, true
		}
	}
	return Image{}, false
}

// DefaultPath is the file read when --config is not given:
// $XDG_CONFIG_HOME/vmapi/config.yaml, or ~/.config/vmapi/config.yaml.
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "vmapi", "config.yaml")
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "vmapi", "config.yaml")
}

// Load reads and validates the file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// The file as written. Everything is a string so that a malformed value is
// reported by validate, with its field name, along with every other problem.
type file struct {
	Listen  string `yaml:"listen"`
	Libvirt struct {
		URI    string `yaml:"uri"`
		Socket string `yaml:"socket"`
	} `yaml:"libvirt"`
	Storage struct {
		Dir string `yaml:"dir"`
	} `yaml:"storage"`
	Network struct {
		Bridge string `yaml:"bridge"`
		Range  struct {
			Start string `yaml:"start"`
			End   string `yaml:"end"`
		} `yaml:"range"`
		Prefix  int      `yaml:"prefix"`
		Gateway string   `yaml:"gateway"`
		DNS     []string `yaml:"dns"`
	} `yaml:"network"`
	Admins []string `yaml:"admins"`
	Images []struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
		URL         string `yaml:"url"`
		SHA256      string `yaml:"sha256"`
	} `yaml:"images"`
}

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Parse decodes and validates a configuration. Unknown fields are errors,
// and every problem found is reported, each with its field name.
func Parse(data []byte) (*Config, error) {
	var f file
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil {
		return nil, err
	}
	return f.validate()
}

func (f *file) validate() (*Config, error) {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }

	c := &Config{
		Listen:  orDefault(f.Listen, ":8080"),
		Libvirt: Libvirt{URI: orDefault(f.Libvirt.URI, "qemu:///system"), Socket: orDefault(f.Libvirt.Socket, "/var/run/libvirt/libvirt-sock")},
		Storage: Storage{Dir: f.Storage.Dir},
		Admins:  f.Admins,
	}

	switch {
	case f.Storage.Dir == "":
		add("storage.dir is required")
	case !filepath.IsAbs(f.Storage.Dir):
		add("storage.dir must be an absolute path, not %q", f.Storage.Dir)
	}

	n := &c.Network
	n.Bridge = f.Network.Bridge
	if n.Bridge == "" {
		add("network.bridge is required")
	}
	ipv4 := func(field, s string) netip.Addr {
		if s == "" {
			add("%s is required", field)
			return netip.Addr{}
		}
		a, err := netip.ParseAddr(s)
		if err != nil || !a.Is4() {
			add("%s must be an IPv4 address, not %q", field, s)
			return netip.Addr{}
		}
		return a
	}
	n.RangeStart = ipv4("network.range.start", f.Network.Range.Start)
	n.RangeEnd = ipv4("network.range.end", f.Network.Range.End)
	n.Gateway = ipv4("network.gateway", f.Network.Gateway)
	n.Prefix = f.Network.Prefix
	switch {
	case f.Network.Prefix == 0:
		add("network.prefix is required")
	case f.Network.Prefix < 1 || f.Network.Prefix > 32:
		add("network.prefix must be between 1 and 32, not %d", f.Network.Prefix)
	}
	if len(f.Network.DNS) == 0 {
		add("network.dns needs at least one address")
	}
	for i, s := range f.Network.DNS {
		n.DNS = append(n.DNS, ipv4(fmt.Sprintf("network.dns[%d]", i), s))
	}

	if n.RangeStart.IsValid() && n.RangeEnd.IsValid() && n.RangeEnd.Less(n.RangeStart) {
		add("network.range: start %s is after end %s", n.RangeStart, n.RangeEnd)
	}
	if n.Gateway.IsValid() && n.Prefix >= 1 && n.Prefix <= 32 {
		subnet := netip.PrefixFrom(n.Gateway, n.Prefix).Masked()
		for _, e := range []struct {
			field string
			addr  netip.Addr
		}{{"network.range.start", n.RangeStart}, {"network.range.end", n.RangeEnd}} {
			if e.addr.IsValid() && !subnet.Contains(e.addr) {
				add("%s %s is outside the gateway's subnet %s", e.field, e.addr, subnet)
			}
		}
	}
	if n.InRange(n.Gateway) {
		add("network.gateway %s is inside network.range", n.Gateway)
	}
	for i, a := range n.DNS {
		if n.InRange(a) {
			add("network.dns[%d] %s is inside network.range", i, a)
		}
	}

	for i, a := range f.Admins {
		if a == "" {
			add("admins[%d] is empty", i)
		}
	}

	if len(f.Images) == 0 {
		add("images needs at least one image")
	}
	seen := map[string]bool{}
	for i, img := range f.Images {
		field := fmt.Sprintf("images[%d]", i)
		switch {
		case !api.ValidName(img.Name):
			add("%s.name must be a DNS label (lowercase letter first, then lowercase letters, digits and hyphens), not %q", field, img.Name)
		case seen[img.Name]:
			add("%s.name %q is used twice", field, img.Name)
		}
		seen[img.Name] = true
		if u, err := url.Parse(img.URL); err != nil || u.Scheme != "https" || u.Host == "" {
			add("%s.url must be an https:// URL, not %q", field, img.URL)
		}
		sum := strings.ToLower(img.SHA256)
		if !sha256Pattern.MatchString(sum) {
			add("%s.sha256 must be 64 hexadecimal digits", field)
		}
		c.Images = append(c.Images, Image{Name: img.Name, Description: img.Description, URL: img.URL, SHA256: sum})
	}

	if len(problems) > 0 {
		return nil, &Error{Problems: problems}
	}
	return c, nil
}

// InRange reports whether a is inside the range addresses are given from.
func (n *Network) InRange(a netip.Addr) bool {
	if !a.IsValid() || !n.RangeStart.IsValid() || !n.RangeEnd.IsValid() {
		return false
	}
	return !a.Less(n.RangeStart) && !n.RangeEnd.Less(a)
}

// Error lists every problem found in a configuration.
type Error struct {
	Problems []string
}

func (e *Error) Error() string {
	return "invalid configuration:\n  " + strings.Join(e.Problems, "\n  ")
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
