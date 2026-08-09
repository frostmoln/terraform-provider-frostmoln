package public_ip

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	publicipresource "go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/public_ip"
)

func dsSchema(t *testing.T) schema.Schema {
	t.Helper()
	d := NewDataSource()
	resp := &datasource.SchemaResponse{}
	d.Schema(context.Background(), datasource.SchemaRequest{}, resp)
	return resp.Schema
}

func dsAttachmentType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"kind":        tftypes.String,
			"resource_id": tftypes.String,
			"vpc_id":      tftypes.String,
		},
	}
}

func dsObjectType() tftypes.Object {
	return tftypes.Object{
		AttributeTypes: map[string]tftypes.Type{
			"id":         tftypes.String,
			"address":    tftypes.String,
			"status":     tftypes.String,
			"private_ip": tftypes.String,
			"attachment": dsAttachmentType(),
			"tags":       tftypes.Map{ElementType: tftypes.String},
			"created_at": tftypes.String,
		},
	}
}

// dsConfig builds a configuration with at most one selector set. "" is a set
// but EMPTY selector, which is a case of its own — see the empty-selector test.
func dsConfig(id, address *string) tftypes.Value {
	str := func(p *string) tftypes.Value {
		if p == nil {
			return tftypes.NewValue(tftypes.String, nil)
		}
		return tftypes.NewValue(tftypes.String, *p)
	}
	null := tftypes.NewValue(tftypes.String, nil)
	return tftypes.NewValue(dsObjectType(), map[string]tftypes.Value{
		"id":         str(id),
		"address":    str(address),
		"status":     null,
		"private_ip": null,
		"attachment": tftypes.NewValue(dsAttachmentType(), nil),
		"tags":       tftypes.NewValue(tftypes.Map{ElementType: tftypes.String}, nil),
		"created_at": null,
	})
}

func ptr(s string) *string { return &s }

func configuredDataSource(t *testing.T, serverURL string) datasource.DataSource {
	t.Helper()
	c := client.NewClient(serverURL, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-123")
	d := NewDataSource()
	d.(datasource.DataSourceWithConfigure).Configure(
		context.Background(),
		datasource.ConfigureRequest{ProviderData: c},
		&datasource.ConfigureResponse{},
	)
	return d
}

func readDataSource(t *testing.T, serverURL string, config tftypes.Value) *datasource.ReadResponse {
	t.Helper()
	s := dsSchema(t)
	d := configuredDataSource(t, serverURL)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: s}}
	d.Read(context.Background(), datasource.ReadRequest{
		Config: tfsdk.Config{Schema: s, Raw: config},
	}, resp)
	return resp
}

func dsDiagText(resp *datasource.ReadResponse) string {
	var b strings.Builder
	for _, d := range resp.Diagnostics.Errors() {
		b.WriteString(d.Summary())
		b.WriteString("\n")
		b.WriteString(d.Detail())
		b.WriteString("\n")
	}
	return b.String()
}

func TestPublicIPDataSourceMetadata(t *testing.T) {
	d := NewDataSource()
	resp := &datasource.MetadataResponse{}
	d.Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "frostmoln"}, resp)
	if resp.TypeName != "frostmoln_public_ip" {
		t.Errorf("unexpected type name %s", resp.TypeName)
	}
}

// TestAttachmentShapeMatchesTheResource keeps the data source's `attachment`
// object identical to the resource's. They are separate declarations (no data
// source in this provider imports a resource package), so nothing but this
// stops them drifting — and a practitioner who moves an expression from
// `frostmoln_public_ip` the resource to `frostmoln_public_ip` the data source
// would then have to rewrite it for no reason they could discover.
func TestAttachmentShapeMatchesTheResource(t *testing.T) {
	if !reflect.DeepEqual(attachmentAttrTypes, publicipresource.AttachmentAttrTypes) {
		t.Errorf("the data source's attachment object has drifted from the resource's:\n data source: %v\n resource:    %v",
			attachmentAttrTypes, publicipresource.AttachmentAttrTypes)
	}
}

