package container_registry_cache

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// tagVal is a map(string) value: nil is null, a nil-free map known, and
// unknown when unknown is set.
func tagVal(m map[string]string, unknown bool) tftypes.Value {
	mt := tftypes.Map{ElementType: tftypes.String}
	if unknown {
		return tftypes.NewValue(mt, tftypes.UnknownValue)
	}
	if m == nil {
		return tftypes.NewValue(mt, nil)
	}
	elems := map[string]tftypes.Value{}
	for k, v := range m {
		elems[k] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(mt, elems)
}

// withAttrs returns a copy of obj with the given attributes replaced.
func withAttrs(t *testing.T, obj tftypes.Value, set map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	var attrs map[string]tftypes.Value
	if err := obj.As(&attrs); err != nil {
		t.Fatalf("not an object: %v", err)
	}
	out := make(map[string]tftypes.Value, len(attrs))
	for k, v := range attrs {
		out[k] = v
	}
	for k, v := range set {
		out[k] = v
	}
	return tftypes.NewValue(obj.Type(), out)
}

// mapAttr renders one map(string) attribute of an object value; nil for null.
func mapAttr(t *testing.T, obj tftypes.Value, name string) map[string]string {
	t.Helper()
	var attrs map[string]tftypes.Value
	if err := obj.As(&attrs); err != nil {
		t.Fatalf("not an object: %v", err)
	}
	v := attrs[name]
	if v.IsNull() {
		return nil
	}
	var elems map[string]tftypes.Value
	if err := v.As(&elems); err != nil {
		t.Fatalf("%s is not a map: %v", name, err)
	}
	out := map[string]string{}
	for k, e := range elems {
		var s string
		_ = e.As(&s)
		out[k] = s
	}
	return out
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// recorder records every request the handler sees: method, path and query, and
// the last body per method.
type recorder struct {
	mu       sync.Mutex
	requests []string
	bodies   map[string]map[string]json.RawMessage
}

func (r *recorder) record(req *http.Request) map[string]json.RawMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	line := req.Method + " " + req.URL.Path
	if req.URL.RawQuery != "" {
		line += "?" + req.URL.RawQuery
	}
	r.requests = append(r.requests, line)
	raw, _ := io.ReadAll(req.Body)
	body := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &body)
	if r.bodies == nil {
		r.bodies = map[string]map[string]json.RawMessage{}
	}
	r.bodies[req.Method] = body
	return body
}

func sentTags(t *testing.T, body map[string]json.RawMessage) map[string]string {
	t.Helper()
	raw, ok := body["tags"]
	if !ok {
		t.Fatalf("the request carried no tags: %v", body)
	}
	out := map[string]string{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("tags = %s: %v", raw, err)
	}
	return out
}
