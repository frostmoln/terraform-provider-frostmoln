package public_ip_association

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/public_ip"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/schemadoc"
)

// --- helpers ---

func TestCompositeID(t *testing.T) {
	if got := compositeID("pip-1", "inst-2"); got != "pip-1/inst-2" {
		t.Errorf("expected pip-1/inst-2, got %s", got)
	}
}

func getSchema(t *testing.T) resource.SchemaResponse {
	t.Helper()
	r := NewResource()
	var schemaResp resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	if schemaResp.Diagnostics.HasError() {
		t.Fatalf("schema failed: %v", schemaResp.Diagnostics.Errors())
	}
	return schemaResp
}

func configureResource(t *testing.T, r resource.Resource, c *client.Client) {
	t.Helper()
	rc, ok := r.(resource.ResourceWithConfigure)
	if !ok {
		t.Fatal("resource does not implement ResourceWithConfigure")
	}
	var configResp resource.ConfigureResponse
	rc.Configure(context.Background(), resource.ConfigureRequest{ProviderData: c}, &configResp)
	if configResp.Diagnostics.HasError() {
		t.Fatalf("configure failed: %v", configResp.Diagnostics.Errors())
	}
}

func newTestClient(t *testing.T, server *httptest.Server) *client.Client {
	t.Helper()
	c := client.NewClient(server.URL, "test-key") // pragma: allowlist secret
	if err := c.Configure(context.Background()); err != nil {
		t.Fatalf("client configure failed: %v", err)
	}
	return c
}

// writeMe answers the tenant lookup every client.Configure makes.
func writeMe(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]string{"id": "user-1", "tenantId": "tenant-1"})
}

func writeNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": "NOT_FOUND", "message": "not found"},
	})
}

// writeAccepted answers a provisioning write the way the real API does: 202
// with an operation envelope and nothing else.
func writeAccepted(w http.ResponseWriter, opID string) {
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"operationId": opID, "status": "accepted", "resourceType": "public_ip",
	})
}

func writeInstance(w http.ResponseWriter, portIDs ...string) {
	networks := make([]map[string]string, 0, len(portIDs))
	for _, p := range portIDs {
		networks = append(networks, map[string]string{"portId": p})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"networks": networks})
}

func stateValue(t *testing.T, s schema.Schema, id, publicIPID, instanceID, portID any) tftypes.Value {
	t.Helper()
	tfType := s.Type().TerraformType(context.Background())
	return tftypes.NewValue(tfType, map[string]tftypes.Value{
		"id":           tftypes.NewValue(tftypes.String, id),
		"public_ip_id": tftypes.NewValue(tftypes.String, publicIPID),
		"instance_id":  tftypes.NewValue(tftypes.String, instanceID),
		"port_id":      tftypes.NewValue(tftypes.String, portID),
	})
}

// --- schema ---

// TestSchema_MutualExclusivityIsDocumentedOnBothResources is the whole reason
// this resource can coexist with frostmoln_public_ip.instance_id. Nothing in
// the framework can refuse a configuration that uses both — they are separate
// resources, and no cross-resource validation runs — so the ONLY protection a
// practitioner gets is that both surfaces say so. These descriptions render to
// the registry and to docs.frostmoln.se; losing the note from either one leaves
// half the readers with no warning at all.
func TestSchema_MutualExclusivityIsDocumentedOnBothResources(t *testing.T) {
	t.Parallel()

	assoc := getSchema(t)
	if !strings.Contains(assoc.Schema.Description, public_ip.MutualExclusivityNote) {
		t.Errorf("frostmoln_public_ip_association's description must carry the mutual-exclusivity "+
			"note; a practitioner arriving here has to be told frostmoln_public_ip.instance_id "+
			"exists and conflicts:\n%s", assoc.Schema.Description)
	}

	var pipSchema resource.SchemaResponse
	public_ip.NewResource().Schema(context.Background(), resource.SchemaRequest{}, &pipSchema)
	if !strings.Contains(pipSchema.Schema.Description, public_ip.MutualExclusivityNote) {
		t.Errorf("frostmoln_public_ip's description must carry the mutual-exclusivity note:\n%s",
			pipSchema.Schema.Description)
	}
	instanceID, ok := pipSchema.Schema.Attributes["instance_id"]
	if !ok {
		t.Fatal("frostmoln_public_ip has no instance_id attribute")
	}
	if !strings.Contains(instanceID.GetDescription(), public_ip.MutualExclusivityNote) {
		t.Errorf("frostmoln_public_ip.instance_id — the attribute that conflicts — must carry the "+
			"note itself, not only the resource description:\n%s", instanceID.GetDescription())
	}

	// Both halves have to name the other resource, or the note tells a reader
	// nothing actionable.
	for _, want := range []string{"frostmoln_public_ip.instance_id", "frostmoln_public_ip_association"} {
		if !strings.Contains(public_ip.MutualExclusivityNote, want) {
			t.Errorf("the mutual-exclusivity note must name %s", want)
		}
	}
}

