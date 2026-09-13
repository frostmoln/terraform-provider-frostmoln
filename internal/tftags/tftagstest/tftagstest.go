// Package tftagstest is the test kit for default_tags and tags_all: a fake
// backend that applies tags the way the Frostmoln services do, the per-resource
// wire profile it needs, and tftypes helpers. It is imported only by tests —
// the provider-wide gate (internal/provider/default_tags_contract_test.go) and
// the per-resource default_tags tests.
package tftagstest

import (
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// ObjectID is the id the fake gives the one object it holds.
const ObjectID = "obj-1"

// operationID is the operation an async write answers with; it completes on
// the first poll.
const operationID = "op-1"

// Profile is the handful of wire facts the fake needs to stand in for the
// service behind one taggable resource.
type Profile struct {
	Create      string // tenant-relative POST path
	Member      string // tenant-relative member path (read and update)
	IDField     string // the JSON field carrying the object's identity (default "id")
	CreateField string // the tag field on create (default "tags")
	UpdateField string // the tag field on update (default "tags")
	ReadField   string // the tag field on read (default "tags")
	Update      string // the update verb
	// ClearFlag names the flag an update sends INSTEAD of an empty tag map (the
	// lb pool and health monitor writes cross gRPC, where {} is lost).
	ClearFlag string
	ImportID  string // default ObjectID
	// ImportMember is the path the Read after an import reaches, when it is
	// not Member (see frostmoln_snapshot).
	ImportMember string
	// Reserved are platform-owned keys the backend keeps on the object and
	// returns on every read; the provider must keep them out of tags_all.
	Reserved map[string]string
	// Config are attribute values a create configuration needs beyond the
	// schema-derived placeholders.
	Config map[string]tftypes.Value
	// Extra are response fields a read-back needs.
	Extra map[string]any
	// Async: the real create answers 202 with an operation (provisioning), so
	// the fake does too, and the resource's operation-polling path runs.
	Async bool
	// AsyncUpdate: the same for the update.
	AsyncUpdate bool
	// Touch is an in-place change to one attribute that is not about tags —
	// a string, int or bool value, or for the `timeouts` block the `update`
	// budget — used to drive an update for another reason.
	Touch map[string]any
}

// Field returns the tag field for a verb, defaulting to "tags".
func (p Profile) Field(v string) string {
	if v == "" {
		return "tags"
	}
	return v
}

var (
	networkReserved = map[string]string{"frostmoln_managed_by": "cluster"}
	storageReserved = map[string]string{"customer-id": "t-1"}
)

// Str is a known string value.
func Str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

// Profiles is every taggable resource's wire profile. The provider gate fails
// for a resource with `tags` and no entry here, so a new taggable resource
// cannot ship without running the default_tags matrix.
var Profiles = map[string]Profile{
	"frostmoln_bucket": {
		Create: "/buckets", Member: "/buckets/" + ObjectID, IDField: "name", Update: http.MethodPut,
		Config: map[string]tftypes.Value{"name": Str(ObjectID)},
		Touch:  map[string]any{"versioning": "suspended"},
	},
	"frostmoln_dns_zone": {
		Create: "/dns/zones", Member: "/dns/zones/" + ObjectID, Update: http.MethodPut,
		Touch: map[string]any{"description": "touched"},
	},
	"frostmoln_instance": {
		Create: "/instances", Member: "/instances/" + ObjectID, Update: http.MethodPatch,
		CreateField: "tags", UpdateField: "metadata", ReadField: "metadata",
		Reserved: map[string]string{"frostmoln_type": "customer"},
		Async:    true,
		Touch:    map[string]any{"name": "touched"},
	},
	"frostmoln_launch_template": {
		Create: "/launch-templates", Member: "/launch-templates/" + ObjectID, Update: http.MethodPatch,
		Touch: map[string]any{"name": "touched"},
	},
	"frostmoln_lb_health_monitor": {
		Create:    "/load-balancers/lb-1/pools/pool-1/healthmonitor",
		Member:    "/load-balancers/lb-1/pools/pool-1/healthmonitor",
		Update:    http.MethodPut,
		ClearFlag: "clearTags",
		ImportID:  "lb-1/pool-1",
		Reserved:  networkReserved,
		Config:    map[string]tftypes.Value{"load_balancer_id": Str("lb-1"), "pool_id": Str("pool-1")},
		Extra:     map[string]any{"poolId": "pool-1"},
		Async:     true, AsyncUpdate: true,
		Touch: map[string]any{"delay": 7},
	},
	"frostmoln_lb_pool": {
		Create: "/load-balancers/lb-1/pools", Member: "/load-balancers/lb-1/pools/" + ObjectID, Update: http.MethodPut,
		ClearFlag: "clearTags", ImportID: "lb-1/" + ObjectID, Reserved: networkReserved,
		Config: map[string]tftypes.Value{"load_balancer_id": Str("lb-1")},
		// proxyProtocol: the schema defaults it; a read-back without it would
		// plan an update on every run.
		Extra: map[string]any{"loadBalancerId": "lb-1", "proxyProtocol": "none"},
		Async: true, AsyncUpdate: true,
		Touch: map[string]any{"name": "touched"},
	},
	"frostmoln_load_balancer": {
		Create: "/load-balancers", Member: "/load-balancers/" + ObjectID, Update: http.MethodPut, Reserved: networkReserved,
		Async: true, Touch: map[string]any{"description": "touched"},
	},
	"frostmoln_public_ip": {
		Create: "/public-ips", Member: "/public-ips/" + ObjectID, Update: http.MethodPut, Reserved: networkReserved,
		Async: true, Touch: map[string]any{"acknowledge_address_loss": true},
	},
	"frostmoln_scale_group": {
		Create: "/scale-groups", Member: "/scale-groups/" + ObjectID, Update: http.MethodPatch,
		// The schema defaults these two; a read-back without them would plan an
		// update on every run.
		// The schema defaults these; a read-back without them would plan an
		// update on every run (warmup non-zero: the read-back cannot tell 0 from
		// absent).
		Extra: map[string]any{
			"healthCheckType": "instance", "terminationPolicy": "oldest_first",
			"healthCheckGracePeriod": 300, "cooldownSeconds": 300, "warmupSeconds": 30,
		},
		Async: true,
		Touch: map[string]any{"name": "touched"},
	},
	"frostmoln_secret": {
		Create: "/secrets", Member: "/secrets/" + ObjectID, Update: http.MethodPut,
		Config: map[string]tftypes.Value{"secret_value": Str("s3cr3t")}, // pragma: allowlist secret
		Touch:  map[string]any{"description": "touched"},
	},
	"frostmoln_security_group": {
		Create: "/security-groups", Member: "/security-groups/" + ObjectID, Update: http.MethodPut, Reserved: networkReserved,
		Async: true, Touch: map[string]any{"description": "touched"},
	},
	"frostmoln_snapshot": {
		Create: "/volumes/vol-1/snapshots", Member: "/volumes/vol-1/snapshots/" + ObjectID, Update: http.MethodPut,
		CreateField: "metadata", UpdateField: "metadata", ReadField: "metadata",
		Reserved: storageReserved,
		Config:   map[string]tftypes.Value{"volume_id": Str("vol-1")},
		Extra:    map[string]any{"volumeId": "vol-1", "status": "available"},
		// A snapshot imported by its bare id has no volume_id yet, so its Read
		// asks for /volumes//snapshots/{id}, which the client collapses to
		// this — a path no storage route serves. That is a defect of the
		// snapshot import itself (it predates default_tags and is tracked
		// separately); the fake answers it so this matrix still exercises the
		// import's tag handling.
		ImportMember: "/volumes/snapshots/" + ObjectID,
		Async:        true,
		// Everything else on a snapshot replaces it; its timeouts change in place.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_subnet": {
		Create: "/subnets", Member: "/subnets/" + ObjectID, Update: http.MethodPut, Reserved: networkReserved,
		Async: true, Touch: map[string]any{"description": "touched"},
	},
	"frostmoln_volume": {
		Create: "/volumes", Member: "/volumes/" + ObjectID, Update: http.MethodPatch,
		CreateField: "metadata", UpdateField: "metadata", ReadField: "metadata",
		Reserved: storageReserved,
		Async:    true,
		Touch:    map[string]any{"description": "touched"},
	},
	"frostmoln_vpc": {
		Create: "/vpcs", Member: "/vpcs/" + ObjectID, Update: http.MethodPut, Reserved: networkReserved,
		Async: true, Touch: map[string]any{"description": "touched"},
	},
}

// Write is one write the fake received.
type Write struct {
	Method string
	Path   string
	Body   map[string]json.RawMessage
}

// Fake stands in for the service behind one resource: one object, whose user
// tags are replaced whenever an update carries the tag field (or cleared by the
// clear flag), whose platform-owned keys survive every write and come back on
// every read, and which stamps Stamp (a tenant default) at create. It also
// answers GET /v1/me for a provider configure, as tenant t-1.
type Fake struct {
	Profile Profile
	// Stamp is merged into the object's tags at create, as the platform stamps
	// tenant default tags.
	Stamp map[string]string

	mu     sync.Mutex
	tags   map[string]string
	writes []Write
	polls  int
}

// OperationPolls counts the reads of the async writes' operation.
func (f *Fake) OperationPolls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.polls
}

