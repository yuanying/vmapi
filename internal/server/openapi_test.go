package server_test

// These tests read api/openapi.yaml and hold the implementation to it: the
// same operations, the same fields, and only documented statuses.

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/yuanying/vmapi/internal/api"
	"github.com/yuanying/vmapi/internal/server"
)

type spec struct {
	Paths      map[string]map[string]any `yaml:"paths"`
	Components struct {
		Schemas map[string]schema `yaml:"schemas"`
	} `yaml:"components"`
}

type schema struct {
	Type       string            `yaml:"type"`
	Required   []string          `yaml:"required"`
	Properties map[string]schema `yaml:"properties"`
}

type operation struct {
	id, method, path string
	statuses         []string
}

func loadSpec(t *testing.T) *spec {
	t.Helper()
	data, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var s spec
	if err := yaml.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func (s *spec) operations() []operation {
	var out []operation
	for path, item := range s.Paths {
		for method, v := range item {
			op, ok := v.(map[string]any)
			if !ok || method == "parameters" {
				continue
			}
			o := operation{method: strings.ToUpper(method), path: path}
			o.id, _ = op["operationId"].(string)
			if responses, ok := op["responses"].(map[string]any); ok {
				o.statuses = slices.Sorted(maps.Keys(responses))
			}
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func (s *spec) operation(t *testing.T, id string) operation {
	t.Helper()
	for _, o := range s.operations() {
		if o.id == id {
			return o
		}
	}
	t.Fatalf("no operation %s in the spec", id)
	return operation{}
}

func TestTheRoutesAreTheSpecsOperations(t *testing.T) {
	var fromSpec []string
	for _, o := range loadSpec(t).operations() {
		fromSpec = append(fromSpec, o.method+" "+o.path)
	}
	fromServer := server.APIRoutes()
	slices.Sort(fromSpec)
	slices.Sort(fromServer)
	if !slices.Equal(fromSpec, fromServer) {
		t.Errorf("routes differ\n spec:   %q\n server: %q", fromSpec, fromServer)
	}
}

// jsonFields are the JSON names of a struct's fields, and those that are
// always sent (no omitempty).
func jsonFields(typ reflect.Type) (all, always []string) {
	for f := range typ.Fields() {
		tag := f.Tag.Get("json")
		name, opts, _ := strings.Cut(tag, ",")
		all = append(all, name)
		if !strings.Contains(opts, "omitempty") {
			always = append(always, name)
		}
	}
	slices.Sort(all)
	slices.Sort(always)
	return all, always
}

func TestTheTypesAreTheSpecsSchemas(t *testing.T) {
	s := loadSpec(t)
	tests := []struct {
		schema string
		typ    any
		// response: every required field is always sent. Requests have
		// pointers for optional fields instead.
		response bool
	}{
		{"Whoami", api.Whoami{}, true},
		{"Image", api.Image{}, true},
		{"ImageList", api.ImageList{}, true},
		{"VM", api.VM{}, true},
		{"VMList", api.VMList{}, true},
		{"Snapshot", api.Snapshot{}, true},
		{"SnapshotList", api.SnapshotList{}, true},
		{"Error", api.ErrorBody{}, true},
		{"CreateVMRequest", api.CreateVMRequest{}, false},
		{"StopVMRequest", api.StopVMRequest{}, false},
		{"CreateSnapshotRequest", api.CreateSnapshotRequest{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			sc, ok := s.Components.Schemas[tt.schema]
			if !ok {
				t.Fatalf("no schema %s", tt.schema)
			}
			all, always := jsonFields(reflect.TypeOf(tt.typ))
			props := slices.Sorted(maps.Keys(sc.Properties))
			if !slices.Equal(all, props) {
				t.Errorf("fields %q, schema properties %q", all, props)
			}
			if tt.response {
				for _, r := range sc.Required {
					if !slices.Contains(always, r) {
						t.Errorf("required %q may be left out", r)
					}
				}
			}
		})
	}
	// The error object inside Error.
	inner := s.Components.Schemas["Error"].Properties["error"]
	all, _ := jsonFields(reflect.TypeOf(api.Error{}))
	if props := slices.Sorted(maps.Keys(inner.Properties)); !slices.Equal(all, props) {
		t.Errorf("api.Error fields %q, schema %q", all, props)
	}
}

// TestEveryStatusIsDocumented walks every operation, its successes and
// some of its failures, and checks that each status sent is one the spec
// lists for that operation, and that the body has the required fields.
func TestEveryStatusIsDocumented(t *testing.T) {
	s := loadSpec(t)
	e := newEnv(t)
	steps := []struct {
		op, user, method, path, body string
		schema                       string
	}{
		{"getWhoami", "pi", "GET", "/api/v1/whoami", "", "Whoami"},
		{"getWhoami", "", "GET", "/api/v1/whoami", "", "Error"},
		{"listImages", "pi", "GET", "/api/v1/images", "", "ImageList"},
		{"createVM", "pi", "POST", "/api/v1/vms", `{"name":"a","image":"noble"}`, "VM"},
		{"createVM", "pi", "POST", "/api/v1/vms", `{"name":"a","image":"noble"}`, "Error"},
		{"createVM", "pi", "POST", "/api/v1/vms", `{"name":"b","image":"nope"}`, "Error"},
		{"createVM", "pi", "POST", "/api/v1/vms", `{"name":"b"`, "Error"},
		{"listVMs", "pi", "GET", "/api/v1/vms", "", "VMList"},
		{"listVMs", "pi", "GET", "/api/v1/vms?owner=penelope", "", "Error"},
		{"getVM", "pi", "GET", "/api/v1/vms/a", "", "VM"},
		{"getVM", "pi", "GET", "/api/v1/vms/zz", "", "Error"},
		{"startVM", "pi", "POST", "/api/v1/vms/a/start", "", "VM"},
		{"stopVM", "pi", "POST", "/api/v1/vms/a/stop", "", "VM"},
		{"stopVM", "pi", "POST", "/api/v1/vms/a/stop", `{"force":true}`, "VM"},
		{"stopVM", "pi", "POST", "/api/v1/vms/a/stop", `{"x":1}`, "Error"},
		{"createSnapshot", "pi", "POST", "/api/v1/vms/a/snapshots", `{"name":"s"}`, "Snapshot"},
		{"createSnapshot", "pi", "POST", "/api/v1/vms/a/snapshots", `{"name":"s"}`, "Error"},
		{"createSnapshot", "pi", "POST", "/api/v1/vms/a/snapshots", `{"name":"S"}`, "Error"},
		{"listSnapshots", "pi", "GET", "/api/v1/vms/a/snapshots", "", "SnapshotList"},
		{"revertSnapshot", "pi", "POST", "/api/v1/vms/a/snapshots/s/revert", "", "VM"},
		{"revertSnapshot", "pi", "POST", "/api/v1/vms/a/snapshots/zz/revert", "", "Error"},
		{"deleteSnapshot", "pi", "DELETE", "/api/v1/vms/a/snapshots/s", "", ""},
		{"deleteSnapshot", "pi", "DELETE", "/api/v1/vms/a/snapshots/s", "", "Error"},
		{"deleteVM", "pi", "DELETE", "/api/v1/vms/a", "", ""},
		{"deleteVM", "pi", "DELETE", "/api/v1/vms/a", "", "Error"},
	}
	seen := map[string]bool{}
	for _, st := range steps {
		op := s.operation(t, st.op)
		seen[st.op] = true
		rec := e.do(t, st.user, st.method, st.path, st.body)
		status := strconv.Itoa(rec.Code)
		if !slices.Contains(op.statuses, status) {
			t.Errorf("%s %s answered %s, the spec lists %q", st.method, st.path, status, op.statuses)
		}
		if st.schema == "" {
			if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
				t.Errorf("%s %s: %d %q, want an empty 204", st.method, st.path, rec.Code, rec.Body.String())
			}
			continue
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s %s: %v", st.method, st.path, err)
			continue
		}
		for _, r := range s.Components.Schemas[st.schema].Required {
			if _, ok := body[r]; !ok {
				t.Errorf("%s %s (%d): %s lacks %q: %s", st.method, st.path, rec.Code, st.schema, r, rec.Body.String())
			}
		}
	}
	for _, o := range s.operations() {
		if !seen[o.id] {
			t.Errorf("operation %s is not walked", o.id)
		}
	}
}
