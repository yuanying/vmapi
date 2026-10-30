package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/server"
	"github.com/yuanying/vmapi/internal/vm/vmtest"
)

type env struct {
	*vmtest.Env
	handler http.Handler
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := vmtest.New(t, vmtest.Config(t))
	s := &server.Server{Service: e.Service, Logger: slog.New(slog.DiscardHandler)}
	return &env{Env: e, handler: s.Handler()}
}

type reqOpt func(*http.Request)

func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

func (e *env) do(t *testing.T, user, method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("X-Devbox-User", user)
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return v
}

func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, status, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := decode[api.ErrorBody](t, rec)
	if body.Error.Code != code || body.Error.Message == "" {
		t.Errorf("error = %+v, want code %s", body.Error, code)
	}
}

func (e *env) createVM(t *testing.T, user, name string) {
	t.Helper()
	rec := e.do(t, user, "POST", "/api/v1/vms", `{"name":"`+name+`","image":"noble"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, rec.Code, rec.Body.String())
	}
}

func TestEveryRouteWantsAUser(t *testing.T) {
	e := newEnv(t)
	for _, op := range loadSpec(t).operations() {
		path := strings.NewReplacer("{name}", "a", "{snapshot}", "s").Replace(op.path)
		rec := e.do(t, "", op.method, path, "")
		t.Run(op.id, func(t *testing.T) { wantError(t, rec, http.StatusUnauthorized, api.CodeUnauthenticated) })
	}
	if rec := e.do(t, "", "GET", "/", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("page without a user: %d", rec.Code)
	}
}

func TestWhoami(t *testing.T) {
	e := newEnv(t)
	got := decode[api.Whoami](t, e.do(t, "penelope", "GET", "/api/v1/whoami", ""))
	if got != (api.Whoami{User: "penelope"}) {
		t.Errorf("whoami = %+v", got)
	}
	got = decode[api.Whoami](t, e.do(t, "admin", "GET", "/api/v1/whoami", ""))
	if got != (api.Whoami{User: "admin", Admin: true}) {
		t.Errorf("whoami = %+v", got)
	}
}

func TestImages(t *testing.T) {
	e := newEnv(t)
	got := decode[api.ImageList](t, e.do(t, "pi", "GET", "/api/v1/images", ""))
	if len(got.Images) != 2 || got.Images[0].Name != "noble" {
		t.Errorf("images = %+v", got)
	}
}

func TestCreateVM(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "penelope", "POST", "/api/v1/vms", `{"name":"k8s-test-cp1","image":"noble","vcpus":4,"user_data":"#cloud-config\n"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/vms/k8s-test-cp1" {
		t.Errorf("Location = %q", loc)
	}
	v := decode[api.VM](t, rec)
	if v.Name != "k8s-test-cp1" || v.Owner != "penelope" || v.VCPUs != 4 || v.IP != "192.168.40.10" || v.State != api.StateRunning {
		t.Errorf("vm = %+v", v)
	}
}

func TestCreateVMRejectsBadBodies(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]string{
		"unknown field":     `{"name":"a","image":"noble","memory":4096}`,
		"not JSON":          `{"name":`,
		"wrong type":        `{"name":"a","image":"noble","vcpus":"two"}`,
		"two values":        `{"name":"a","image":"noble"} {}`,
		"empty":             ``,
		"over a mebibyte":   `{"name":"a","image":"noble","user_data":"` + strings.Repeat("x", 1<<20) + `"}`,
		"IP out of range":   `{"name":"a","image":"noble","ip":"10.0.0.1"}`,
		"name not a label":  `{"name":"A","image":"noble"}`,
		"negative vcpus":    `{"name":"a","image":"noble","vcpus":-1}`,
		"array, not object": `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			wantError(t, e.do(t, "pi", "POST", "/api/v1/vms", body), http.StatusBadRequest, api.CodeInvalidArgument)
		})
	}
}

func TestCreateVMConflicts(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "penelope", "a")
	wantError(t, e.do(t, "pi", "POST", "/api/v1/vms", `{"name":"a","image":"noble"}`), http.StatusConflict, api.CodeAlreadyExists)
	wantError(t, e.do(t, "pi", "POST", "/api/v1/vms", `{"name":"b","image":"noble","ip":"192.168.40.10"}`), http.StatusConflict, api.CodeIPInUse)
	wantError(t, e.do(t, "pi", "POST", "/api/v1/vms", `{"name":"b","image":"plucky"}`), http.StatusNotFound, api.CodeNotFound)
}

func TestListVMsByOwner(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "penelope", "p1")
	e.createVM(t, "pi", "x1")

	got := decode[api.VMList](t, e.do(t, "penelope", "GET", "/api/v1/vms", ""))
	if len(got.VMs) != 2 {
		t.Errorf("penelope's list = %+v", got)
	}
	got = decode[api.VMList](t, e.do(t, "penelope", "GET", "/api/v1/vms?owner=pi", ""))
	if len(got.VMs) != 1 || got.VMs[0].Name != "x1" || got.VMs[0].Owner != "pi" {
		t.Errorf("penelope's list of pi = %+v", got)
	}

	got = decode[api.VMList](t, e.do(t, "admin", "GET", "/api/v1/vms", ""))
	if len(got.VMs) != 2 {
		t.Errorf("admin's list = %+v", got)
	}
	got = decode[api.VMList](t, e.do(t, "admin", "GET", "/api/v1/vms?owner=pi", ""))
	if len(got.VMs) != 1 || got.VMs[0].Name != "x1" {
		t.Errorf("admin's list of pi = %+v", got)
	}

	// An empty list is an empty array, not null.
	if rec := e.do(t, "nobody", "GET", "/api/v1/vms?owner=nobody", ""); strings.TrimSpace(rec.Body.String()) != `{"vms":[]}` {
		t.Errorf("empty list = %s", rec.Body.String())
	}
}

func TestOthersSeeAVMButCannotOperateIt(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "penelope", "p1")
	if rec := e.do(t, "penelope", "POST", "/api/v1/vms/p1/snapshots", `{"name":"s"}`); rec.Code != http.StatusCreated {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}
	before := e.Hypervisor.Calls()

	if v := decode[api.VM](t, e.do(t, "pi", "GET", "/api/v1/vms/p1", "")); v.Owner != "penelope" {
		t.Errorf("pi getting p1: %+v", v)
	}
	if got := decode[api.SnapshotList](t, e.do(t, "pi", "GET", "/api/v1/vms/p1/snapshots", "")); len(got.Snapshots) != 1 {
		t.Errorf("pi listing p1's snapshots: %+v", got)
	}
	for _, r := range []struct{ method, path, body string }{
		{"DELETE", "/api/v1/vms/p1", ""},
		{"POST", "/api/v1/vms/p1/start", ""},
		{"POST", "/api/v1/vms/p1/stop", `{"force":true}`},
		{"POST", "/api/v1/vms/p1/snapshots", `{"name":"t"}`},
		{"DELETE", "/api/v1/vms/p1/snapshots/s", ""},
		{"POST", "/api/v1/vms/p1/snapshots/s/revert", ""},
	} {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			wantError(t, e.do(t, "pi", r.method, r.path, r.body), http.StatusForbidden, api.CodePermissionDenied)
		})
	}
	if calls := e.Hypervisor.Calls(); !slices.Equal(calls, before) {
		t.Errorf("libvirt was changed: %q", calls[len(before):])
	}
}

func TestStopVM(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "pi", "a")

	rec := e.do(t, "pi", "POST", "/api/v1/vms/a/stop", "")
	if rec.Code != http.StatusAccepted {
		t.Errorf("stop without a body: %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(t, "pi", "POST", "/api/v1/vms/a/stop", `{"force":true}`)
	if rec.Code != http.StatusOK || decode[api.VM](t, rec).State != api.StateShutOff {
		t.Errorf("forced stop: %d %s", rec.Code, rec.Body.String())
	}
	wantError(t, e.do(t, "pi", "POST", "/api/v1/vms/a/stop", `{"forse":true}`), http.StatusBadRequest, api.CodeInvalidArgument)

	rec = e.do(t, "pi", "POST", "/api/v1/vms/a/start", "")
	if rec.Code != http.StatusOK || decode[api.VM](t, rec).State != api.StateRunning {
		t.Errorf("start: %d %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteVM(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "pi", "a")
	rec := e.do(t, "pi", "DELETE", "/api/v1/vms/a", "")
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("delete: %d %q", rec.Code, rec.Body.String())
	}
	wantError(t, e.do(t, "pi", "GET", "/api/v1/vms/a", ""), http.StatusNotFound, api.CodeNotFound)
}

func TestSnapshots(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "pi", "a")
	rec := e.do(t, "pi", "POST", "/api/v1/vms/a/snapshots", `{"name":"s1"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create snapshot: %d %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/api/v1/vms/a/snapshots/s1" {
		t.Errorf("Location = %q", loc)
	}
	wantError(t, e.do(t, "pi", "POST", "/api/v1/vms/a/snapshots", `{"name":"s1"}`), http.StatusConflict, api.CodeAlreadyExists)
	wantError(t, e.do(t, "pi", "POST", "/api/v1/vms/a/snapshots", `{"name":"s2","x":1}`), http.StatusBadRequest, api.CodeInvalidArgument)

	list := decode[api.SnapshotList](t, e.do(t, "pi", "GET", "/api/v1/vms/a/snapshots", ""))
	if len(list.Snapshots) != 1 || list.Snapshots[0].Name != "s1" {
		t.Errorf("snapshots = %+v", list)
	}
	if rec := e.do(t, "pi", "POST", "/api/v1/vms/a/snapshots/s1/revert", ""); rec.Code != http.StatusOK {
		t.Errorf("revert: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, "pi", "DELETE", "/api/v1/vms/a/snapshots/s1", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete snapshot: %d %s", rec.Code, rec.Body.String())
	}
	wantError(t, e.do(t, "pi", "DELETE", "/api/v1/vms/a/snapshots/s1", ""), http.StatusNotFound, api.CodeNotFound)
}

func TestUnknownAPIPathsAreJSON(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.do(t, "pi", "GET", "/api/v1/nope", ""), http.StatusNotFound, api.CodeNotFound)
}

