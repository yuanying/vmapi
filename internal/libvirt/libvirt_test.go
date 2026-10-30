package libvirt

import (
	"errors"
	"fmt"
	"testing"

	golibvirt "github.com/digitalocean/go-libvirt"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/vm"
)

func TestState(t *testing.T) {
	tests := map[golibvirt.DomainState]string{
		golibvirt.DomainNostate:     api.StateUnknown,
		golibvirt.DomainRunning:     api.StateRunning,
		golibvirt.DomainBlocked:     api.StateRunning,
		golibvirt.DomainPaused:      api.StatePaused,
		golibvirt.DomainShutdown:    api.StateShuttingDown,
		golibvirt.DomainShutoff:     api.StateShutOff,
		golibvirt.DomainCrashed:     api.StateCrashed,
		golibvirt.DomainPmsuspended: api.StateSuspended,
		golibvirt.DomainState(99):   api.StateUnknown,
	}
	for in, want := range tests {
		if got := state(int32(in)); got != want {
			t.Errorf("state(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestClassify(t *testing.T) {
	noDomain := golibvirt.Error{Code: uint32(golibvirt.ErrNoDomain), Message: "Domain not found"}
	noSnapshot := golibvirt.Error{Code: uint32(golibvirt.ErrNoDomainSnapshot), Message: "Snapshot not found"}
	other := golibvirt.Error{Code: uint32(golibvirt.ErrOperationInvalid), Message: "invalid"}

	if err := classify(fmt.Errorf("lookup: %w", noDomain), true); !errors.Is(err, vm.ErrNoDomain) {
		t.Errorf("no domain: %v", err)
	}
	if err := classify(noSnapshot, true); !errors.Is(err, vm.ErrNoSnapshot) {
		t.Errorf("no snapshot: %v", err)
	}
	if err := classify(other, true); errors.Is(err, vm.ErrUnavailable) || errors.Is(err, vm.ErrNoDomain) {
		t.Errorf("other: %v", err)
	}
	if err := classify(errors.New("broken pipe"), false); !errors.Is(err, vm.ErrUnavailable) {
		t.Errorf("disconnected: %v", err)
	}
	if classify(nil, true) != nil {
		t.Error("nil is not nil")
	}
}