// TestSchema_GatewayOrderingNoteIsRendered is the wiring check for THIS
// resource: the note reaches the description, naming this resource as where the
// `depends_on` goes.
//
// Membership of the attaching-surface set, and the note's claims, are owned by
// internal/provider/gateway_ordering_note_test.go — this file used to assert
// both and said "there are TWO of those" in its name and its comment. There are
// six. A guard whose title is a false statement about the size of the set is one
// the next reader believes.
func TestSchema_GatewayOrderingNoteIsRendered(t *testing.T) {
	t.Parallel()

	assoc := getSchema(t)
	if !strings.Contains(assoc.Schema.Description, schemadoc.GatewayOrderingNote("frostmoln_public_ip_association")) {
		t.Errorf("frostmoln_public_ip_association's description must carry the gateway-ordering note "+
			"naming ITSELF as where the depends_on goes:\n%s", assoc.Schema.Description)
	}
}

func TestSchema_BothAttributesForceReplacement(t *testing.T) {
	t.Parallel()

	s := getSchema(t).Schema
	for _, name := range []string{"public_ip_id", "instance_id"} {
		attr, ok := s.Attributes[name].(schema.StringAttribute)
		if !ok {
			t.Fatalf("%s is not a StringAttribute", name)
		}
		if !attr.Required {
			t.Errorf("%s must be Required", name)
		}
		if len(attr.PlanModifiers) == 0 {
			t.Errorf("%s must force replacement — the platform has no in-place re-point", name)
		}
	}
	if _, ok := s.Attributes["port_id"]; !ok {
		t.Error("port_id must be exposed: the platform attaches an address to a PORT")
	}
}

func TestMetadata(t *testing.T) {
	t.Parallel()

	var resp resource.MetadataResponse
	NewResource().Metadata(context.Background(), resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_public_ip_association" {
		t.Errorf("expected frostmoln_public_ip_association, got %s", resp.TypeName)
	}
}

// --- Create ---

func TestCreate_AssociatesAndRecordsThePort(t *testing.T) {
	var associateCalls atomic.Int32
	var sentPortID atomic.Value
	attachedPort := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1", "port-2")

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			associateCalls.Add(1)
			var req apiAssociateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			sentPortID.Store(req.PortID)
			attachedPort = req.PortID
			writeAccepted(w, "op-1")

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-1":
			_ = json.NewEncoder(w).Encode(client.Operation{
				OperationID: "op-1", Status: client.OperationStatusCompleted, ResourceID: "pip-1",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: attachedPort})

		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	ctx := context.Background()
	planVal := stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(ctx, resource.CreateRequest{Plan: tfsdk.Plan{Schema: s, Raw: planVal}}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", resp.Diagnostics.Errors())
	}
	if associateCalls.Load() != 1 {
		t.Errorf("expected exactly one associate call, got %d", associateCalls.Load())
	}
	// The FIRST port, matching what frostmoln_public_ip.instance_id binds.
	if got := sentPortID.Load(); got != "port-1" {
		t.Errorf("expected the instance's first port (port-1) to be bound, got %v", got)
	}

	var model PublicIPAssociationModel
	resp.State.Get(ctx, &model)
	if model.ID.ValueString() != "pip-1/inst-1" {
		t.Errorf("expected composite id pip-1/inst-1, got %s", model.ID.ValueString())
	}
	if model.PortID.ValueString() != "port-1" {
		t.Errorf("expected port_id port-1, got %s", model.PortID.ValueString())
	}
}

// TestCreate_TranslatesTheGatewayRefusal proves the association reuses
// frostmoln_public_ip's error mapping rather than surfacing a bare 409. The
// platform refuses to attach a VPC's outbound source address to an instance,
// and that refusal reads as an obscure conflict on an address that looks idle
// from every other angle.
func TestCreate_TranslatesTheGatewayRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")

		// A VPC's outbound source address has NO port — that is why the
		// pre-flight read cannot refuse this one, and why the platform has to.
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-gw":
			_ = json.NewEncoder(w).Encode(apiPublicIP{
				ID:         "pip-gw",
				Attachment: &apiPublicIPAttachment{Kind: public_ip.AttachmentKindGateway, VPCID: "vpc-1"},
			})

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-gw/associate":
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{
					"code":    "PUBLIC_IP_IN_USE_BY_GATEWAY",
					"message": "public IP is in use by a gateway",
				},
			})

		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-gw", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected the gateway refusal to fail Create")
	}
	summary := resp.Diagnostics.Errors()[0].Summary()
	if !strings.Contains(summary, "outbound source address") {
		t.Errorf("expected the shared gateway-refusal wording, got summary %q with detail %q",
			summary, resp.Diagnostics.Errors()[0].Detail())
	}
}

