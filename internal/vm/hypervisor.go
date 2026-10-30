package vm

import "errors"

// Hypervisor is the part of libvirt vmapi uses. internal/libvirt implements
// it over go-libvirt; tests use an in-memory one (vmtest).
type Hypervisor interface {
	// ListDomains returns every domain on the host, vmapi's or not.
	ListDomains() ([]Domain, error)
	// GetDomain returns ErrNoDomain when there is no such domain.
	GetDomain(name string) (Domain, error)
	DefineDomain(xml string) error
	// UndefineDomain removes the domain with its snapshots' metadata.
	UndefineDomain(name string) error
	StartDomain(name string) error
	// ShutdownDomain asks the guest to shut down and does not wait.
	ShutdownDomain(name string) error
	// DestroyDomain turns the domain off at once.
	DestroyDomain(name string) error
	// ListSnapshots returns the XML description of every snapshot.
	ListSnapshots(name string) ([]string, error)
	CreateSnapshot(name, xml string) error
	// DeleteSnapshot and RevertSnapshot return ErrNoSnapshot when there is
	// no such snapshot.
	DeleteSnapshot(name, snapshot string) error
	RevertSnapshot(name, snapshot string) error
}

// Domain is a libvirt domain as vmapi reads it.
type Domain struct {
	Name string
	// XML is the domain's definition, its metadata included.
	XML string
	// State is one of the api.State values.
	State string
}

var (
	// ErrNoDomain is returned for a domain that does not exist.
	ErrNoDomain = errors.New("no such domain")
	// ErrNoSnapshot is returned for a snapshot that does not exist.
	ErrNoSnapshot = errors.New("no such snapshot")
	// ErrUnavailable wraps the errors of a libvirt that cannot be reached.
	ErrUnavailable = errors.New("libvirt is unavailable")
)