func TestLegacyDomainsAreNotThere(t *testing.T) {
	e := newEnv(t)
	e.Hypervisor.AddDomain("legacy", api.StateRunning)
	if got := decode[api.VMList](t, e.do(t, "admin", "GET", "/api/v1/vms", "")); len(got.VMs) != 0 {
		t.Errorf("admin sees %+v", got)
	}
	wantError(t, e.do(t, "admin", "DELETE", "/api/v1/vms/legacy", ""), http.StatusNotFound, api.CodeNotFound)
	if calls := e.Hypervisor.Calls(); len(calls) != 0 {
		t.Errorf("libvirt was changed: %q", calls)
	}
}

// --- Cross-site requests ---

func TestCrossSitePostsAreRefused(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "pi", "a")

	crossSite := []struct {
		name string
		opts []reqOpt
	}{
		{"Sec-Fetch-Site cross-site", []reqOpt{header("Sec-Fetch-Site", "cross-site")}},
		{"Sec-Fetch-Site same-site", []reqOpt{header("Sec-Fetch-Site", "same-site")}},
		{"Origin of another host", []reqOpt{header("Origin", "https://evil.example")}},
	}
	for _, c := range crossSite {
		t.Run(c.name, func(t *testing.T) {
			for _, path := range []string{"/ui/vms/a/stop", "/ui/vms/a/delete", "/api/v1/vms/a/stop"} {
				rec := e.do(t, "pi", "POST", path, "", c.opts...)
				if rec.Code != http.StatusForbidden {
					t.Errorf("POST %s: %d, want 403", path, rec.Code)
				}
			}
			rec := e.do(t, "pi", "POST", "/api/v1/vms", `{"name":"evil","image":"noble"}`, c.opts...)
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST /api/v1/vms: %d, want 403", rec.Code)
			}
		})
	}
	if calls := e.Hypervisor.Calls(); len(calls) != 2 { // define and start of a
		t.Errorf("libvirt was changed: %q", calls)
	}
}

