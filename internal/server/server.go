// Package server is vmapi's HTTP side: the JSON API of api/openapi.yaml
// under /api/v1/, and the page at / with its buttons.
package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/vm"
)

// userHeader carries the caller's name, set by devbox proxy (docs/adr/0002).
const userHeader = "X-Devbox-User"

// maxBody is the largest request body taken: user-data is the only big field.
const maxBody = 1 << 20

// Server serves the API and the page over a vm.Service.
type Server struct {
	Service *vm.Service
	Logger  *slog.Logger
}

type route struct {
	pattern string
	handle  func(s *Server, w http.ResponseWriter, r *http.Request, c vm.Caller)
}

// apiRoutes are the operations of api/openapi.yaml; the patterns are its
// paths as they are written there.
var apiRoutes = []route{
	{"GET /api/v1/whoami", (*Server).whoami},
	{"GET /api/v1/images", (*Server).listImages},
	{"GET /api/v1/vms", (*Server).listVMs},
	{"POST /api/v1/vms", (*Server).createVM},
	{"GET /api/v1/vms/{name}", (*Server).getVM},
	{"DELETE /api/v1/vms/{name}", (*Server).deleteVM},
	{"POST /api/v1/vms/{name}/start", (*Server).startVM},
	{"POST /api/v1/vms/{name}/stop", (*Server).stopVM},
	{"GET /api/v1/vms/{name}/snapshots", (*Server).listSnapshots},
	{"POST /api/v1/vms/{name}/snapshots", (*Server).createSnapshot},
	{"DELETE /api/v1/vms/{name}/snapshots/{snapshot}", (*Server).deleteSnapshot},
	{"POST /api/v1/vms/{name}/snapshots/{snapshot}/revert", (*Server).revertSnapshot},
}

// APIRoutes lists the API's routes as "METHOD /path", for the tests that
// hold them to the spec.
func APIRoutes() []string {
	out := make([]string, 0, len(apiRoutes))
	for _, r := range apiRoutes {
		out = append(out, r.pattern)
	}
	return out
}

// Handler is the whole of vmapi's HTTP side.
//
// Browsers reach vmapi with the proxy's session cookie, so a page on any
// other site could make them send a POST that the proxy would pass on with
// the user's name. Every unsafe request that a browser marks as coming from
// another origin (Sec-Fetch-Site, or an Origin whose host is not the
// request's) is refused. vmctl and other programs send neither header and
// pass.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range apiRoutes {
		mux.Handle(r.pattern, s.withCaller(r.handle, s.writeAPIError))
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.writeAPIError(w, r, api.Errorf(api.CodeNotFound, "no such endpoint: %s %s", r.Method, r.URL.Path))
	})
	mux.Handle("GET /{$}", s.withCaller((*Server).page, s.writePageError))
	mux.Handle("POST /ui/vms/{name}/{action}", s.withCaller((*Server).pageAction, s.writePageError))

	csrf := http.NewCrossOriginProtection()
	csrf.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.writeAPIError(w, r, api.Errorf(api.CodePermissionDenied, "cross-origin request refused"))
	}))
	return s.logged(csrf.Handler(mux))
}

type errorWriter func(w http.ResponseWriter, r *http.Request, err error)

// withCaller turns X-Devbox-User into the Caller; without it, the request
// did not come through the proxy and is refused.
func (s *Server) withCaller(h func(*Server, http.ResponseWriter, *http.Request, vm.Caller), fail errorWriter) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.Header.Get(userHeader)
		if user == "" {
			fail(w, r, api.Errorf(api.CodeUnauthenticated, "no %s: the request did not come through the proxy", userHeader))
			return
		}
		h(s, w, r, vm.Caller{User: user, Admin: s.Service.Config.IsAdmin(user)})
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (s *Server) logged(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rec, r)
		s.Logger.Info("request", "method", r.Method, "path", r.URL.Path, "status", rec.status,
			"user", r.Header.Get(userHeader), "duration", time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// asAPIError makes any error one of the contract's.
func asAPIError(err error) *api.Error {
	var e *api.Error
	if errors.As(err, &e) {
		return e
	}
	return &api.Error{Code: api.CodeInternal, Message: err.Error()}
}

func (s *Server) writeAPIError(w http.ResponseWriter, r *http.Request, err error) {
	e := asAPIError(err)
	status := api.HTTPStatus(e.Code)
	if status >= 500 {
		s.Logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", e.Message)
	}
	writeJSON(w, status, api.ErrorBody{Error: *e})
}

// decodeBody reads one JSON object into v. Unknown fields are refused
// (docs/adr/0007). An empty body is refused unless optional.
func decodeBody(r *http.Request, w http.ResponseWriter, v any, optional bool) error {
	body := http.MaxBytesReader(w, r.Body, maxBody)
	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return api.Errorf(api.CodeInvalidArgument, "the request body is over %d bytes", maxBody)
		}
		return api.Errorf(api.CodeInvalidArgument, "reading the request body: %v", err)
	}
	if len(data) == 0 && optional {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return api.Errorf(api.CodeInvalidArgument, "the request body: %v", err)
	}
	if dec.More() {
		return api.Errorf(api.CodeInvalidArgument, "the request body has more than one JSON value")
	}
	return nil
}

func (s *Server) whoami(w http.ResponseWriter, _ *http.Request, c vm.Caller) {
	writeJSON(w, http.StatusOK, api.Whoami{User: c.User, Admin: c.Admin})
}

func (s *Server) listImages(w http.ResponseWriter, _ *http.Request, _ vm.Caller) {
	writeJSON(w, http.StatusOK, api.ImageList{Images: s.Service.Images()})
}

func (s *Server) listVMs(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	vms, err := s.Service.List(r.Context(), c, r.URL.Query().Get("owner"))
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.VMList{VMs: vms})
}

func (s *Server) createVM(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	var req api.CreateVMRequest
	if err := decodeBody(r, w, &req, false); err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	v, err := s.Service.Create(r.Context(), c, req)
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/vms/"+v.Name)
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) getVM(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	v, err := s.Service.Get(r.Context(), c, r.PathValue("name"))
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteVM(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	if err := s.Service.Delete(r.Context(), c, r.PathValue("name")); err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) startVM(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	v, err := s.Service.Start(r.Context(), c, r.PathValue("name"))
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) stopVM(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	var req api.StopVMRequest
	if err := decodeBody(r, w, &req, true); err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	v, accepted, err := s.Service.Stop(r.Context(), c, r.PathValue("name"), req.Force)
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	status := http.StatusOK
	if accepted {
		status = http.StatusAccepted
	}
	writeJSON(w, status, v)
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	snaps, err := s.Service.Snapshots(r.Context(), c, r.PathValue("name"))
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.SnapshotList{Snapshots: snaps})
}

func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	var req api.CreateSnapshotRequest
	if err := decodeBody(r, w, &req, false); err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	name := r.PathValue("name")
	snap, err := s.Service.CreateSnapshot(r.Context(), c, name, req.Name)
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/vms/"+name+"/snapshots/"+snap.Name)
	writeJSON(w, http.StatusCreated, snap)
}

func (s *Server) deleteSnapshot(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	if err := s.Service.DeleteSnapshot(r.Context(), c, r.PathValue("name"), r.PathValue("snapshot")); err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) revertSnapshot(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	v, err := s.Service.RevertSnapshot(r.Context(), c, r.PathValue("name"), r.PathValue("snapshot"))
	if err != nil {
		s.writeAPIError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