func TestCreate_FailsWhenTheOperationFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			writeAccepted(w, "op-fail")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-fail":
			_ = json.NewEncoder(w).Encode(client.Operation{
				OperationID: "op-fail", Status: client.OperationStatusFailed, Error: "port already has a floating ip",
			})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed operation must fail the apply — a 202 is not an attachment")
	}
	if !resp.State.Raw.IsNull() {
		t.Error("no state must be recorded for an association the platform refused")
	}
}

// TestCreate_RefusesToRecordAMismatch guards the case a completed operation is
// not proof of: the address ended up somewhere other than the port that was
// asked for. Recording it would give a row that plans a no-op forever while the
// instance has no public address.
func TestCreate_RefusesToRecordAMismatch(t *testing.T) {
	var movedAway atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			movedAway.Store(true)
			writeAccepted(w, "op-1")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-1":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: client.OperationStatusCompleted})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			if movedAway.Load() {
				_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: "port-somewhere-else"})
				return
			}
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1"})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a mismatch between the requested port and the reported one to fail Create")
	}
}

func TestCreate_FailsWhenTheInstanceHasNoPort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w)
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an instance with no network port to fail Create")
	}
}

// --- Read ---

// readCase drives Read against a mock and reports whether the row survived.
type readCase struct {
	name string
	// publicIP is what GET /public-ips/{id} answers; nil means 404.
	publicIP *apiPublicIP
	// instancePorts is what the instance reports; nil means the instance 404s.
	instancePorts []string
	instanceGone  bool
	wantRemoved   bool
	wantPortID    string
}

func TestRead(t *testing.T) {
	cases := []readCase{
		{
			name:          "still attached to the instance",
			publicIP:      &apiPublicIP{ID: "pip-1", PortID: "port-1"},
			instancePorts: []string{"port-1"},
			wantPortID:    "port-1",
		},
		{
			// An instance can have more than one interface, and an address does
			// not have to be on the first. Comparing only the first port would
			// report this as detached and silently drop a live association.
			name:          "attached to a port that is not the instance's first",
			publicIP:      &apiPublicIP{ID: "pip-1", PortID: "port-2"},
			instancePorts: []string{"port-1", "port-2"},
			wantPortID:    "port-2",
		},
		{
			name:          "detached out of band",
			publicIP:      &apiPublicIP{ID: "pip-1"},
			instancePorts: []string{"port-1"},
			wantRemoved:   true,
		},
		{
			name:          "re-attached to something else",
			publicIP:      &apiPublicIP{ID: "pip-1", PortID: "port-elsewhere"},
			instancePorts: []string{"port-1"},
			wantRemoved:   true,
		},
		{
			name:        "the address is gone",
			publicIP:    nil,
			wantRemoved: true,
		},
		{
			name:         "the instance is gone",
			publicIP:     &apiPublicIP{ID: "pip-1", PortID: "port-1"},
			instanceGone: true,
			wantRemoved:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
					writeMe(w)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
					if tc.publicIP == nil {
						writeNotFound(w)
						return
					}
					_ = json.NewEncoder(w).Encode(tc.publicIP)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
					if tc.instanceGone {
						writeNotFound(w)
						return
					}
					writeInstance(w, tc.instancePorts...)
				default:
					writeNotFound(w)
				}
			}))
			defer server.Close()

			r := NewResource()
			configureResource(t, r, newTestClient(t, server))
			s := getSchema(t).Schema

			ctx := context.Background()
			var resp resource.ReadResponse
			resp.State = tfsdk.State{Schema: s}
			r.Read(ctx, resource.ReadRequest{
				State: tfsdk.State{Schema: s, Raw: stateValue(t, s, "pip-1/inst-1", "pip-1", "inst-1", "port-1")},
			}, &resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("Read failed: %v", resp.Diagnostics.Errors())
			}
			if removed := resp.State.Raw.IsNull(); removed != tc.wantRemoved {
				t.Fatalf("expected removed=%v, got removed=%v", tc.wantRemoved, removed)
			}
			if tc.wantRemoved {
				return
			}

			var model PublicIPAssociationModel
			resp.State.Get(ctx, &model)
			if model.PortID.ValueString() != tc.wantPortID {
				t.Errorf("expected port_id %s, got %s", tc.wantPortID, model.PortID.ValueString())
			}
			if model.ID.ValueString() != "pip-1/inst-1" {
				t.Errorf("expected composite id pip-1/inst-1, got %s", model.ID.ValueString())
			}
		})
	}
}

