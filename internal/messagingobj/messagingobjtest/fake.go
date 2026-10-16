// Package messagingobjtest is an in-memory fake of the messaging service's
// vhost/user/permission surface, for the resource and data-source unit tests.
// It models the contract the resources depend on: a mutation answers 202 with
// the object `pending` (or `deleting`), the verdict lands after a configurable
// number of GETs, a failed change of a held object goes back to `ready` WITH a
// failureReason, a failed create lands in `error`, and a local delete answers
// 204. It also measures how many operations were open on one instance at once,
// which is what the per-instance lock exists to keep at one.
package messagingobjtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/messagingobj"
)

// TenantID is the tenant the fake's /v1/me answers.
const TenantID = "tenant-1"

// Outcome is the verdict an operation lands with. The zero value is success.
type Outcome struct {
	// Status, when set, is the status the object lands in instead of the
	// success status (`ready`, or gone for a delete).
	Status  string
	Reason  string
	Message string
}

// Refusal is an HTTP error answered instead of handling a request.
type Refusal struct {
	Status  int
	Code    string
	Message string
}

type object struct {
	kind      string // "vhost", "user", "permission"
	instance  string
	name      string // vhost/user name; for a permission, "<user>/<vhost>"
	user      string
	vhost     string
	status    string
	reason    string
	message   string
	configure string
	write     string
	read      string
	createdAt string
	updatedAt string

	// The open operation, if any.
	op          string // "create", "replace", "delete"
	pollsLeft   int
	outcome     Outcome
	reqConf     string
	reqWrite    string
	reqRead     string
	brokerHolds bool
}

// Fake is the backend. Configure its fields before the first request.
type Fake struct {
	// PollsUntilDone is how many GETs of an object with an open operation pass
	// before its verdict lands (default 1: the first poll after the 202 sees it).
	PollsUntilDone int
	// Outcomes, keyed "<op> <kind> <name>" (e.g. "replace permission app/orders"),
	// decide an operation's verdict; absent means success.
	Outcomes map[string]Outcome
	// Refuse, keyed "<METHOD> <path suffix after /messaging/{id}>" (e.g.
	// "POST /vhosts"), answers that refusal instead.
	Refuse map[string]Refusal
	// LocalDelete makes every delete answer 204 (the broker never held it).
	LocalDelete bool
	// Delay holds every object request this long before it is handled, to
	// widen any overlap between operations.
	Delay time.Duration

	// CredFailures answers the first N credentials reads of a ready user 503.
	CredFailures int

	mu        sync.Mutex
	objects   map[string]*object
	open      map[string]int
	maxOpen   map[string]int
	Requests  []string
	CredReads int
	// Languages is the Accept-Language of every object request.
	Languages []string
}

// NewServer starts the fake. Close it with t.Cleanup or defer.
func NewServer(f *Fake) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(f.serve))
}

// key is EXACT, like the service's lookups (`name = $2`): `Orders` and
// `orders` are different objects. Only the create's uniqueness check folds
// case (taken).
func key(kind, instance, name string) string {
	return kind + "|" + instance + "|" + name
}

// taken mirrors the service's case-insensitive unique index.
func (f *Fake) taken(kind, instance, name string) bool {
	for _, o := range f.objects {
		if o.kind == kind && o.instance == instance && strings.EqualFold(o.name, name) {
			return true
		}
	}
	return false
}

// Seed adds an object in a given state (no open operation).
func (f *Fake) Seed(kind, instance, name, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	o := &object{
		kind: kind, instance: instance, name: name, status: status, brokerHolds: status == messagingobj.StatusReady,
		createdAt: "2026-10-16T10:00:00Z", updatedAt: "2026-10-16T10:00:00Z",
	}
	if kind == "permission" {
		parts := strings.SplitN(name, "/", 2)
		o.user, o.vhost = parts[0], parts[1]
		o.configure, o.write, o.read = ".*", ".*", ".*"
	}
	f.objects[key(kind, instance, name)] = o
}

// Has reports whether the object exists.
func (f *Fake) Has(kind, instance, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	_, ok := f.objects[key(kind, instance, name)]
	return ok
}

// MaxOpen is the largest number of operations ever open at once on instance.
func (f *Fake) MaxOpen(instance string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	return f.maxOpen[strings.ToLower(instance)]
}

