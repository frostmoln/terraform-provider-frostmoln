// Package tftagstest is the test kit for default_tags and tags_all: a fake
// backend that applies tags the way the Frostmoln services do, the per-resource
// wire profile it needs, and tftypes helpers. It is imported only by tests —
// the provider-wide gate (internal/provider/default_tags_contract_test.go) and
// the per-resource default_tags tests.
package tftagstest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
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
	// CreateOnly are attribute values the create configuration needs that a
	// configuration rebuilt after an import must NOT get back: a value the
	// platform never returns, whose absence after an import the resource itself
	// documents (a certificate's private key).
	CreateOnly map[string]tftypes.Value
	// Extra are response fields a read-back needs.
	Extra map[string]any
	// Async: the real create answers 202 with an operation (provisioning), so
	// the fake does too, and the resource's operation-polling path runs.
	Async bool
	// AsyncUpdate: the same for the update.
	AsyncUpdate bool
	// ListKey: the resource reads itself from the collection listing (no GET by
	// id), so a GET of ListPath (default: the Create path) answers
	// {ListKey: [the object]}.
	ListKey  string
	ListPath string
	// CreateWrap: the create answers 202 with the object under this key beside
	// the operation to poll (the application gateway's {gateway, operationId}).
	CreateWrap string
	// Routes are further tenant-relative GET paths the resource reads (a cluster's
	// node-pool list and kubeconfig), answered with the given body.
	Routes map[string]any
	// TagsOnly: the update route takes `tags` and nothing else (a managed offer's
	// child PATCH answers 400 field_not_updatable to any other field), so the fake
	// refuses an update body carrying another field the same way.
	TagsOnly bool
	// NoTouch is why the resource has no in-place attribute besides tags to drive
	// an update for another reason (every other attribute replaces it); the matrix
	// then skips that step. Exactly one of Touch and NoTouch is set.
	NoTouch string
	// NoImport is why the resource's import cannot recover the object the tags
	// live on; the matrix then skips its import steps.
	NoImport string
	// Handle answers requests the generic fake does not model (a multi-step
	// create); it returns false to leave a request to the fake. It runs with the
	// fake locked and may call f.create to record the create.
	Handle func(f *Fake, w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage) bool
	// Touch is an in-place change to one attribute that is not about tags —
	// a string, int or bool value, or for the `timeouts` block the `update`
	// budget — used to drive an update for another reason.
	Touch map[string]any
}

// listPath is where the listing a ListKey resource reads itself from is served.
func (p Profile) listPath() string {
	if p.ListPath != "" {
		return p.ListPath
	}
	return p.Create
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
	// offerReserved: a managed offer reserves /^frostmoln[_-]/i and REFUSES such a
	// key on every write, so none should ever be stored — but one that is (a manual
	// repair) must stay out of tags_all, in any letter case, or the next replace
	// would send it back and be refused.
	offerReserved = map[string]string{"Frostmoln_Legacy": "x", "frostmoln-note": "y"}
)

// k8sInitialPool is the frostmoln_kubernetes_cluster's initial_node_pool block in a
// create configuration: the nested object, with only flavor_id set.
func k8sInitialPool() tftypes.Value {
	str := tftypes.String
	ot := tftypes.Object{AttributeTypes: map[string]tftypes.Type{
		"id": str, "name": str, "flavor_id": str, "node_count": tftypes.Number,
		"status": str, "status_reason": str, "status_message": str, "failed_step": str,
	}}
	vals := map[string]tftypes.Value{}
	for name, at := range ot.AttributeTypes {
		vals[name] = tftypes.NewValue(at, nil)
	}
	vals["flavor_id"] = Str("k8s.gp1.medium")
	return tftypes.NewValue(ot, vals)
}

// k8sInitialPoolWire is the initial pool as the node-pool routes answer it.
var k8sInitialPoolWire = map[string]any{
	"id": "np-1", "clusterId": ObjectID, "name": "default", "status": "active",
	"flavorId": "k8s.gp1.medium", "nodeCount": 1, "isInitial": true,
}

// Str is a known string value.
func Str(s string) tftypes.Value { return tftypes.NewValue(tftypes.String, s) }