// TestRead_AfterImportResolvesThePort covers the shape ImportState leaves
// behind: id, public_ip_id and instance_id are set and port_id is not. Read has
// to fill it from the platform rather than from state, or an imported
// association would immediately look detached.
func TestRead_AfterImportResolvesThePort(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: "port-9"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-9")
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	ctx := context.Background()
	var resp resource.ReadResponse
	resp.State = tfsdk.State{Schema: s}
	r.Read(ctx, resource.ReadRequest{
		State: tfsdk.State{Schema: s, Raw: stateValue(t, s, "pip-1/inst-1", "pip-1", "inst-1", nil)},
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Read failed: %v", resp.Diagnostics.Errors())
	}
	var model PublicIPAssociationModel
	resp.State.Get(ctx, &model)
	if model.PortID.ValueString() != "port-9" {
		t.Errorf("expected Read to resolve port_id to port-9, got %q", model.PortID.ValueString())
	}
}

// --- Delete ---

func TestDelete_Disassociates(t *testing.T) {
	var disassociateCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: "port-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/disassociate":
			disassociateCalls.Add(1)
			writeAccepted(w, "op-d")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-d":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-d", Status: client.OperationStatusCompleted})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.DeleteResponse
	resp.State = tfsdk.State{Schema: s}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateValue(t, s, "pip-1/inst-1", "pip-1", "inst-1", "port-1")},
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete failed: %v", resp.Diagnostics.Errors())
	}
	if disassociateCalls.Load() != 1 {
		t.Errorf("expected one disassociate call, got %d", disassociateCalls.Load())
	}
}

func TestDelete_FailsWhenTheOperationFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: "port-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/disassociate":
			writeAccepted(w, "op-d")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-d":
			_ = json.NewEncoder(w).Encode(client.Operation{
				OperationID: "op-d", Status: client.OperationStatusFailed, Error: "port is locked",
			})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.DeleteResponse
	resp.State = tfsdk.State{Schema: s}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateValue(t, s, "pip-1/inst-1", "pip-1", "inst-1", "port-1")},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a failed disassociate operation must fail the apply and keep the row in state")
	}
}

// TestDelete_DoesNotDetachSomebodyElsesBinding is the point of the pre-flight
// read. `disassociate` takes no port — it removes whatever binding the address
// currently has — so issuing it blind when the address has been moved would
// take it off a port this resource never attached it to, and the running
// instance that holds it now loses its inbound address with no plan line
// anywhere saying so.
func TestDelete_DoesNotDetachSomebodyElsesBinding(t *testing.T) {
	var disassociateCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: "port-someone-else"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/disassociate":
			disassociateCalls.Add(1)
			writeAccepted(w, "op-d")
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.DeleteResponse
	resp.State = tfsdk.State{Schema: s}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateValue(t, s, "pip-1/inst-1", "pip-1", "inst-1", "port-1")},
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Delete must succeed — the association is gone either way: %v", resp.Diagnostics.Errors())
	}
	if disassociateCalls.Load() != 0 {
		t.Error("Delete must NOT detach an address that has been re-attached elsewhere")
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("the practitioner has to be told the address was re-attached outside this configuration")
	}
}

func TestDelete_NoOpWhenAlreadyDetachedOrGone(t *testing.T) {
	cases := []struct {
		name     string
		publicIP *apiPublicIP // nil means the address 404s
	}{
		{name: "already detached", publicIP: &apiPublicIP{ID: "pip-1"}},
		{name: "address released out of band", publicIP: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var disassociateCalls atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
					writeMe(w)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
					if tc.publicIP == nil {
						writeNotFound(w)
						return
					}
					_ = json.NewEncoder(w).Encode(tc.publicIP)
				case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/disassociate":
					disassociateCalls.Add(1)
					writeAccepted(w, "op-d")
				default:
					writeNotFound(w)
				}
			}))
			defer server.Close()

			r := NewResource()
			configureResource(t, r, newTestClient(t, server))
			s := getSchema(t).Schema

			var resp resource.DeleteResponse
			resp.State = tfsdk.State{Schema: s}
			r.Delete(context.Background(), resource.DeleteRequest{
				State: tfsdk.State{Schema: s, Raw: stateValue(t, s, "pip-1/inst-1", "pip-1", "inst-1", "port-1")},
			}, &resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("Delete must be idempotent: %v", resp.Diagnostics.Errors())
			}
			if disassociateCalls.Load() != 0 {
				t.Errorf("expected no disassociate call, got %d", disassociateCalls.Load())
			}
		})
	}
}