// NewFake returns a fake for the given profile.
func NewFake(p Profile) *Fake {
	return &Fake{Profile: p, tags: map[string]string{}}
}

// Set puts a tag on the object as a change made outside Terraform would.
func (f *Fake) Set(k, v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tags[k] = v
}

// Delete removes a tag from the object as a change made outside Terraform
// would.
func (f *Fake) Delete(k string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.tags, k)
}

// UserTags returns the object's non-platform tags.
func (f *Fake) UserTags() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Merge(f.tags, nil)
}

// LastWrite returns the last write with the given method, or nil.
func (f *Fake) LastWrite(method string) *Write {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.writes) - 1; i >= 0; i-- {
		if f.writes[i].Method == method {
			w := f.writes[i]
			return &w
		}
	}
	return nil
}

// Object is the object's read-back body.
func (f *Fake) Object() map[string]any {
	obj := map[string]any{}
	for k, v := range f.Profile.Extra {
		obj[k] = v
	}
	idField := f.Profile.IDField
	if idField == "" {
		idField = "id"
	}
	obj[idField] = ObjectID
	obj["createdAt"] = "2026-09-13T00:00:00Z"
	obj[f.Profile.Field(f.Profile.ReadField)] = Merge(f.tags, f.Profile.Reserved)
	return obj
}