func (f *Fake) init() {
	if f.objects == nil {
		f.objects = map[string]*object{}
		f.open = map[string]int{}
		f.maxOpen = map[string]int{}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, status int, code, message string) {
	// servicekit's FLAT envelope: what makes a 404 a service verdict.
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/me" {
		writeJSON(w, http.StatusOK, map[string]string{"id": "user-1", "tenantId": TenantID})
		return
	}
	prefix := "/v1/tenants/" + TenantID + "/messaging/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		refuse(w, http.StatusNotFound, "NOT_FOUND", "unknown path")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	slash := strings.Index(rest, "/")
	if slash < 0 {
		refuse(w, http.StatusNotFound, "NOT_FOUND", "unknown path")
		return
	}
	instance, sub := rest[:slash], rest[slash:]
	segs := strings.Split(strings.Trim(sub, "/"), "/")

	if f.Delay > 0 {
		time.Sleep(f.Delay)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	f.Requests = append(f.Requests, r.Method+" "+sub)
	f.Languages = append(f.Languages, r.Header.Get("Accept-Language"))
	if rf, ok := f.Refuse[r.Method+" "+sub]; ok {
		refuse(w, rf.Status, rf.Code, rf.Message)
		return
	}

	switch {
	case len(segs) == 1 && (segs[0] == "vhosts" || segs[0] == "users"):
		kind := strings.TrimSuffix(segs[0], "s")
		if r.Method == http.MethodPost {
			var body messagingobj.CreateRequest
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.create(w, kind, instance, body.Name)
			return
		}
		refuse(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	case len(segs) == 2 && (segs[0] == "vhosts" || segs[0] == "users"):
		kind := strings.TrimSuffix(segs[0], "s")
		f.member(w, r, kind, instance, segs[1])
	case len(segs) == 3 && segs[0] == "users" && segs[2] == "credentials":
		o, ok := f.objects[key("user", instance, segs[1])]
		if !ok {
			refuse(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		if o.status != messagingobj.StatusReady {
			refuse(w, http.StatusConflict, messagingobj.CodeObjectNotReady, "The user is not ready.")
			return
		}
		f.CredReads++
		if f.CredFailures > 0 {
			f.CredFailures--
			refuse(w, http.StatusServiceUnavailable, "unavailable", "try again")
			return
		}
		writeJSON(w, http.StatusOK, messagingobj.Credentials{
			Host: "10.0.0.5", AMQPPort: 5672, AMQPSPort: 5671, Username: o.name, Password: "pw-" + o.name,
		})
	case len(segs) == 3 && segs[2] == "permissions":
		list := messagingobj.PermissionList{Permissions: []messagingobj.Permission{}}
		for _, o := range f.objects {
			if o.kind != "permission" || o.instance != instance {
				continue
			}
			if (segs[0] == "users" && o.user == segs[1]) ||
				(segs[0] == "vhosts" && o.vhost == segs[1]) {
				list.Permissions = append(list.Permissions, *f.permission(o))
			}
		}
		writeJSON(w, http.StatusOK, list)
	case len(segs) == 4 && segs[0] == "users" && segs[2] == "permissions":
		f.grant(w, r, instance, segs[1], segs[3])
	default:
		refuse(w, http.StatusNotFound, "NOT_FOUND", "unknown path")
	}
}

func (f *Fake) begin(o *object, op string) {
	o.op = op
	o.pollsLeft = f.PollsUntilDone
	if o.pollsLeft <= 0 {
		o.pollsLeft = 1
	}
	o.outcome = f.Outcomes[op+" "+o.kind+" "+o.name]
	inst := strings.ToLower(o.instance)
	f.open[inst]++
	if f.open[inst] > f.maxOpen[inst] {
		f.maxOpen[inst] = f.open[inst]
	}
}

// advance moves an open operation one poll closer to its verdict; it returns
// false when the object is gone.
func (f *Fake) advance(k string, o *object) bool {
	if o.op == "" {
		return true
	}
	o.pollsLeft--
	if o.pollsLeft > 0 {
		return true
	}
	op, out := o.op, o.outcome
	o.op = ""
	f.open[strings.ToLower(o.instance)]--
	o.updatedAt = "2026-10-16T10:05:00Z"
	switch {
	case out.Status == "" && op == "delete":
		delete(f.objects, k)
		return false
	case out.Status == "":
		o.status, o.reason, o.message = messagingobj.StatusReady, "", ""
		o.brokerHolds = true
		if o.kind == "permission" {
			o.configure, o.write, o.read = o.reqConf, o.reqWrite, o.reqRead
		}
	default:
		o.status, o.reason, o.message = out.Status, out.Reason, out.Message
	}
	return true
}

func (f *Fake) create(w http.ResponseWriter, kind, instance, name string) {
	k := key(kind, instance, name)
	if f.taken(kind, instance, name) {
		refuse(w, http.StatusConflict, messagingobj.CodeObjectExists, "a "+kind+" with this name already exists on the instance")
		return
	}
	o := &object{
		kind: kind, instance: instance, name: name, status: messagingobj.StatusPending,
		createdAt: "2026-10-16T10:00:00Z", updatedAt: "2026-10-16T10:00:00Z",
	}
	f.objects[k] = o
	f.begin(o, "create")
	writeJSON(w, http.StatusAccepted, f.plain(o))
}

func (f *Fake) plain(o *object) *messagingobj.Object {
	return &messagingobj.Object{
		Name: o.name, Status: o.status, FailureReason: o.reason, FailureMessage: o.message,
		OperationID: "op-1", CreatedAt: o.createdAt, UpdatedAt: o.updatedAt,
	}
}

func (f *Fake) permission(o *object) *messagingobj.Permission {
	return &messagingobj.Permission{
		User: o.user, Vhost: o.vhost, Configure: o.configure, Write: o.write, Read: o.read,
		Status: o.status, FailureReason: o.reason, FailureMessage: o.message, OperationID: "op-1",
		CreatedAt: o.createdAt, UpdatedAt: o.updatedAt,
	}
}

func (f *Fake) remove(w http.ResponseWriter, k string, o *object) {
	if o.op != "" {
		refuse(w, http.StatusConflict, messagingobj.CodeOperationInProgress, "An operation is already in progress for this object.")
		return
	}
	if f.LocalDelete || !o.brokerHolds {
		delete(f.objects, k)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	o.status = messagingobj.StatusDeleting
	f.begin(o, "delete")
	writeJSON(w, http.StatusAccepted, map[string]string{"operationId": "op-2", "status": messagingobj.StatusDeleting})
}

func (f *Fake) member(w http.ResponseWriter, r *http.Request, kind, instance, name string) {
	k := key(kind, instance, name)
	o, ok := f.objects[k]
	if !ok {
		refuse(w, http.StatusNotFound, "not_found", kind+" not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !f.advance(k, o) {
			refuse(w, http.StatusNotFound, "not_found", kind+" not found")
			return
		}
		writeJSON(w, http.StatusOK, f.plain(o))
	case http.MethodDelete:
		for _, p := range f.objects {
			if p.kind == "permission" && p.instance == instance &&
				((kind == "user" && p.user == name) || (kind == "vhost" && p.vhost == name)) {
				refuse(w, http.StatusConflict, messagingobj.CodeObjectInUse, "The "+kind+" still has permissions; delete them first.")
				return
			}
		}
		f.remove(w, k, o)
	default:
		refuse(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (f *Fake) grant(w http.ResponseWriter, r *http.Request, instance, user, vhost string) {
	name := user + "/" + vhost
	k := key("permission", instance, name)
	o, ok := f.objects[k]
	switch r.Method {
	case http.MethodGet:
		if !ok || !f.advance(k, o) {
			refuse(w, http.StatusNotFound, "not_found", "permission not found")
			return
		}
		writeJSON(w, http.StatusOK, f.permission(o))
	case http.MethodPut:
		var body messagingobj.SetPermissionRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		for _, n := range []struct{ kind, name string }{{"user", user}, {"vhost", vhost}} {
			if dep, ok := f.objects[key(n.kind, instance, n.name)]; !ok || dep.status != messagingobj.StatusReady {
				refuse(w, http.StatusConflict, messagingobj.CodeObjectNotReady, "The user and the vhost must both be ready.")
				return
			}
		}
		op := "replace"
		if !ok {
			op = "create"
			o = &object{
				kind: "permission", instance: instance, name: name, user: user, vhost: vhost,
				configure: body.Configure, write: body.Write, read: body.Read,
				createdAt: "2026-10-16T10:00:00Z", updatedAt: "2026-10-16T10:00:00Z",
			}
			f.objects[k] = o
		} else if o.op != "" {
			refuse(w, http.StatusConflict, messagingobj.CodeOperationInProgress, "An operation is already in progress for this object.")
			return
		}
		o.status = messagingobj.StatusPending
		o.reqConf, o.reqWrite, o.reqRead = body.Configure, body.Write, body.Read
		f.begin(o, op)
		writeJSON(w, http.StatusAccepted, f.permission(o))
	case http.MethodDelete:
		if !ok {
			refuse(w, http.StatusNotFound, "not_found", "permission not found")
			return
		}
		f.remove(w, k, o)
	default:
		refuse(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// Client returns a client configured against the fake.
func Client(t *testing.T, url string) *client.Client {
	t.Helper()
	c := client.NewClient(url, "test-key")
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("configure: %v", err)
	}
	return c
}

// Value builds a plan/state value of typ (a schema's object type) with the
// given string attributes set and every other attribute (the timeouts block
// included) null.
func Value(t *testing.T, typ tftypes.Type, vals map[string]string) tftypes.Value {
	t.Helper()
	obj, ok := typ.(tftypes.Object)
	if !ok {
		t.Fatalf("want an object type, got %T", typ)
	}
	attrs := make(map[string]tftypes.Value, len(obj.AttributeTypes))
	for name, at := range obj.AttributeTypes {
		if v, ok := vals[name]; ok {
			attrs[name] = tftypes.NewValue(tftypes.String, v)
			continue
		}
		attrs[name] = tftypes.NewValue(at, nil)
	}
	for name := range vals {
		if _, ok := obj.AttributeTypes[name]; !ok {
			t.Fatalf("Value: %q is not an attribute", name)
		}
	}
	return tftypes.NewValue(typ, attrs)
}