func TestSameOriginAndNonBrowserPostsPass(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "pi", "a")
	rec := e.do(t, "pi", "POST", "/ui/vms/a/stop", "", header("Sec-Fetch-Site", "same-origin"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("same-origin stop: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec = e.do(t, "pi", "POST", "/ui/vms/a/start", "", header("Origin", "http://example.com"))
	if rec.Code != http.StatusSeeOther {
		t.Errorf("same-host Origin: %d", rec.Code)
	}
	// vmctl and curl send neither header.
	rec = e.do(t, "pi", "POST", "/api/v1/vms/a/stop", `{"force":true}`)
	if rec.Code != http.StatusOK {
		t.Errorf("non-browser stop: %d", rec.Code)
	}
}

// --- The page ---

func TestThePageListsEveryVM(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "pi", "mine")
	e.createVM(t, "penelope", "hers")
	e.Hypervisor.AddDomain("legacy", api.StateRunning)

	rec := e.do(t, "pi", "GET", "/", "")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("page: %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{"mine", "hers", "penelope", "192.168.40.10", "192.168.40.11", "running", `action="/ui/vms/mine/start"`, `action="/ui/vms/mine/stop"`, `action="/ui/vms/mine/delete"`} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	// The buttons are only on the VMs the caller may operate.
	for _, unwanted := range []string{"legacy", "/ui/vms/hers/"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("page shows %q", unwanted)
		}
	}

	body = e.do(t, "admin", "GET", "/", "").Body.String()
	for _, want := range []string{"mine", "hers", "penelope", `action="/ui/vms/hers/delete"`, `action="/ui/vms/mine/delete"`} {
		if !strings.Contains(body, want) {
			t.Errorf("admin's page lacks %q", want)
		}
	}
}

func TestThePageEscapesTheOwner(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "<script>x</script>", "a")
	body := e.do(t, "pi", "GET", "/", "").Body.String()
	if strings.Contains(body, "<script>x</script>") {
		t.Error("the owner is not escaped")
	}
}

func TestThePageButtons(t *testing.T) {
	e := newEnv(t)
	e.createVM(t, "pi", "a")
	same := header("Sec-Fetch-Site", "same-origin")

	if rec := e.do(t, "pi", "POST", "/ui/vms/a/stop", "", same); rec.Code != http.StatusSeeOther {
		t.Errorf("stop: %d", rec.Code)
	}
	if v := decode[api.VM](t, e.do(t, "pi", "GET", "/api/v1/vms/a", "")); v.State != api.StateShuttingDown {
		t.Errorf("after stop: %s", v.State)
	}
	// Another owner's VM: refused, shown as a page.
	rec := e.do(t, "penelope", "POST", "/ui/vms/a/delete", "", same)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "only its owner") {
		t.Errorf("penelope deleting a: %d %s", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, "pi", "POST", "/ui/vms/a/delete", "", same); rec.Code != http.StatusSeeOther {
		t.Errorf("delete: %d", rec.Code)
	}
	wantError(t, e.do(t, "pi", "GET", "/api/v1/vms/a", ""), http.StatusNotFound, api.CodeNotFound)
	if rec := e.do(t, "pi", "POST", "/ui/vms/a/reboot", "", same); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: %d", rec.Code)
	}
}