// --- Update / ImportState ---

func TestUpdate_IsRefused(t *testing.T) {
	t.Parallel()

	var resp resource.UpdateResponse
	NewResource().Update(context.Background(), resource.UpdateRequest{}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("Update must fail loudly rather than record a change it did not make")
	}
}

func TestImportState(t *testing.T) {
	cases := []struct {
		name       string
		importID   string
		wantErr    bool
		wantPIP    string
		wantInstID string
	}{
		{name: "valid", importID: "pip-1/inst-1", wantPIP: "pip-1", wantInstID: "inst-1"},
		{name: "no separator", importID: "pip-1", wantErr: true},
		{name: "empty public ip", importID: "/inst-1", wantErr: true},
		{name: "empty instance", importID: "pip-1/", wantErr: true},
		{name: "empty", importID: "", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r, ok := NewResource().(resource.ResourceWithImportState)
			if !ok {
				t.Fatal("resource does not implement ResourceWithImportState")
			}
			s := getSchema(t).Schema
			ctx := context.Background()

			var resp resource.ImportStateResponse
			resp.State = tfsdk.State{
				Schema: s,
				Raw:    stateValue(t, s, nil, nil, nil, nil),
			}
			r.ImportState(ctx, resource.ImportStateRequest{ID: tc.importID}, &resp)

			if tc.wantErr {
				if !resp.Diagnostics.HasError() {
					t.Fatalf("expected import id %q to be rejected", tc.importID)
				}
				return
			}
			if resp.Diagnostics.HasError() {
				t.Fatalf("import failed: %v", resp.Diagnostics.Errors())
			}

			var model PublicIPAssociationModel
			resp.State.Get(ctx, &model)
			if model.PublicIPID.ValueString() != tc.wantPIP {
				t.Errorf("expected public_ip_id %s, got %s", tc.wantPIP, model.PublicIPID.ValueString())
			}
			if model.InstanceID.ValueString() != tc.wantInstID {
				t.Errorf("expected instance_id %s, got %s", tc.wantInstID, model.InstanceID.ValueString())
			}
			if model.ID.ValueString() != tc.importID {
				t.Errorf("expected id %s, got %s", tc.importID, model.ID.ValueString())
			}
		})
	}
}

func TestConfigure_RejectsUnexpectedProviderData(t *testing.T) {
	t.Parallel()

	rc, ok := NewResource().(resource.ResourceWithConfigure)
	if !ok {
		t.Fatal("resource does not implement ResourceWithConfigure")
	}

	var nilResp resource.ConfigureResponse
	rc.Configure(context.Background(), resource.ConfigureRequest{}, &nilResp)
	if nilResp.Diagnostics.HasError() {
		t.Errorf("a nil ProviderData is the pre-Configure call and must be ignored: %v", nilResp.Diagnostics.Errors())
	}

	var badResp resource.ConfigureResponse
	rc.Configure(context.Background(), resource.ConfigureRequest{ProviderData: "not a client"}, &badResp)
	if !badResp.Diagnostics.HasError() {
		t.Error("expected an error for unexpected provider data")
	}
}

// --- Create: the address is already where it should be, or somewhere else ---

// newFastResource is the resource with the operation wait cut to milliseconds.
//
// The branch that decides what to record when the platform has NOT answered in
// time is the one this whole resource turns on — it is the difference between a
// state row and a live attachment nothing knows about — and the five-minute
// production bound cannot be exercised in a test any other way.
func newFastResource(t *testing.T, server *httptest.Server) resource.Resource {
	t.Helper()
	r, ok := NewResource().(*publicIPAssociationResource)
	if !ok {
		t.Fatal("NewResource did not return the concrete resource")
	}
	r.pollInterval = 5 * time.Millisecond
	r.pollTimeout = 75 * time.Millisecond
	configureResource(t, r, newTestClient(t, server))
	return r
}