// TestPublicIPDataSourceLookupByAddress is the point of this data source: a
// configuration can name an address that already exists — one already in a
// partner's allow-list or published in DNS — and hand it to a gateway
// without importing it into Terraform's management.
func TestPublicIPDataSourceLookupByAddress(t *testing.T) {
	var gotPath, gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = w.Write([]byte(`{"publicIps":[{"id":"pip-1","publicIpAddress":"203.0.113.10",
			"status":"in_use","createdAt":"2026-01-01T00:00:00Z",
			"attachment":{"kind":"gateway","resourceId":"gw-1","vpcId":"vpc-1"}}],"totalCount":1}`))
	}))
	defer server.Close()

	resp := readDataSource(t, server.URL, dsConfig(nil, ptr("203.0.113.10")))
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if gotPath != "/v1/tenants/t-123/public-ips" {
		t.Errorf("unexpected path %s", gotPath)
	}
	if !strings.Contains(gotQuery, "publicIpAddress=203.0.113.10") {
		t.Errorf("the lookup must filter server-side by the address, got query %q", gotQuery)
	}

	var state publicIPModel
	resp.State.Get(context.Background(), &state)
	if state.ID.ValueString() != "pip-1" {
		t.Errorf("expected pip-1, got %v", state.ID)
	}
	kind := state.Attachment.Attributes()["kind"].(types.String).ValueString()
	if kind != "gateway" {
		t.Errorf("the gateway attachment must be surfaced, got kind %q", kind)
	}
	if state.Attachment.Attributes()["vpc_id"].(types.String).ValueString() != "vpc-1" {
		t.Errorf("the attached VPC must be surfaced, got %v", state.Attachment)
	}
}

// TestPublicIPDataSourceRechecksTheAddress: the filter is the server's, but the
// promise is an EXACT address. A server that widened or ignored the filter must
// not resolve this to a NEIGHBOURING address — the practitioner would publish
// it, or pin a VPC's outbound path to it.
func TestPublicIPDataSourceRechecksTheAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"publicIps":[{"id":"pip-9","publicIpAddress":"203.0.113.99",
			"status":"available","createdAt":"2026-01-01T00:00:00Z"}],"totalCount":1}`))
	}))
	defer server.Close()

	resp := readDataSource(t, server.URL, dsConfig(nil, ptr("203.0.113.10")))
	if !resp.Diagnostics.HasError() {
		t.Fatal("an unrelated address must not be accepted as the answer")
	}
	if !strings.Contains(dsDiagText(resp), "203.0.113.10") {
		t.Errorf("the diagnostic must name the address that was asked for:\n%s", dsDiagText(resp))
	}
}

func TestPublicIPDataSourceRefusesMultipleMatches(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"publicIps":[
			{"id":"pip-1","publicIpAddress":"203.0.113.10","status":"available","createdAt":"2026-01-01T00:00:00Z"},
			{"id":"pip-2","publicIpAddress":"203.0.113.10","status":"available","createdAt":"2026-01-01T00:00:00Z"}
		],"totalCount":2}`))
	}))
	defer server.Close()

	resp := readDataSource(t, server.URL, dsConfig(nil, ptr("203.0.113.10")))
	if !resp.Diagnostics.HasError() {
		t.Fatal("an ambiguous result must be refused, never chosen from")
	}
}