// Profiles is every taggable resource's wire profile. The provider gate fails
// for a resource with `tags` and no entry here, so a new taggable resource
// cannot ship without running the default_tags matrix.
var Profiles = map[string]Profile{
	"frostmoln_api_key": {
		Create: "/api-keys", Member: "/api-keys/" + ObjectID, Update: http.MethodPatch, Reserved: offerReserved,
		Config: map[string]tftypes.Value{
			"name":   Str("k"),
			"scopes": tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, []tftypes.Value{Str("compute:read")}),
		},
		Extra: map[string]any{"name": "k", "keyPrefix": "fmk_1_abcdef", "scopes": []string{"compute:read"}, "status": "active"},
		// identity answers a create with {apiKey: <the key>, key: <the secret>}.
		Handle: func(f *Fake, w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage) bool {
			if r.Method != http.MethodPost || r.URL.Path != "/v1/tenants/t-1/api-keys" {
				return false
			}
			f.create(r, body)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"apiKey": f.Object(), "key": "fmk_1_secret"}) // pragma: allowlist secret
			return true
		},
		Touch: map[string]any{"description": "touched"},
	},
	"frostmoln_apache_instance": {
		Create: "/webservers", Member: "/webservers/" + ObjectID, Update: http.MethodPut,
		Reserved: offerReserved,
		// The six Required attributes read back verbatim (placeholders "x" and 1).
		Extra: map[string]any{
			"name": "x", "type": "apache", "typeVersion": "x", "flavorId": "x", "storageGb": 1,
			"vpcId": "x", "subnetId": "x", "status": "running",
		},
		// A rename reads back from the platform, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_appgw_backend": {
		Create:  "/application-gateways/gw-1/backend-pools/p-1/backends",
		Member:  "/application-gateways/gw-1/backend-pools/p-1/backends/" + ObjectID,
		ListKey: "backends",
		Update:  http.MethodPatch, TagsOnly: true, ImportID: "gw-1/p-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"gateway_id": Str("gw-1"), "pool_id": Str("p-1")},
		// port reads back verbatim (placeholder 1).
		Extra:   map[string]any{"poolId": "p-1", "sourceKind": "address", "address": "10.0.0.1", "port": 1, "weight": 100, "status": "unknown", "enabled": true},
		NoTouch: "every other attribute replaces a backend, and it has no timeouts block",
	},
	"frostmoln_appgw_backend_authorization": {
		Create:   "/application-gateways/gw-1/backend-pools/p-1/backends/b-1/authorize",
		Member:   "/application-gateways/gw-1/authorizations/" + ObjectID,
		ListKey:  "items",
		ListPath: "/application-gateways/gw-1/authorizations",
		Update:   http.MethodPatch, TagsOnly: true, ImportID: "gw-1/p-1/b-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{
			"gateway_id": Str("gw-1"), "pool_id": Str("p-1"), "backend_id": Str("b-1"),
		},
		// security_group_id reads back verbatim (placeholder "x").
		Extra:   map[string]any{"gatewayId": "gw-1", "targetSecurityGroupId": "x", "protocol": "tcp", "portMin": 80, "portMax": 80},
		NoTouch: "every other attribute replaces an authorization, and it has no timeouts block",
	},
	"frostmoln_appgw_backend_pool": {
		Create: "/application-gateways/gw-1/backend-pools", Member: "/application-gateways/gw-1/backend-pools/" + ObjectID,
		Update: http.MethodPatch, ImportID: "gw-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"gateway_id": Str("gw-1")},
		// name reads back verbatim (placeholder "x"); the Optional+Computed settings
		// read back as the platform's defaults.
		Extra: map[string]any{
			"gatewayId": "gw-1", "name": "x", "protocol": "http", "algorithm": "round_robin",
			"sessionAffinity": "none", "tlsVerifyBackend": true, "timeoutConnectMs": 5000, "timeoutResponseMs": 60000,
		},
		// A settings change rides the same PATCH as a tags change.
		Touch: map[string]any{"timeout_connect_ms": 7000},
	},
	"frostmoln_appgw_certificate": {
		Create: "/application-gateways/gw-1/certificates", Member: "/application-gateways/gw-1/certificates/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, ImportID: "gw-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"gateway_id": Str("gw-1")},
		// The key only at create: an imported certificate has none in state (the
		// import warns that its next plan replaces it), so a configuration that
		// named one after the import would replace it here too.
		CreateOnly: map[string]tftypes.Value{"private_key_pem": Str("k")},
		// name reads back verbatim (placeholder "x"); chain_pem is kept from state,
		// never read back.
		Extra:   map[string]any{"gatewayId": "gw-1", "name": "x", "source": "upload", "status": "active", "sans": []string{}},
		NoTouch: "every other attribute replaces a certificate, and it has no timeouts block",
	},
	"frostmoln_appgw_listener": {
		Create: "/application-gateways/gw-1/listeners", Member: "/application-gateways/gw-1/listeners/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, ImportID: "gw-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"gateway_id": Str("gw-1")},
		// The Required attributes read back verbatim (placeholders "x" and 1).
		Extra:   map[string]any{"gatewayId": "gw-1", "name": "x", "protocol": "x", "port": 1, "geoBlockMode": "none", "enabled": true},
		NoTouch: "every other attribute replaces a listener, and it has no timeouts block",
	},
	"frostmoln_appgw_route": {
		Create: "/application-gateways/gw-1/listeners/l-1/routes", Member: "/application-gateways/gw-1/listeners/l-1/routes/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, ImportID: "gw-1/l-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"gateway_id": Str("gw-1"), "listener_id": Str("l-1")},
		// The Required attributes read back verbatim (placeholder "x").
		Extra: map[string]any{
			"listenerId": "l-1", "name": "x", "backendPoolId": "x", "priority": 100,
			"pathMatchType": "prefix", "path": "/", "action": "forward", "enabled": true,
		},
		NoTouch: "every other attribute replaces a route, and it has no timeouts block",
	},
	"frostmoln_appgw_waf_policy": {
		Create: "/application-gateways/gw-1/waf-policies", Member: "/application-gateways/gw-1/waf-policies/" + ObjectID,
		Update: http.MethodPatch, ImportID: "gw-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"gateway_id": Str("gw-1")},
		// name reads back verbatim (placeholder "x"); the settings read back as the
		// platform's defaults.
		Extra: map[string]any{
			"gatewayId": "gw-1", "name": "x", "scope": "gateway", "mode": "detect", "effectiveMode": "detect",
			"paranoiaLevel": 1, "anomalyScoreThreshold": 5, "crsVersion": "4", "failMode": "open",
			"requestBodyLimitBytes": 131072,
		},
		// A settings change rides the same PATCH as a tags change.
		Touch: map[string]any{"paranoia_level": 2},
	},
	"frostmoln_application_gateway": {
		Create: "/application-gateways", Member: "/application-gateways/" + ObjectID, Update: http.MethodPatch,
		CreateWrap: "gateway", Reserved: offerReserved,
		// The Required attributes read back verbatim (placeholder "x"); public_ip_mode
		// has a schema default the read-back must echo.
		Extra: map[string]any{
			"name": "x", "vpcId": "x", "subnetId": "x", "status": "active", "publicIpMode": "allocated",
			"flavorId": "f", "version": "v", "configStatus": "applied",
		},
		// A rename reads back from the platform, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_bucket": {
		Create: "/buckets", Member: "/buckets/" + ObjectID, IDField: "name", Update: http.MethodPut,
		Config: map[string]tftypes.Value{"name": Str(ObjectID)},
		Touch:  map[string]any{"versioning": "suspended"},
	},
	"frostmoln_workload_identity_binding": {
		Create: "/workload-identity/bindings", Member: "/workload-identity/bindings/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, Reserved: offerReserved,
		Config: map[string]tftypes.Value{
			"cluster_id": Str("c-1"), "namespace": Str("ns"), "service_account": Str("sa"),
		},
		Extra: map[string]any{"clusterId": "c-1", "namespace": "ns", "serviceAccount": "sa", "tenantId": "t-1"},
		// scopes is the binding's other in-place attribute, but it is a list and
		// the matrix touches scalars only; cluster_id/namespace/service_account
		// replace it.
		NoTouch: "its only other in-place attribute, scopes, is a list the matrix cannot touch",
	},
	"frostmoln_dns_record": {
		Create: "/dns/zones/z-1/records", Member: "/dns/zones/z-1/records/" + ObjectID, Update: http.MethodPut,
		ImportID: "z-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"zone_id": Str("z-1")},
		// The Required attributes read back verbatim (placeholder "x"); records
		// is a set the placeholder leaves empty.
		Extra: map[string]any{"zoneId": "z-1", "name": "x", "type": "x", "records": []string{}, "ttl": 300},
		// A comment change writes the recordset's own fields; tags ride along.
		Touch: map[string]any{"comment": "touched"},
	},
	"frostmoln_dns_zone": {
		Create: "/dns/zones", Member: "/dns/zones/" + ObjectID, Update: http.MethodPut,
		Touch: map[string]any{"description": "touched"},
	},
	"frostmoln_gateway": {
		// Read resolves the gateway from the vpcId-filtered listing; an import
		// (id only) reads GET /gateways/{id}.
		Create: "/gateways", Member: "/gateways/" + ObjectID, ListKey: "gateways",
		Update: http.MethodPatch, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"mode": Str("public_ip")},
		// vpc_id reads back verbatim (placeholder "x").
		Extra: map[string]any{"vpcId": "x", "mode": "public_ip", "status": "active", "origin": "explicit"},
		// The acknowledgement is the one in-place attribute that is not tags (a
		// mode or address change needs a second address the fake does not model);
		// toggling it sends no write, so the update reads the platform's set.
		Touch: map[string]any{"acknowledge_connectivity_loss": true},
	},
	"frostmoln_iam_policy": {
		Create: "/iam/policies", Member: "/iam/policies/" + ObjectID, Update: http.MethodPatch, Reserved: offerReserved,
		Config: map[string]tftypes.Value{
			"name":     Str("p"),
			"document": Str(`{"schemaVersion":"1","rules":[]}`),
		},
		Extra: map[string]any{
			"name": "p", "document": map[string]any{"schemaVersion": "1", "rules": []any{}},
			"authoredBy": "customer", "version": 1, "tenantId": "t-1",
		},
		Touch: map[string]any{"description": "touched"},
	},
	"frostmoln_image": {
		// The create runs the whole bring-your-own-image flow (create, presigned
		// upload, import, poll; imageCreate), and the image it creates is the
		// object the fake holds. A tags-only change is a PUT carrying tags alone.
		// Customer tags travel in `resourceTags`; the response's `tags` is a
		// deprecated string list of them, never read.
		Create: "/images", Member: "/images/" + ObjectID, Update: http.MethodPut, Reserved: offerReserved,
		CreateField: "resourceTags", UpdateField: "resourceTags", ReadField: "resourceTags",
		Handle: imageCreate,
		Config: map[string]tftypes.Value{
			"name": Str("x"), "source_file": Str(imageFile), "disk_format": Str("qcow2"),
			"container_format": Str("bare"),
		},
		Extra: map[string]any{
			"name": "x", "status": "active", "visibility": "private", "diskFormat": "qcow2",
			"containerFormat": "bare", "owner": "p-1", "tags": []string{"deprecated-list"},
		},
		NoImport: "the resource implements no import (the image's source_file cannot be recovered)",
		Touch:    map[string]any{"description": "touched"},
	},
	"frostmoln_instance": {
		Create: "/instances", Member: "/instances/" + ObjectID, Update: http.MethodPatch,
		CreateField: "tags", UpdateField: "metadata", ReadField: "metadata",
		Reserved: map[string]string{"frostmoln_type": "customer"},
		Async:    true,
		Touch:    map[string]any{"name": "touched"},
	},
	"frostmoln_kubernetes_cluster": {
		Create: "/kubernetes-clusters", Member: "/kubernetes-clusters/" + ObjectID, Update: http.MethodPut,
		Reserved: offerReserved,
		Config:   map[string]tftypes.Value{"initial_node_pool": k8sInitialPool()},
		// vpc_id and subnet_id force replacement and read back verbatim, so they echo
		// the placeholders the create configuration gives them.
		Extra: map[string]any{"name": "x", "status": "running", "vpcId": "x", "subnetId": "x", "addons": []string{}},
		Routes: map[string]any{
			"/kubernetes-clusters/" + ObjectID + "/node-pools":      map[string]any{"nodePools": []any{k8sInitialPoolWire}},
			"/kubernetes-clusters/" + ObjectID + "/node-pools/np-1": k8sInitialPoolWire,
			"/kubernetes-clusters/" + ObjectID + "/kubeconfig":      map[string]any{"endpoint": "https://k", "kubeconfig": "apiVersion: v1"},
		},
		// The cluster PUT has PATCH semantics: a rename sends the name alone.
		Touch: map[string]any{"name": "touched"},
	},
	"frostmoln_kubernetes_node_pool": {
		Create: "/kubernetes-clusters/c-1/node-pools", Member: "/kubernetes-clusters/c-1/node-pools/" + ObjectID,
		Update: http.MethodPatch, ImportID: "c-1/" + ObjectID,
		Reserved: offerReserved,
		Config:   map[string]tftypes.Value{"cluster_id": Str("c-1")},
		// flavor_id forces replacement and reads back verbatim (placeholder "x").
		Extra: map[string]any{"clusterId": "c-1", "name": "pool-1", "status": "active", "flavorId": "x", "nodeCount": 1},
		// node_count scales through POST .../scale, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
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
	"frostmoln_lb_listener": {
		Create: "/load-balancers/lb-1/listeners", Member: "/load-balancers/lb-1/listeners/" + ObjectID, Update: http.MethodPut,
		ClearFlag: "clearTags", ImportID: "lb-1/" + ObjectID, Reserved: networkReserved,
		Config: map[string]tftypes.Value{"load_balancer_id": Str("lb-1")},
		// The Required attributes read back verbatim (placeholders "x" and 1).
		Extra: map[string]any{"loadBalancerId": "lb-1", "name": "x", "protocol": "x", "protocolPort": 1, "adminStateUp": true},
		Async: true, AsyncUpdate: true,
		Touch: map[string]any{"name": "touched"},
	},
	"frostmoln_lb_member": {
		Create:    "/load-balancers/lb-1/pools/pool-1/members",
		Member:    "/load-balancers/lb-1/pools/pool-1/members/" + ObjectID,
		Update:    http.MethodPut,
		ClearFlag: "clearTags", ImportID: "lb-1/pool-1/" + ObjectID, Reserved: networkReserved,
		Config: map[string]tftypes.Value{"load_balancer_id": Str("lb-1"), "pool_id": Str("pool-1")},
		// The Required attributes read back verbatim (placeholders "x" and 1).
		Extra: map[string]any{"poolId": "pool-1", "address": "x", "protocolPort": 1, "weight": 1},
		Async: true, AsyncUpdate: true,
		Touch: map[string]any{"name": "touched"},
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
	"frostmoln_messaging_instance": {
		Create: "/messaging", Member: "/messaging/" + ObjectID, Update: http.MethodPut,
		Reserved: offerReserved,
		// The Required attributes read back verbatim (placeholder "x"); type has a
		// schema default the read-back must echo.
		Extra: map[string]any{
			"name": "x", "type": "lavinmq", "typeVersion": "x", "flavorId": "x", "vpcId": "x", "subnetId": "x",
			"status": "running", "persistenceMode": "persistent",
		},
		// A rename reads back from the platform, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_mysql_backup": {
		Create: "/databases/db-1/backups", Member: "/databases/db-1/backups/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"instance_id": Str("db-1")},
		// name reads back verbatim (placeholder "x").
		Extra: map[string]any{"instanceId": "db-1", "name": "x", "type": "full", "status": "completed"},
		// The import takes the bare backup id, so its first Read asks for
		// /databases//backups/{id} (instance_id is not known yet), which the client
		// collapses to this — a path no database route serves. A defect of the
		// backup import itself (it predates tags); the fake answers it so this
		// matrix still exercises the import's tag handling.
		ImportMember: "/databases/backups/" + ObjectID,
		// Every other attribute replaces a backup; a timeouts change updates in place.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_mysql_instance": {
		Create: "/databases", Member: "/databases/" + ObjectID, Update: http.MethodPut,
		Reserved: offerReserved,
		// The six Required attributes read back verbatim (placeholders "x" and 1).
		Extra: map[string]any{
			"name": "x", "type": "mysql", "typeVersion": "x", "flavorId": "x", "storageGb": 1,
			"vpcId": "x", "subnetId": "x", "status": "running", "backupRetentionDays": 35,
		},
		// A rename reads back from the platform, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_mysql_read_replica": {
		Create: "/databases/db-1/replicas", Member: "/databases/db-1/replicas/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"instance_id": Str("db-1")},
		// name reads back verbatim (placeholder "x").
		Extra: map[string]any{"primaryId": "db-1", "name": "x", "flavorId": "f", "status": "running", "port": 5432},
		// The import takes the bare replica id, so its first Read asks for
		// /databases//replicas/{id} (instance_id is not known yet), which the client
		// collapses to this — a path no database route serves. That is a defect of the replica import itself
		// (it predates tags); the fake answers it so this matrix still exercises
		// the import's tag handling.
		ImportMember: "/databases/replicas/" + ObjectID,
		// Every other attribute replaces a replica; a timeouts change updates in place.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_nginx_instance": {
		Create: "/webservers", Member: "/webservers/" + ObjectID, Update: http.MethodPut,
		Reserved: offerReserved,
		// The six Required attributes read back verbatim (placeholders "x" and 1).
		Extra: map[string]any{
			"name": "x", "type": "nginx", "typeVersion": "x", "flavorId": "x", "storageGb": 1,
			"vpcId": "x", "subnetId": "x", "status": "running",
		},
		// A rename reads back from the platform, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_postgres_backup": {
		Create: "/databases/db-1/backups", Member: "/databases/db-1/backups/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"instance_id": Str("db-1")},
		// name reads back verbatim (placeholder "x").
		Extra: map[string]any{"instanceId": "db-1", "name": "x", "type": "full", "status": "completed"},
		// The import takes the bare backup id, so its first Read asks for
		// /databases//backups/{id} (instance_id is not known yet), which the client
		// collapses to this — a path no database route serves. A defect of the
		// backup import itself (it predates tags); the fake answers it so this
		// matrix still exercises the import's tag handling.
		ImportMember: "/databases/backups/" + ObjectID,
		// Every other attribute replaces a backup; a timeouts change updates in place.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_postgres_instance": {
		Create: "/databases", Member: "/databases/" + ObjectID, Update: http.MethodPut,
		Reserved: offerReserved,
		// The six Required attributes read back verbatim (placeholders "x" and 1).
		Extra: map[string]any{
			"name": "x", "type": "postgresql", "typeVersion": "x", "flavorId": "x", "storageGb": 1,
			"vpcId": "x", "subnetId": "x", "status": "running", "backupRetentionDays": 35,
		},
		// A rename reads back from the platform, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_postgres_read_replica": {
		Create: "/databases/db-1/replicas", Member: "/databases/db-1/replicas/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"instance_id": Str("db-1")},
		// name reads back verbatim (placeholder "x").
		Extra: map[string]any{"primaryId": "db-1", "name": "x", "flavorId": "f", "status": "running", "port": 5432},
		// The import takes the bare replica id, so its first Read asks for
		// /databases//replicas/{id} (instance_id is not known yet), which the client
		// collapses to this — a path no database route serves. That is a defect of the replica import itself
		// (it predates tags); the fake answers it so this matrix still exercises
		// the import's tag handling.
		ImportMember: "/databases/replicas/" + ObjectID,
		// Every other attribute replaces a replica; a timeouts change updates in place.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_public_ip": {
		Create: "/public-ips", Member: "/public-ips/" + ObjectID, Update: http.MethodPut, Reserved: networkReserved,
		Async: true, Touch: map[string]any{"acknowledge_address_loss": true},
	},
	"frostmoln_redis_instance": {
		Create: "/caches", Member: "/caches/" + ObjectID, Update: http.MethodPut,
		Reserved: offerReserved,
		// The five Required attributes read back verbatim (placeholder "x").
		Extra: map[string]any{
			"name": "x", "type": "redis", "typeVersion": "x", "flavorId": "x", "vpcId": "x", "subnetId": "x",
			"status": "running", "storageGb": 1, "persistenceMode": "rdb", "evictionPolicy": "noeviction",
			// backup_schedule has a schema default the read-back must echo.
			"backupSchedule": "0 2 * * *", "backupRetentionDays": 35,
		},
		// A rename reads back from the platform, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
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
	"frostmoln_s3_credential": {
		Create: "/credentials", Member: "/credentials/" + ObjectID, IDField: "accessKeyId",
		Update: http.MethodPut, TagsOnly: true, Reserved: offerReserved,
		Config:  map[string]tftypes.Value{"name": Str("c")},
		Extra:   map[string]any{"name": "c", "status": "active", "secretAccessKey": "s3cr3t"}, // pragma: allowlist secret
		NoTouch: "every other attribute replaces a credential, and it has no timeouts block",
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
	"frostmoln_ssh_key": {
		Create: "/sshkeys", Member: "/sshkeys/" + ObjectID, IDField: "name",
		Update: http.MethodPatch, TagsOnly: true, Reserved: offerReserved,
		Config:  map[string]tftypes.Value{"name": Str(ObjectID), "public_key": Str("ssh-ed25519 AAAA")},
		Extra:   map[string]any{"id": "uuid-1", "publicKey": "ssh-ed25519 AAAA", "fingerprint": "SHA256:x"},
		NoTouch: "every other attribute replaces an SSH key, and it has no timeouts block",
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
	"frostmoln_valkey_instance": {
		Create: "/caches", Member: "/caches/" + ObjectID, Update: http.MethodPut,
		Reserved: offerReserved,
		// The five Required attributes read back verbatim (placeholder "x").
		Extra: map[string]any{
			"name": "x", "type": "valkey", "typeVersion": "x", "flavorId": "x", "vpcId": "x", "subnetId": "x",
			"status": "running", "storageGb": 1, "persistenceMode": "rdb", "evictionPolicy": "noeviction",
		},
		// A rename reads back from the platform, which the fake does not model; a
		// timeouts change drives an update with no tag change instead.
		Touch: map[string]any{"timeouts": "7m"},
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
	"frostmoln_webserver_deployment": {
		// The deploy record the resource tracks. The create runs the whole deploy
		// flow (create, presigned upload, start; webserverDeploy), and the record
		// it creates is the object the fake holds.
		Create: "/webservers/w-1/deploys", Member: "/webservers/w-1/deploys/" + ObjectID,
		Update: http.MethodPatch, TagsOnly: true, Reserved: offerReserved,
		Handle: webserverDeploy,
		Config: map[string]tftypes.Value{"instance_id": Str("w-1"), "source_archive": Str(deployArchive)},
		Extra:  map[string]any{"instanceId": "w-1", "status": "succeeded"},
		NoImport: "the import takes the instance id and cannot know which deploy record to track, so " +
			"there is nothing to read tags from until the next deploy",
		// A timeouts change updates in place and deploys nothing.
		Touch: map[string]any{"timeouts": "7m"},
	},
	"frostmoln_webserver_domain": {
		Create: "/webservers/w-1/domains", Member: "/webservers/w-1/domains/" + ObjectID, ListKey: "domains",
		Update: http.MethodPatch, TagsOnly: true, ImportID: "w-1/" + ObjectID, Reserved: offerReserved,
		Config: map[string]tftypes.Value{"instance_id": Str("w-1")},
		// domain_name reads back verbatim (placeholder "x").
		Extra:   map[string]any{"instanceId": "w-1", "domainName": "x", "tlsEnabled": false, "isDefault": false},
		NoTouch: "every other attribute replaces a domain binding, and it has no timeouts block",
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
	// fields are the non-tag fields updates carried, read back over Extra, so an
	// in-place change of another attribute (Touch) reads back as written.
	fields map[string]json.RawMessage
}

// updatedAt is the object's updatedAt: like the platform, the fake bumps it on
// every write, so a plan that kept the prior value fails the apply.
func (f *Fake) updatedAt() string {
	return fmt.Sprintf("2026-09-13T00:00:00.%06dZ", len(f.writes))
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
	obj["updatedAt"] = f.updatedAt()
	for k, raw := range f.fields {
		var v any
		_ = json.Unmarshal(raw, &v)
		obj[k] = v
	}
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

	if f.Profile.Handle != nil && f.Profile.Handle(f, w, r, body) {
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == tenant+f.Profile.Create:
		f.create(r, body)
		if f.Profile.CreateWrap != "" {
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				f.Profile.CreateWrap: f.Object(), "operationId": operationID, "status": "pending",
			})
			return
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
	case r.Method == http.MethodGet && f.Profile.ListKey != "" && r.URL.Path == tenant+f.Profile.listPath():
		_ = json.NewEncoder(w).Encode(map[string]any{f.Profile.ListKey: []any{f.Object()}})
	case r.Method == http.MethodGet && isMember:
		_ = json.NewEncoder(w).Encode(f.Object())
	case r.Method == http.MethodGet && f.Profile.Routes[strings.TrimPrefix(r.URL.Path, tenant)] != nil:
		_ = json.NewEncoder(w).Encode(f.Profile.Routes[strings.TrimPrefix(r.URL.Path, tenant)])
	case r.Method == f.Profile.Update && isMember:
		if f.Profile.TagsOnly {
			for k := range body {
				if k != f.Profile.Field(f.Profile.UpdateField) {
					w.WriteHeader(http.StatusBadRequest)
					_ = json.NewEncoder(w).Encode(map[string]string{"code": "field_not_updatable", "message": k + " cannot be updated"})
					return
				}
			}
		}
		f.writes = append(f.writes, Write{r.Method, r.URL.Path, body})
		for k, raw := range body {
			if k != f.Profile.Field(f.Profile.UpdateField) && k != f.Profile.ClearFlag {
				if f.fields == nil {
					f.fields = map[string]json.RawMessage{}
				}
				f.fields[k] = raw
			}
		}
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

// create records a create: its write, and the object's tags — the create body's,
// plus Stamp as the platform stamps tenant default tags. Called with f locked.
func (f *Fake) create(r *http.Request, body map[string]json.RawMessage) {
	f.writes = append(f.writes, Write{r.Method, r.URL.Path, body})
	f.tags = map[string]string{}
	if raw, ok := body[f.Profile.Field(f.Profile.CreateField)]; ok {
		_ = json.Unmarshal(raw, &f.tags)
	}
	for k, v := range f.Stamp {
		f.tags[k] = v
	}
}

// webserverDeploy is the frostmoln_webserver_deployment flow beyond the deploy
// record itself: create the deploy (answering the presigned upload this fake
// also serves), the upload, and the start.
func webserverDeploy(f *Fake, w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage) bool {
	const tenant = "/v1/tenants/t-1"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == tenant+f.Profile.Create:
		f.create(r, body)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"deployId": ObjectID, "uploadUrl": "http://" + r.Host + "/upload",
			"uploadFields": map[string]string{"key": "k"}, "status": "pending_upload",
		})
		return true
	case r.Method == http.MethodPost && r.URL.Path == "/upload":
		w.WriteHeader(http.StatusNoContent)
		return true
	case r.Method == http.MethodPost && r.URL.Path == tenant+f.Profile.Member+"/start":
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(f.Object())
		return true
	}
	return false
}

// imageCreate is the frostmoln_image flow beyond the image record itself:
// create the image (answering a presigned upload form this fake also serves),
// the upload, and the import. The import poll reads the member, already active.
func imageCreate(f *Fake, w http.ResponseWriter, r *http.Request, body map[string]json.RawMessage) bool {
	const tenant = "/v1/tenants/t-1"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == tenant+f.Profile.Create:
		f.create(r, body)
		obj := f.Object()
		obj["upload"] = map[string]any{
			"url": "http://" + r.Host + "/upload", "fields": map[string]string{"key": "k"},
			"expiresAt": "2999-01-01T00:00:00Z",
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(obj)
		return true
	case r.Method == http.MethodPost && r.URL.Path == "/upload":
		w.WriteHeader(http.StatusNoContent)
		return true
	case r.Method == http.MethodPost && r.URL.Path == tenant+f.Profile.Member+"/import":
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{})
		return true
	}
	return false
}

// imageFile is a small file standing in for a disk image.
var imageFile = func() string {
	f, err := os.CreateTemp("", "tftagstest-image-*.qcow2")
	if err != nil {
		return ""
	}
	_, _ = f.WriteString("disk image bytes")
	_ = f.Close()
	return f.Name()
}()

// deployArchive is a small file standing in for a site archive: the deployment
// hashes it at plan and apply time.
var deployArchive = func() string {
	f, err := os.CreateTemp("", "tftagstest-site-*.tar.gz")
	if err != nil {
		return ""
	}
	_, _ = f.WriteString("site archive bytes")
	_ = f.Close()
	return f.Name()
}()

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
	for name, v := range p.CreateOnly {
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

// NullUnsetTags gives a test fixture model that predates tags (a zero types.Map,
// which has no element type and cannot be set) the null maps an untagged
// configuration holds: every types.Map field of the struct ptr points at that has
// no element type becomes MapNull(StringType).
func NullUnsetTags(ptr any) {
	v := reflect.ValueOf(ptr).Elem()
	mapType := reflect.TypeOf(types.Map{})
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Type() != mapType || !f.CanSet() {
			continue
		}
		if m := f.Interface().(types.Map); m.ElementType(context.Background()) == nil {
			f.Set(reflect.ValueOf(types.MapNull(types.StringType)))
		}
	}
}