// TestCreate_AdoptsAnAttachmentThatAlreadyExists is the REGRESSION TEST for the
// permanently-stranded practitioner.
//
// The associate is not idempotent: the platform answers a second one with 409
// "Public IP is already associated". So an apply that attached the address but
// failed to record it — a wait that timed out on a slow-but-successful
// associate, an interrupted apply — used to leave a configuration that could
// never apply again: every retry hit that 409, with a diagnostic that mentioned
// neither state nor import.
//
// Reading the address BEFORE attaching is what breaks that loop. The requested
// attachment already exists, so it is recorded and nothing is sent.
func TestCreate_AdoptsAnAttachmentThatAlreadyExists(t *testing.T) {
	var associateCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			// The attachment the previous, unrecorded apply made.
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: "port-1"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			associateCalls.Add(1)
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "resource_in_use", "message": "Public IP is already associated"},
			})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	ctx := context.Background()
	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("an attachment that already matches the configuration must be adopted, not refused: %v",
			resp.Diagnostics.Errors())
	}
	if associateCalls.Load() != 0 {
		t.Errorf("the associate must NOT be sent for an attachment that already exists (it 409s), got %d calls",
			associateCalls.Load())
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("the existing attachment must be RECORDED — leaving state null is what strands the practitioner")
	}

	var model PublicIPAssociationModel
	resp.State.Get(ctx, &model)
	if model.ID.ValueString() != "pip-1/inst-1" || model.PortID.ValueString() != "port-1" {
		t.Errorf("expected id pip-1/inst-1 and port_id port-1, got %s / %s",
			model.ID.ValueString(), model.PortID.ValueString())
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("adopting an attachment nobody planned has to be visible: it is also what using both " +
			"frostmoln_public_ip.instance_id and this resource looks like")
	}
}

// TestCreate_RefusesAnAddressAttachedElsewhere: the pre-flight read must not
// become an adopt-anything. An address on someone else's port is a conflict,
// and the practitioner gets told which port rather than the platform's bare 409.
func TestCreate_RefusesAnAddressAttachedElsewhere(t *testing.T) {
	var associateCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: "port-of-another-instance"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			associateCalls.Add(1)
			writeAccepted(w, "op-1")
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("attaching an address that already serves something else must be refused")
	}
	if associateCalls.Load() != 0 {
		t.Error("the refusal must come from the pre-flight read, before anything is sent")
	}
	if detail := resp.Diagnostics.Errors()[0].Detail(); !strings.Contains(detail, "port-of-another-instance") {
		t.Errorf("the diagnostic must name the port that holds the address, got: %s", detail)
	}
}

// TestCreate_RefusesWhenThePublicIPDoesNotExist: this resource never allocates,
// so an id that names nothing is a configuration error and has to read as one.
func TestCreate_RefusesWhenThePublicIPDoesNotExist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-gone", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("expected a missing public IP to fail Create")
	}
	if summary := resp.Diagnostics.Errors()[0].Summary(); !strings.Contains(summary, "Not Found") {
		t.Errorf("expected a not-found diagnostic, got %q", summary)
	}
}

// --- Create: what the wait could not settle ---

// TestCreate_TimeoutRecordsAnAttachmentThatLanded is the other half of the
// stranding regression. The wait is bounded; the WRITE is not. An associate
// that merely took longer than the provider waits has still happened, and
// erroring without recording it leaves exactly the live-attachment-no-state
// situation the adoption path above exists to recover from.
func TestCreate_TimeoutRecordsAnAttachmentThatLanded(t *testing.T) {
	var attached atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			// The platform does the work; the operation just never says so in
			// time.
			attached.Store(true)
			writeAccepted(w, "op-slow")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-slow":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-slow", Status: "running"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			if attached.Load() {
				_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: "port-1"})
				return
			}
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1"})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := newFastResource(t, server)
	s := getSchema(t).Schema

	ctx := context.Background()
	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("an attachment that landed after the wait gave up must be recorded, not errored: %v",
			resp.Diagnostics.Errors())
	}
	if resp.State.Raw.IsNull() {
		t.Fatal("state must hold the attachment the platform actually made")
	}
	var model PublicIPAssociationModel
	resp.State.Get(ctx, &model)
	if model.PortID.ValueString() != "port-1" {
		t.Errorf("expected port_id port-1, got %q", model.PortID.ValueString())
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Error("the practitioner has to be told the wait gave up even though the outcome was good")
	}
}

// TestCreate_TimeoutSaysHowToImportWhenNothingIsKnown covers the case that
// cannot be recovered automatically: the wait gave up and the address is not
// (yet) attached. The apply must fail — but with the ONE thing that unblocks
// the practitioner if it does complete a moment later, which is the import
// command, not a bare timeout.
func TestCreate_TimeoutSaysHowToImportWhenNothingIsKnown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			writeAccepted(w, "op-slow")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-slow":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-slow", Status: "running"})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1"})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := newFastResource(t, server)
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("an unfinished associate must fail the apply — a 202 is not an attachment")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "terraform import frostmoln_public_ip_association.<name> pip-1/inst-1") {
		t.Errorf("the diagnostic must carry the exact import command — the associate is not idempotent, "+
			"so without it every later apply is refused with a 409 for ever. Got: %s", detail)
	}
}

