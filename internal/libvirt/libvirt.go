// Package libvirt implements vm.Hypervisor over libvirt's socket with
// go-libvirt. It needs a libvirt to talk to and is not covered by
// `make test` beyond its pure parts.
package libvirt

import (
	"errors"
	"fmt"
	"sync"
	"time"

	golibvirt "github.com/digitalocean/go-libvirt"
	"github.com/digitalocean/go-libvirt/socket/dialers"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/vm"
)

// Hypervisor is a connection to libvirt, made when first needed and made
// again after it is lost.
type Hypervisor struct {
	uri    string
	socket string

	mu sync.Mutex
	l  *golibvirt.Libvirt
}

var _ vm.Hypervisor = (*Hypervisor)(nil)

// New is a Hypervisor for the URI over the Unix socket.
func New(uri, socket string) *Hypervisor {
	return &Hypervisor{uri: uri, socket: socket}
}

func (h *Hypervisor) conn() (*golibvirt.Libvirt, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.l != nil && h.l.IsConnected() {
		return h.l, nil
	}
	l := golibvirt.NewWithDialer(dialers.NewLocal(dialers.WithSocket(h.socket), dialers.WithLocalTimeout(5*time.Second)))
	if err := l.ConnectToURI(golibvirt.ConnectURI(h.uri)); err != nil {
		return nil, fmt.Errorf("%w: connecting to %s at %s: %v", vm.ErrUnavailable, h.uri, h.socket, err)
	}
	h.l = l
	return l, nil
}

// Close drops the connection.
func (h *Hypervisor) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.l == nil || !h.l.IsConnected() {
		return nil
	}
	return h.l.Disconnect()
}

func hasCode(err error, code golibvirt.ErrorNumber) bool {
	var e golibvirt.Error
	return errors.As(err, &e) && e.Code == uint32(code)
}

// classify turns go-libvirt's errors into vm's. connected is whether the
// connection was still up after the call.
func classify(err error, connected bool) error {
	switch {
	case err == nil:
		return nil
	case hasCode(err, golibvirt.ErrNoDomain):
		return fmt.Errorf("%w: %v", vm.ErrNoDomain, err)
	case hasCode(err, golibvirt.ErrNoDomainSnapshot):
		return fmt.Errorf("%w: %v", vm.ErrNoSnapshot, err)
	case !connected:
		return fmt.Errorf("%w: %v", vm.ErrUnavailable, err)
	default:
		return err
	}
}

// call runs f on the connection and classifies what it returns.
func (h *Hypervisor) call(f func(l *golibvirt.Libvirt) error) error {
	l, err := h.conn()
	if err != nil {
		return err
	}
	err = f(l)
	return classify(err, l.IsConnected())
}

// state maps libvirt's domain states to the api's.
func state(s int32) string {
	switch golibvirt.DomainState(s) {
	case golibvirt.DomainRunning, golibvirt.DomainBlocked:
		return api.StateRunning
	case golibvirt.DomainPaused:
		return api.StatePaused
	case golibvirt.DomainShutdown:
		return api.StateShuttingDown
	case golibvirt.DomainShutoff:
		return api.StateShutOff
	case golibvirt.DomainCrashed:
		return api.StateCrashed
	case golibvirt.DomainPmsuspended:
		return api.StateSuspended
	default:
		return api.StateUnknown
	}
}

func read(l *golibvirt.Libvirt, d golibvirt.Domain) (vm.Domain, error) {
	def, err := l.DomainGetXMLDesc(d, 0)
	if err != nil {
		return vm.Domain{}, err
	}
	s, _, err := l.DomainGetState(d, 0)
	if err != nil {
		return vm.Domain{}, err
	}
	return vm.Domain{Name: d.Name, XML: def, State: state(s)}, nil
}

// ListDomains reads every domain, defined or running. One that goes away
// while being read is left out.
func (h *Hypervisor) ListDomains() ([]vm.Domain, error) {
	var out []vm.Domain
	err := h.call(func(l *golibvirt.Libvirt) error {
		domains, _, err := l.ConnectListAllDomains(1, golibvirt.ConnectListDomainsActive|golibvirt.ConnectListDomainsInactive)
		if err != nil {
			return err
		}
		for _, d := range domains {
			dom, err := read(l, d)
			if hasCode(err, golibvirt.ErrNoDomain) {
				continue
			}
			if err != nil {
				return err
			}
			out = append(out, dom)
		}
		return nil
	})
	return out, err
}

