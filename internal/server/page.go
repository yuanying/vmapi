package server

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/vm"
)

//go:embed page.html
var pageHTML string

var pageTemplate = template.Must(template.New("page").Parse(pageHTML))

type pageData struct {
	Caller vm.Caller
	VMs    []api.VM
	Error  string
}

func (s *Server) renderPage(w http.ResponseWriter, status int, data pageData) {
	var buf bytes.Buffer
	if err := pageTemplate.Execute(&buf, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}

// page lists the caller's VMs, every VM for an admin.
func (s *Server) page(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	vms, err := s.Service.List(r.Context(), c, "")
	if err != nil {
		s.writePageError(w, r, err)
		return
	}
	s.renderPage(w, http.StatusOK, pageData{Caller: c, VMs: vms})
}

// pageAction is a button of the page. It goes back to the page when done.
func (s *Server) pageAction(w http.ResponseWriter, r *http.Request, c vm.Caller) {
	name := r.PathValue("name")
	var err error
	switch r.PathValue("action") {
	case "start":
		_, err = s.Service.Start(r.Context(), c, name)
	case "stop":
		_, _, err = s.Service.Stop(r.Context(), c, name, false)
	case "delete":
		err = s.Service.Delete(r.Context(), c, name)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.writePageError(w, r, err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// writePageError shows the error on the page, with the list when it can
// still be had.
func (s *Server) writePageError(w http.ResponseWriter, r *http.Request, err error) {
	e := asAPIError(err)
	status := api.HTTPStatus(e.Code)
	if status >= 500 {
		s.Logger.Error("request failed", "method", r.Method, "path", r.URL.Path, "error", e.Message)
	}
	data := pageData{Error: e.Message}
	if user := r.Header.Get(userHeader); user != "" && e.Code != api.CodeUnauthenticated {
		data.Caller = vm.Caller{User: user, Admin: s.Service.Config.IsAdmin(user)}
		data.VMs, _ = s.Service.List(r.Context(), data.Caller, "")
	}
	s.renderPage(w, status, data)
}
