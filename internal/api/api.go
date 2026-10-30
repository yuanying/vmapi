// Package api holds the wire types of the vmapi HTTP API, as api/openapi.yaml
// defines them. The server and vmctl both speak in these types.
package api

import (
	"fmt"
	"net/http"
	"time"
)

// Whoami is the caller as vmapi sees it.
type Whoami struct {
	User  string `json:"user"`
	Admin bool   `json:"admin"`
}

// Image is an OS image a VM can be created from.
type Image struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// ImageList wraps the images.
type ImageList struct {
	Images []Image `json:"images"`
}

// Known VM states. More may be added (docs/adr/0007).
const (
	StateRunning      = "running"
	StatePaused       = "paused"
	StateShuttingDown = "shutting_down"
	StateShutOff      = "shut_off"
	StateCrashed      = "crashed"
	StateSuspended    = "suspended"
	StateUnknown      = "unknown"
)

// VM is a VM created by vmapi.
type VM struct {
	Name      string    `json:"name"`
	Owner     string    `json:"owner"`
	Image     string    `json:"image"`
	VCPUs     int       `json:"vcpus"`
	MemoryMiB int       `json:"memory_mib"`
	DiskGiB   int       `json:"disk_gib"`
	IP        string    `json:"ip"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

// VMList wraps the VMs.
type VMList struct {
	VMs []VM `json:"vms"`
}

// CreateVMRequest is the body of createVM. Pointers tell an omitted field
// from a zero.
type CreateVMRequest struct {
	Name      string  `json:"name"`
	Image     string  `json:"image"`
	VCPUs     *int    `json:"vcpus,omitempty"`
	MemoryMiB *int    `json:"memory_mib,omitempty"`
	DiskGiB   *int    `json:"disk_gib,omitempty"`
	IP        *string `json:"ip,omitempty"`
	UserData  *string `json:"user_data,omitempty"`
}

// StopVMRequest is the body of stopVM.
type StopVMRequest struct {
	Force bool `json:"force,omitempty"`
}

// Snapshot is a snapshot of a VM.
type Snapshot struct {
	Name      string    `json:"name"`
	VMState   string    `json:"vm_state"`
	CreatedAt time.Time `json:"created_at"`
}

// SnapshotList wraps the snapshots.
type SnapshotList struct {
	Snapshots []Snapshot `json:"snapshots"`
}

// CreateSnapshotRequest is the body of createSnapshot.
type CreateSnapshotRequest struct {
	Name string `json:"name"`
}

// Error codes. More may be added (docs/adr/0007).
const (
	CodeInvalidArgument  = "invalid_argument"
	CodeUnauthenticated  = "unauthenticated"
	CodePermissionDenied = "permission_denied"
	CodeNotFound         = "not_found"
	CodeAlreadyExists    = "already_exists"
	CodeIPInUse          = "ip_in_use"
	CodeIPExhausted      = "ip_exhausted"
	CodeUnavailable      = "unavailable"
	CodeInternal         = "internal"
)

// Error is an error with a code from the contract. It is also the body of
// every error response, inside ErrorBody.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Errorf makes an Error with the code and a formatted message.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ErrorBody is the shape of every error response.
type ErrorBody struct {
	Error Error `json:"error"`
}

// HTTPStatus is the status the code is sent with. An unknown code is
// treated as internal.
func HTTPStatus(code string) int {
	switch code {
	case CodeInvalidArgument:
		return http.StatusBadRequest
	case CodeUnauthenticated:
		return http.StatusUnauthorized
	case CodePermissionDenied:
		return http.StatusForbidden
	case CodeNotFound:
		return http.StatusNotFound
	case CodeAlreadyExists, CodeIPInUse, CodeIPExhausted:
		return http.StatusConflict
	case CodeUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