func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const tenant = "/v1/tenants/t-1"
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")

	if r.URL.Path == "/v1/me" {
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "u-1", "tenantId": "t-1"})
		return
	}
	body := map[string]json.RawMessage{}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	isMember := r.URL.Path == tenant+f.Profile.Member ||
		(f.Profile.ImportMember != "" && r.URL.Path == tenant+f.Profile.ImportMember)

	switch {
	case r.Method == http.MethodPost && r.URL.Path == tenant+f.Profile.Create:
		f.writes = append(f.writes, Write{r.Method, r.URL.Path, body})
		f.tags = map[string]string{}
		if raw, ok := body[f.Profile.Field(f.Profile.CreateField)]; ok {
			_ = json.Unmarshal(raw, &f.tags)
		}
		for k, v := range f.Stamp {
			f.tags[k] = v
		}
		if f.Profile.Async {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operationId": operationID, "status": "pending"})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(f.Object())
	case r.Method == http.MethodGet && r.URL.Path == tenant+"/operations/"+operationID:
		f.polls++
		_ = json.NewEncoder(w).Encode(map[string]any{
			"operationId": operationID, "status": "completed", "resourceId": ObjectID,
		})
	case r.Method == http.MethodGet && isMember:
		_ = json.NewEncoder(w).Encode(f.Object())
	case r.Method == f.Profile.Update && isMember:
		f.writes = append(f.writes, Write{r.Method, r.URL.Path, body})
		if raw, ok := body[f.Profile.Field(f.Profile.UpdateField)]; ok && string(raw) != "null" {
			next := map[string]string{}
			_ = json.Unmarshal(raw, &next)
			f.tags = next
		}
		if f.Profile.ClearFlag != "" && string(body[f.Profile.ClearFlag]) == "true" {
			f.tags = map[string]string{}
		}
		if f.Profile.AsyncUpdate {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"operationId": operationID, "status": "pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(f.Object())
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"code": "NOT_FOUND", "message": r.Method + " " + r.URL.Path})
	}
}

