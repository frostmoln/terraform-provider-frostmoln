package webserver_domain

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	resSchema "github.com/hashicorp/terraform-plugin-framework/resource/schema"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// Class A (2026-09 convergence audit): a webserver domain binding row is
// stored and never consumed — no vhost is rendered from it and no
// certificate path exists — so tls_enabled = true records a TLS promise the
// platform does not serve, on create or update alike. The plan-time
// validator (internal/unenacted.BoolTrue) refuses `true`; the Create belt
// below stops an apply-resolved value from reaching the API. The rest of
// the resource (the create/delete-only row) is out of the refusal's scope:
// the binding itself plans fine, and its inertness is recorded in the
// scopedecl declaration instead.

func TestSchemaRefusesTLSTrueAtPlanTime(t *testing.T) {
	var schemaResp resource.SchemaResponse
	NewResource().Schema(context.Background(), resource.SchemaRequest{}, &schemaResp)
	attr, ok := schemaResp.Schema.Attributes["tls_enabled"].(resSchema.BoolAttribute)
	if !ok {
		t.Fatal("expected a Bool attribute tls_enabled")
	}
	if len(attr.Validators) != 1 {
		t.Fatalf("expected the unenacted refusal validator on tls_enabled, got %d validators", len(attr.Validators))
	}

	resp := &validator.BoolResponse{}
	attr.Validators[0].ValidateBool(context.Background(), validator.BoolRequest{
		Path:        path.Root("tls_enabled"),
		ConfigValue: types.BoolValue(true),
	}, resp)
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected tls_enabled = true to be refused at plan validation")
	}
	if got, want := resp.Diagnostics.Errors()[0].Summary(), tlsEnabledRefusalTitle; got != want {
		t.Errorf("expected summary %q, got %q", want, got)
	}
	for _, want := range []string{"never consumes it", "application_gateway"} {
		if !strings.Contains(resp.Diagnostics.Errors()[0].Detail(), want) {
			t.Errorf("expected the diagnostic to name %q, got %q", want, resp.Diagnostics.Errors()[0].Detail())
		}
	}

	// `false` matches the platform's actual behaviour and must not be fought.
	resp = &validator.BoolResponse{}
	attr.Validators[0].ValidateBool(context.Background(), validator.BoolRequest{
		Path:        path.Root("tls_enabled"),
		ConfigValue: types.BoolValue(false),
	}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("expected tls_enabled = false to pass, got %v", resp.Diagnostics.Errors())
	}
}

func TestCreateRefusesTLSTrue(t *testing.T) {
	requested := atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		requested.Store(true)
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c := client.NewClient(server.URL, "test-key", client.WithHTTPClient(server.Client())) // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	r := &webserverDomainResource{client: c}

	plan := buildDomainPlan(t, webserverDomainModel{
		InstanceID: types.StringValue("inst-1"),
		DomainName: types.StringValue("example.com"),
		TLSEnabled: types.BoolValue(true),
		IsDefault:  types.BoolValue(false),
	})

	createResp := resource.CreateResponse{State: emptyDomainState(t)}
	r.Create(context.Background(), resource.CreateRequest{Plan: plan}, &createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected the create with tls_enabled = true to be refused")
	}
	if got, want := createResp.Diagnostics.Errors()[0].Summary(), tlsEnabledRefusalTitle; got != want {
		t.Errorf("expected summary %q, got %q", want, got)
	}
	if requested.Load() {
		t.Error("the refusal must fire before any API request")
	}
}