func (h *Hypervisor) GetDomain(name string) (vm.Domain, error) {
	var out vm.Domain
	err := h.call(func(l *golibvirt.Libvirt) error {
		d, err := l.DomainLookupByName(name)
		if err != nil {
			return err
		}
		out, err = read(l, d)
		return err
	})
	return out, err
}

// DefineDomain defines the domain, having libvirt validate the XML.
func (h *Hypervisor) DefineDomain(def string) error {
	return h.call(func(l *golibvirt.Libvirt) error {
		_, err := l.DomainDefineXMLFlags(def, golibvirt.DomainDefineValidate)
		return err
	})
}

// withDomain looks the domain up and runs f on it.
func (h *Hypervisor) withDomain(name string, f func(l *golibvirt.Libvirt, d golibvirt.Domain) error) error {
	return h.call(func(l *golibvirt.Libvirt) error {
		d, err := l.DomainLookupByName(name)
		if err != nil {
			return err
		}
		return f(l, d)
	})
}

func (h *Hypervisor) UndefineDomain(name string) error {
	return h.withDomain(name, func(l *golibvirt.Libvirt, d golibvirt.Domain) error {
		return l.DomainUndefineFlags(d, golibvirt.DomainUndefineSnapshotsMetadata|golibvirt.DomainUndefineManagedSave)
	})
}

func (h *Hypervisor) StartDomain(name string) error {
	return h.withDomain(name, func(l *golibvirt.Libvirt, d golibvirt.Domain) error {
		return l.DomainCreate(d)
	})
}

func (h *Hypervisor) ShutdownDomain(name string) error {
	return h.withDomain(name, func(l *golibvirt.Libvirt, d golibvirt.Domain) error {
		return l.DomainShutdown(d)
	})
}

func (h *Hypervisor) DestroyDomain(name string) error {
	return h.withDomain(name, func(l *golibvirt.Libvirt, d golibvirt.Domain) error {
		return l.DomainDestroy(d)
	})
}

func (h *Hypervisor) ListSnapshots(name string) ([]string, error) {
	var out []string
	err := h.withDomain(name, func(l *golibvirt.Libvirt, d golibvirt.Domain) error {
		snaps, _, err := l.DomainListAllSnapshots(d, 1, 0)
		if err != nil {
			return err
		}
		for _, s := range snaps {
			def, err := l.DomainSnapshotGetXMLDesc(s, 0)
			if hasCode(err, golibvirt.ErrNoDomainSnapshot) {
				continue
			}
			if err != nil {
				return err
			}
			out = append(out, def)
		}
		return nil
	})
	return out, err
}

func (h *Hypervisor) CreateSnapshot(name, def string) error {
	return h.withDomain(name, func(l *golibvirt.Libvirt, d golibvirt.Domain) error {
		_, err := l.DomainSnapshotCreateXML(d, def, 0)
		return err
	})
}

func (h *Hypervisor) withSnapshot(name, snap string, f func(l *golibvirt.Libvirt, s golibvirt.DomainSnapshot) error) error {
	return h.withDomain(name, func(l *golibvirt.Libvirt, d golibvirt.Domain) error {
		s, err := l.DomainSnapshotLookupByName(d, snap, 0)
		if err != nil {
			return err
		}
		return f(l, s)
	})
}

func (h *Hypervisor) DeleteSnapshot(name, snap string) error {
	return h.withSnapshot(name, snap, func(l *golibvirt.Libvirt, s golibvirt.DomainSnapshot) error {
		return l.DomainSnapshotDelete(s, 0)
	})
}

// RevertSnapshot returns the domain to the snapshot, in the state the
// snapshot was taken in.
func (h *Hypervisor) RevertSnapshot(name, snap string) error {
	return h.withSnapshot(name, snap, func(l *golibvirt.Libvirt, s golibvirt.DomainSnapshot) error {
		return l.DomainRevertToSnapshot(s, 0)
	})
}