// TestCreate_RefusedOperationSaysNothingHappened is the OTHER side of the same
// branch, and the reason the two are not one message: a terminal refusal
// changed nothing, so the advice is "apply again", and telling that
// practitioner to go and import something would send them looking for an
// attachment that does not exist.
func TestCreate_RefusedOperationSaysNothingHappened(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			writeAccepted(w, "op-no")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-no":
			_ = json.NewEncoder(w).Encode(client.Operation{
				OperationID: "op-no", Status: client.OperationStatusFailed, Error: "quota exceeded",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1"})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := newFastResource(t, server)
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", tftypes.UnknownValue)},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a refused operation must fail the apply")
	}
	detail := resp.Diagnostics.Errors()[0].Detail()
	if !strings.Contains(detail, "applying again is safe") {
		t.Errorf("a refusal must say nothing was changed, got: %s", detail)
	}
	if strings.Contains(detail, "terraform import") {
		t.Errorf("a refusal must NOT send the practitioner looking for an attachment that was never "+
			"made, got: %s", detail)
	}
	if !strings.Contains(detail, "quota exceeded") {
		t.Errorf("the platform's own reason must survive, got: %s", detail)
	}
}

// --- port_id ---

// TestSchema_PortIDIsSettable: `fm network public-ip associate --port-id` lets a
// customer put an address on a multi-NIC instance's second interface, and Read
// here accepts any of the instance's ports — so a configuration that cannot
// SAY which port has a divergence it can neither express nor see. A replace
// would silently move the address back to the first interface.
func TestSchema_PortIDIsSettable(t *testing.T) {
	t.Parallel()

	attr, ok := getSchema(t).Schema.Attributes["port_id"].(schema.StringAttribute)
	if !ok {
		t.Fatal("port_id is not a StringAttribute")
	}
	if !attr.Optional {
		t.Error("port_id must be Optional: the CLI exposes --port-id, and a multi-NIC attachment the " +
			"configuration cannot express is one a replace moves silently")
	}
	if !attr.Computed {
		t.Error("port_id must stay Computed: a single-homed instance resolves it, and it must survive " +
			"an import that never set it")
	}
}

func TestCreate_BindsTheConfiguredPort(t *testing.T) {
	var sentPortID atomic.Value
	attachedPort := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1", "port-2")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			var req apiAssociateRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			sentPortID.Store(req.PortID)
			attachedPort = req.PortID
			writeAccepted(w, "op-1")
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-1":
			_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-1", Status: client.OperationStatusCompleted})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: attachedPort})
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	ctx := context.Background()
	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(ctx, resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", "port-2")},
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("Create failed: %v", resp.Diagnostics.Errors())
	}
	if got := sentPortID.Load(); got != "port-2" {
		t.Errorf("expected the configured port (port-2) to be bound, got %v", got)
	}
	var model PublicIPAssociationModel
	resp.State.Get(ctx, &model)
	if model.PortID.ValueString() != "port-2" {
		t.Errorf("expected port_id port-2 in state, got %s", model.PortID.ValueString())
	}
}

// TestCreate_RefusesAPortThatIsNotOnTheInstance: the platform binds ports and
// does not care which instance the configuration names, so it would accept
// this. Read would then find the address outside instance_id's ports and drop
// the association from state — on every refresh, for ever.
func TestCreate_RefusesAPortThatIsNotOnTheInstance(t *testing.T) {
	var associateCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
			writeInstance(w, "port-1", "port-2")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/associate":
			associateCalls.Add(1)
			writeAccepted(w, "op-1")
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.CreateResponse
	resp.State = tfsdk.State{Schema: s}
	r.Create(context.Background(), resource.CreateRequest{
		Plan: tfsdk.Plan{Schema: s, Raw: stateValue(t, s, tftypes.UnknownValue, "pip-1", "inst-1", "port-of-something-else")},
	}, &resp)

	if !resp.Diagnostics.HasError() {
		t.Fatal("a port outside the instance must be refused")
	}
	if associateCalls.Load() != 0 {
		t.Error("nothing may be sent for a port the instance does not have")
	}
}

// --- Delete: what "no port" does and does not prove ---