func TestPublicIPDataSourceLookupByID(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"id":"pip-1","publicIpAddress":"203.0.113.10","status":"in_use",
			"fixedIpAddress":"10.0.0.5","portId":"port-1","createdAt":"2026-01-01T00:00:00Z",
			"tags":{"env":"prod"}}`))
	}))
	defer server.Close()

	resp := readDataSource(t, server.URL, dsConfig(ptr("pip-1"), nil))
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}
	if gotPath != "/v1/tenants/t-123/public-ips/pip-1" {
		t.Errorf("unexpected path %s", gotPath)
	}

	var state publicIPModel
	resp.State.Get(context.Background(), &state)
	if state.Address.ValueString() != "203.0.113.10" || state.PrivateIP.ValueString() != "10.0.0.5" {
		t.Errorf("unexpected state %+v", state)
	}
	// No attachment object on the wire, but a portId: positive evidence of a
	// port, which is what portId meant before the object existed. Never null.
	if state.Attachment.IsNull() {
		t.Fatal("attachment must never be null")
	}
	if state.Attachment.Attributes()["kind"].(types.String).ValueString() != "port" {
		t.Errorf("a public IP with a port must report kind \"port\", got %v", state.Attachment)
	}
}

// TestPublicIPDataSourceAbsentAttachmentIsUnknownNotNone mirrors the resource's
// rule, and it is a safety rule rather than a cosmetic one.
//
// No attachment object AND no port is not evidence that the address is free:
// an address serving a VPC's outbound path has no port either. A platform rolled back
// below the version that reports attachments still has gateway-attached
// addresses and answers for them exactly like this. Reporting "none" would tell
// a practitioner an address is idle at the one moment giving it away cannot be
// undone.
func TestPublicIPDataSourceAbsentAttachmentIsUnknownNotNone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"id":"pip-1","publicIpAddress":"203.0.113.10","status":"available",
			"createdAt":"2026-01-01T00:00:00Z"}`))
	}))
	defer server.Close()

	resp := readDataSource(t, server.URL, dsConfig(ptr("pip-1"), nil))
	if resp.Diagnostics.HasError() {
		t.Fatalf("unexpected errors: %v", resp.Diagnostics)
	}

	var state publicIPModel
	resp.State.Get(context.Background(), &state)
	kind := state.Attachment.Attributes()["kind"].(types.String).ValueString()
	if kind != "unknown" {
		t.Errorf("an unreported attachment must be \"unknown\", never \"none\" — \"none\" reassures a "+
			"practitioner that an address is free when the platform never said so; got %q", kind)
	}
}

// TestAttachmentKindUnknownMatchesTheResource: the synthesized kind must be
// spelled identically on both surfaces, or a configuration that branches on it
// silently stops matching when it is moved between them.
func TestAttachmentKindUnknownMatchesTheResource(t *testing.T) {
	if attachmentKindUnknown != publicipresource.AttachmentKindUnknown {
		t.Errorf("the data source says %q, the resource says %q",
			attachmentKindUnknown, publicipresource.AttachmentKindUnknown)
	}
}

// TestPublicIPDataSourceRefusesNoOrBothSelectors: with no selector the lookup
// would widen to the tenant-wide list and return an arbitrary address; with
// both there is no answer that is not a guess. An EMPTY string counts as "not
// set" for the first case, because an interpolation resolving to "" is exactly
// how the unfiltered request would get made.
func TestPublicIPDataSourceRefusesNoOrBothSelectors(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"publicIps":[],"totalCount":0}`))
	}))
	defer server.Close()

	for _, tc := range []struct {
		name    string
		id      *string
		address *string
	}{
		{"neither", nil, nil},
		{"empty id", ptr(""), nil},
		{"empty address", nil, ptr("")},
		{"both", ptr("pip-1"), ptr("203.0.113.10")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := readDataSource(t, server.URL, dsConfig(tc.id, tc.address))
			if !resp.Diagnostics.HasError() {
				t.Fatal("expected the lookup to be refused")
			}
		})
	}

	if calls != 0 {
		t.Errorf("a refused lookup must send no request at all, saw %d", calls)
	}
}

func TestPublicIPDataSourceNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"publicIps":[],"totalCount":0}`))
	}))
	defer server.Close()

	resp := readDataSource(t, server.URL, dsConfig(nil, ptr("203.0.113.10")))
	if !resp.Diagnostics.HasError() {
		t.Fatal("expected an error for an address this tenant does not own")
	}
	// The same empty answer covers "does not exist" and "belongs to someone
	// else", so the diagnostic must not assert the first.
	if !strings.Contains(dsDiagText(resp), "another tenant") {
		t.Errorf("the diagnostic must name the other reason for an empty result:\n%s", dsDiagText(resp))
	}
}

func TestPublicIPDataSourceConfigureWrongType(t *testing.T) {
	d := NewDataSource()
	resp := &datasource.ConfigureResponse{}
	d.(datasource.DataSourceWithConfigure).Configure(context.Background(),
		datasource.ConfigureRequest{ProviderData: "bad"}, resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected an error for the wrong provider data type")
	}
}

func TestPublicIPDataSourceConfigureNilProviderData(t *testing.T) {
	d := NewDataSource()
	resp := &datasource.ConfigureResponse{}
	d.(datasource.DataSourceWithConfigure).Configure(context.Background(),
		datasource.ConfigureRequest{ProviderData: nil}, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("unexpected errors: %v", resp.Diagnostics)
	}
}