// SentTags decodes the tag map a recorded write carried in field; ok is false
// when the field was absent.
func (w *Write) SentTags(t *testing.T, field string) (map[string]string, bool) {
	t.Helper()
	raw, ok := w.Body[field]
	if !ok {
		return nil, false
	}
	got := map[string]string{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("%q is not a tag map: %s", field, raw)
	}
	return got, true
}

// --- values ---

// ObjectType is the schema's object type.
func ObjectType(t *testing.T, s schema.Schema) tftypes.Object {
	t.Helper()
	ot, ok := s.Type().TerraformType(t.Context()).(tftypes.Object)
	if !ok {
		t.Fatal("schema type is not an object")
	}
	return ot
}

// Object is an object of the schema's type with every attribute null except
// the overrides.
func Object(t *testing.T, s schema.Schema, overrides map[string]tftypes.Value) tftypes.Value {
	t.Helper()
	ot := ObjectType(t, s)
	vals := map[string]tftypes.Value{}
	for name, at := range ot.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	for name, v := range overrides {
		if _, ok := ot.AttributeTypes[name]; !ok {
			t.Fatalf("%q is not an attribute", name)
		}
		vals[name] = v
	}
	return tftypes.NewValue(ot, vals)
}

// CreateConfig is a configuration with every Required attribute given a
// placeholder, the profile's values on top, and the given tags (nil: null).
func CreateConfig(t *testing.T, s schema.Schema, p Profile, tags map[string]string) tftypes.Value {
	t.Helper()
	ot := ObjectType(t, s)
	vals := map[string]tftypes.Value{}
	for name, a := range s.Attributes {
		if a.IsRequired() {
			vals[name] = placeholder(ot.AttributeTypes[name])
		}
	}
	for name, v := range p.Config {
		vals[name] = v
	}
	vals["tags"] = Tags(tags)
	return Object(t, s, vals)
}

// Tags is a map(string) value; nil is null.
func Tags(m map[string]string) tftypes.Value {
	mt := tftypes.Map{ElementType: tftypes.String}
	if m == nil {
		return tftypes.NewValue(mt, nil)
	}
	elems := map[string]tftypes.Value{}
	for k, v := range m {
		elems[k] = tftypes.NewValue(tftypes.String, v)
	}
	return tftypes.NewValue(mt, elems)
}

// Attr returns one attribute of an object value.
func Attr(t *testing.T, obj tftypes.Value, name string) tftypes.Value {
	t.Helper()
	var attrs map[string]tftypes.Value
	if err := obj.As(&attrs); err != nil {
		t.Fatalf("not an object: %v", err)
	}
	return attrs[name]
}

// MapOf renders a map(string) value; nil for null.
func MapOf(t *testing.T, v tftypes.Value) map[string]string {
	t.Helper()
	if v.IsNull() {
		return nil
	}
	var elems map[string]tftypes.Value
	if err := v.As(&elems); err != nil {
		t.Fatalf("not a map: %v", err)
	}
	out := map[string]string{}
	for k, e := range elems {
		var s string
		if err := e.As(&s); err != nil {
			t.Fatalf("map element %q: %v", k, err)
		}
		out[k] = s
	}
	return out
}

// Merge returns a ∪ b (b wins).
func Merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// Equal reports whether two maps hold the same pairs; nil and empty differ.
func Equal(a, b map[string]string) bool {
	if (a == nil) != (b == nil) || len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// placeholder is a value of the given type for a Required attribute the
// profile does not name.
func placeholder(t tftypes.Type) tftypes.Value {
	switch {
	case t.Is(tftypes.String):
		return tftypes.NewValue(t, "x")
	case t.Is(tftypes.Number):
		return tftypes.NewValue(t, 1)
	case t.Is(tftypes.Bool):
		return tftypes.NewValue(t, false)
	case t.Is(tftypes.List{}), t.Is(tftypes.Set{}):
		return tftypes.NewValue(t, []tftypes.Value{})
	case t.Is(tftypes.Map{}):
		return tftypes.NewValue(t, map[string]tftypes.Value{})
	}
	return tftypes.NewValue(t, nil)
}