// TestDelete_DoesNotClaimSuccessForAGatewayHeldAddress.
//
// `portId` is not evidence of no attachment. An address re-purposed as a VPC's
// outbound source has no port either — the platform ships `attachment` for
// exactly this reason — so reading an empty portId as "already detached, we're
// done" reports a clean destroy for an address a whole VPC egresses through.
func TestDelete_DoesNotClaimSuccessForAGatewayHeldAddress(t *testing.T) {
	var disassociateCalls atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			writeMe(w)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
			_ = json.NewEncoder(w).Encode(apiPublicIP{
				ID:         "pip-1",
				Attachment: &apiPublicIPAttachment{Kind: public_ip.AttachmentKindGateway, VPCID: "vpc-7"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/disassociate":
			disassociateCalls.Add(1)
			writeAccepted(w, "op-d")
		default:
			writeNotFound(w)
		}
	}))
	defer server.Close()

	r := NewResource()
	configureResource(t, r, newTestClient(t, server))
	s := getSchema(t).Schema

	var resp resource.DeleteResponse
	resp.State = tfsdk.State{Schema: s}
	r.Delete(context.Background(), resource.DeleteRequest{
		State: tfsdk.State{Schema: s, Raw: stateValue(t, s, "pip-1/inst-1", "pip-1", "inst-1", "port-1")},
	}, &resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("the association is gone either way, so Delete must succeed: %v", resp.Diagnostics.Errors())
	}
	if disassociateCalls.Load() != 0 {
		t.Error("Delete must not detach a VPC's outbound source address")
	}
	if resp.Diagnostics.WarningsCount() == 0 {
		t.Fatal("a destroy that reports success while the address carries a VPC's egress has to say so")
	}
	if detail := resp.Diagnostics.Warnings()[0].Detail(); !strings.Contains(detail, "vpc-7") {
		t.Errorf("the warning must name what actually holds the address, got: %s", detail)
	}
}

// TestDelete_WithoutARecordedPortEstablishesOwnership covers the state row that
// does not say which port it bound — an imported row destroyed before its first
// refresh. That is the one branch where ownership is UNKNOWN, so it is the last
// one that may fall through to a blind disassociate.
func TestDelete_WithoutARecordedPortEstablishesOwnership(t *testing.T) {
	cases := []struct {
		name          string
		boundPort     string
		instancePorts []string
		instanceGone  bool
		wantDetach    bool
		wantWarning   bool
	}{
		{
			name:          "the binding is one of the instance's ports",
			boundPort:     "port-2",
			instancePorts: []string{"port-1", "port-2"},
			wantDetach:    true,
		},
		{
			name:          "the binding belongs to something else",
			boundPort:     "port-elsewhere",
			instancePorts: []string{"port-1"},
			wantWarning:   true,
		},
		{
			name:         "the instance is gone, so it holds nothing",
			boundPort:    "port-elsewhere",
			instanceGone: true,
			wantWarning:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var disassociateCalls atomic.Int32

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
					writeMe(w)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1":
					_ = json.NewEncoder(w).Encode(apiPublicIP{ID: "pip-1", PortID: tc.boundPort})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/instances/inst-1":
					if tc.instanceGone {
						writeNotFound(w)
						return
					}
					writeInstance(w, tc.instancePorts...)
				case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-1/public-ips/pip-1/disassociate":
					disassociateCalls.Add(1)
					writeAccepted(w, "op-d")
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-1/operations/op-d":
					_ = json.NewEncoder(w).Encode(client.Operation{OperationID: "op-d", Status: client.OperationStatusCompleted})
				default:
					writeNotFound(w)
				}
			}))
			defer server.Close()

			r := NewResource()
			configureResource(t, r, newTestClient(t, server))
			s := getSchema(t).Schema

			var resp resource.DeleteResponse
			resp.State = tfsdk.State{Schema: s}
			r.Delete(context.Background(), resource.DeleteRequest{
				// port_id null: the shape ImportState leaves behind.
				State: tfsdk.State{Schema: s, Raw: stateValue(t, s, "pip-1/inst-1", "pip-1", "inst-1", nil)},
			}, &resp)

			if resp.Diagnostics.HasError() {
				t.Fatalf("Delete failed: %v", resp.Diagnostics.Errors())
			}
			if detached := disassociateCalls.Load() > 0; detached != tc.wantDetach {
				t.Errorf("expected detach=%v, got detach=%v", tc.wantDetach, detached)
			}
			if warned := resp.Diagnostics.WarningsCount() > 0; warned != tc.wantWarning {
				t.Errorf("expected warning=%v, got %v", tc.wantWarning, resp.Diagnostics.Warnings())
			}
		})
	}
}
