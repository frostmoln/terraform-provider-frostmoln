// Package secret implements the frostmoln_secret Terraform data source.
package secret

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &secretDataSource{}

// NewDataSource returns a new frostmoln_secret data source factory.
func NewDataSource() datasource.DataSource {
	return &secretDataSource{}
}

type secretDataSource struct {
	client *client.Client
}

// secretModel is the Terraform state model for a secret data source.
type secretModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	ContentType        types.String `tfsdk:"content_type"`
	Tags               types.Map    `tfsdk:"tags"`
	MaxVersions        types.Int64  `tfsdk:"max_versions"`
	RecoveryWindowDays types.Int64  `tfsdk:"recovery_window_days"`
	CurrentVersion     types.Int64  `tfsdk:"current_version"`
	Status             types.String `tfsdk:"status"`
	CreatedAt          types.String `tfsdk:"created_at"`
	UpdatedAt          types.String `tfsdk:"updated_at"`
}

// apiSecret is the API representation of a secret.
type apiSecret struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Description        string            `json:"description,omitempty"`
	ContentType        string            `json:"contentType"`
	Tags               map[string]string `json:"tags,omitempty"`
	MaxVersions        int               `json:"maxVersions"`
	RecoveryWindowDays int               `json:"recoveryWindowDays"`
	CurrentVersion     int               `json:"currentVersion"`
	Status             string            `json:"status"`
	CreatedAt          string            `json:"createdAt"`
	UpdatedAt          string            `json:"updatedAt,omitempty"`
}

func (d *secretDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_secret"
}

func (d *secretDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a secret by ID. Does not expose the secret value; use the frostmoln_secret resource for that.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the secret.",
				Required:    true,
			},
			"name": schema.StringAttribute{
				Description: "The name of the secret.",
				Computed:    true,
			},
			"description": schema.StringAttribute{
				Description: "A description of the secret.",
				Computed:    true,
			},
			"content_type": schema.StringAttribute{
				Description: "The content type of the secret value.",
				Computed:    true,
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the secret.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"max_versions": schema.Int64Attribute{
				Description: "The maximum number of versions retained.",
				Computed:    true,
			},
			"recovery_window_days": schema.Int64Attribute{
				Description: "The number of days a deleted secret is retained before permanent removal.",
				Computed:    true,
			},
			"current_version": schema.Int64Attribute{
				Description: "The current version number of the secret.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the secret.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the secret was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the secret was last updated.",
				Computed:    true,
			},
		},
	}
}

func (d *secretDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *secretDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state secretModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/secrets/"+state.ID.ValueString()), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read secret", err.Error())
		return
	}

	var s apiSecret
	if err := json.Unmarshal(apiResp.Body, &s); err != nil {
		resp.Diagnostics.AddError("Failed to parse secret response", err.Error())
		return
	}

	state.ID = types.StringValue(s.ID)
	state.Name = types.StringValue(s.Name)
	state.ContentType = types.StringValue(s.ContentType)
	state.MaxVersions = types.Int64Value(int64(s.MaxVersions))
	state.RecoveryWindowDays = types.Int64Value(int64(s.RecoveryWindowDays))
	state.CurrentVersion = types.Int64Value(int64(s.CurrentVersion))
	state.Status = types.StringValue(s.Status)
	state.CreatedAt = types.StringValue(s.CreatedAt)

	if s.Description != "" {
		state.Description = types.StringValue(s.Description)
	} else {
		state.Description = types.StringNull()
	}

	if s.UpdatedAt != "" {
		state.UpdatedAt = types.StringValue(s.UpdatedAt)
	} else {
		state.UpdatedAt = types.StringNull()
	}

	if len(s.Tags) > 0 {
		tagsMap, d := types.MapValueFrom(ctx, types.StringType, s.Tags)
		resp.Diagnostics.Append(d...)
		state.Tags = tagsMap
	} else {
		state.Tags = types.MapNull(types.StringType)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
