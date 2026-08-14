// Package apikeyscopes implements the frostmoln_api_key_scopes Terraform data
// source: the server-owned catalog of scopes an API key or workload identity
// can be granted.
package apikeyscopes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &apiKeyScopesDataSource{}

// NewDataSource returns a new frostmoln_api_key_scopes data source factory.
func NewDataSource() datasource.DataSource {
	return &apiKeyScopesDataSource{}
}

type apiKeyScopesDataSource struct {
	client *client.Client
}

// apiKeyScopesModel is the Terraform state model for the scope catalog.
type apiKeyScopesModel struct {
	Scopes types.List `tfsdk:"scopes"`
}

// scopeItemModel represents a single grantable scope.
type scopeItemModel struct {
	Scope       types.String `tfsdk:"scope"`
	Service     types.String `tfsdk:"service"`
	Description types.String `tfsdk:"description"`
}

// apiScope is the API representation of one catalog scope.
type apiScope struct {
	Scope       string `json:"scope"`
	Description string `json:"description"`
}

// apiScopeList is the API response for the scope catalog.
//
// The response also carries an `operations` array (the ADR-0102
// `service:resource:action` access-policy grammar). It is deliberately NOT
// decoded: identity ACCEPTS an operation in a key's scopes list, but no service
// ENFORCES one there — servicekit's HasScope matches exactly or on a trailing
// ":*", and the services require the 2-part `<service>:read`/`<service>:write`.
// A key built from operations plans, applies, and is then denied on every call.
// Operations belong to frostmoln_iam_policy_document.
type apiScopeList struct {
	Scopes []apiScope `json:"scopes"`
}

var scopeItemAttrTypes = map[string]attr.Type{
	"scope":       types.StringType,
	"service":     types.StringType,
	"description": types.StringType,
}

func (d *apiKeyScopesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key_scopes"
}

func (d *apiKeyScopesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "List the scopes an API key (frostmoln_api_key) or workload identity (frostmoln_workload_identity_binding) can be granted, with a description of what each allows. " +
			"The catalog is server-owned, so it needs no provider release to change. " +
			"A scope is `<service>:<action>`; grant `:read` and `:write`, which are what the services enforce. " +
			"The global \"*\" wildcard is excluded: keys are least-privilege and the API rejects it. Per-service wildcards such as `compute:*` are listed and are accepted on an API key, but a workload identity rejects every `:*` — filter those out when feeding a binding. " +
			"For per-resource targets, constraints or explicit denies, use an access policy (frostmoln_iam_policy_document) rather than a finer scope string.",
		Attributes: map[string]schema.Attribute{
			"scopes": schema.ListNestedAttribute{
				Description: "The grantable scopes, in catalog order.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"scope": schema.StringAttribute{
							Description: "The scope string to put in a `scopes` list (e.g. \"compute:read\").",
							Computed:    true,
						},
						"service": schema.StringAttribute{
							Description: "The service the scope belongs to — the part before the first colon (e.g. \"compute\").",
							Computed:    true,
						},
						"description": schema.StringAttribute{
							Description: "What the scope allows.",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *apiKeyScopesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	d.client = c
}

func (d *apiKeyScopesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state apiKeyScopesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, "/v1/scopes", nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list API key scopes", err.Error())
		return
	}

	var list apiScopeList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to parse API key scopes response", err.Error())
		return
	}

	items := make([]scopeItemModel, 0, len(list.Scopes))
	for _, s := range list.Scopes {
		// The catalog lists the global "*" (it is meaningful when CHECKING a
		// scope) but the API rejects it when granting one, so surfacing it here
		// would only produce a plan that fails on apply.
		if s.Scope == "*" {
			continue
		}
		service, _, found := strings.Cut(s.Scope, ":")
		if !found {
			service = s.Scope
		}
		items = append(items, scopeItemModel{
			Scope:       types.StringValue(s.Scope),
			Service:     types.StringValue(service),
			Description: types.StringValue(s.Description),
		})
	}

	scopesList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: scopeItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Scopes = scopesList
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
