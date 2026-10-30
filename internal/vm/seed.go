package vm

import (
	"fmt"
	"net/netip"

	"go.yaml.in/yaml/v3"

	"github.com/yuanying/vmapi/internal/config"
)

// emptyUserData is the user-data of a VM created without one.
const emptyUserData = "#cloud-config\n"

// metaData is the seed's meta-data: the domain's UUID as the instance-id and
// the VM's name as its hostname (docs/adr/0005).
func metaData(uuid, name string) ([]byte, error) {
	return yaml.Marshal(map[string]string{"instance-id": uuid, "local-hostname": name})
}

// networkConfig is the seed's network-config, version 2. The interface is
// matched by MAC, as its name depends on the machine type.
func networkConfig(n config.Network, mac string, ip netip.Addr) ([]byte, error) {
	type route struct {
		To  string `yaml:"to"`
		Via string `yaml:"via"`
	}
	type ethernet struct {
		Match struct {
			MACAddress string `yaml:"macaddress"`
		} `yaml:"match"`
		Addresses   []string `yaml:"addresses"`
		Routes      []route  `yaml:"routes"`
		Nameservers struct {
			Addresses []string `yaml:"addresses"`
		} `yaml:"nameservers"`
	}
	var eth ethernet
	eth.Match.MACAddress = mac
	eth.Addresses = []string{fmt.Sprintf("%s/%d", ip, n.Prefix)}
	eth.Routes = []route{{To: "default", Via: n.Gateway.String()}}
	for _, a := range n.DNS {
		eth.Nameservers.Addresses = append(eth.Nameservers.Addresses, a.String())
	}
	return yaml.Marshal(struct {
		Version   int                 `yaml:"version"`
		Ethernets map[string]ethernet `yaml:"ethernets"`
	}{2, map[string]ethernet{"primary": eth}})
}
