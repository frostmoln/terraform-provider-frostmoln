package appgw_listener

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                   = &listenerResource{}
	_ resource.ResourceWithImportState    = &listenerResource{}
	_ resource.ResourceWithConfigure      = &listenerResource{}
	_ resource.ResourceWithValidateConfig = &listenerResource{}
)

type listenerResource struct {
	client *client.Client
}

// NewResource returns a new Application Gateway listener resource factory.
func NewResource() resource.Resource {
	return &listenerResource{}
}

func (r *listenerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_listener"
}

// replaceOnChange is every listener attribute's plan modifier set.
//
// 🔴 THE LISTENER API HAS NO UPDATE. The server registers POST, GET and DELETE
// on listeners and nothing else, so there is no in-place change to make and
// every attribute forces a replacement. Marking one of these mutable would
// produce a plan Terraform cannot execute.
func (r *listenerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceInt := []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	replaceList := []planmodifier.List{listplanmodifier.RequiresReplace()}
	replaceBool := []planmodifier.Bool{boolplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		Description: "Manages a listener on a Frostmoln Application Gateway: one bound port, with its " +
			"TLS settings and its network firewall.\n\n" +
			"Routes hang off a listener rather than off the gateway.\n\n" +
			"The listener API has no update operation, so **every** attribute forces a new resource.\n\n" +
			"A listener is authored, not live: it starts serving on the gateway's next configuration apply.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The unique identifier of the listener.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway this listener belongs to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"name": schema.StringAttribute{
				Description:   "The name of the listener.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"protocol": schema.StringAttribute{
				Description: "The listener protocol: `http` or `https`.\n\n" +
					"`tcp` is reserved for a future listener type and is refused today.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.OneOf("http", "https")},
				PlanModifiers: replaceStr,
			},
			"port": schema.Int64Attribute{
				Description:   "The port to bind.",
				Required:      true,
				Validators:    []validator.Int64{int64validator.Between(1, 65535)},
				PlanModifiers: replaceInt,
			},
			"default_certificate_id": schema.StringAttribute{
				Description:   "The certificate served when no SNI matches. `https` listeners only.",
				Optional:      true,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: replaceStr,
			},
			"sni_certificate_ids": schema.ListAttribute{
				Description: "Additional certificates selected by SNI. `https` listeners only.",
				Optional:    true,
				ElementType: types.StringType,
				// SizeAtLeast(1): the server omits an empty collection entirely, so
				// `= []` plans as empty and applies as null — a hard "inconsistent
				// result after apply". Omit the attribute instead of writing an
				// empty one; they mean the same thing here.
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: replaceList,
			},
			"tls_min_version": schema.StringAttribute{
				Description:   "The minimum TLS version accepted: `1.2` or `1.3`.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("1.2", "1.3")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"tls_cipher_profile": schema.StringAttribute{
				Description:   "The cipher profile: `modern` or `intermediate`.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("modern", "intermediate")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"redirect_to_https": schema.BoolAttribute{
				Description:   "Redirect requests to the https listener instead of serving them.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: replaceBool,
			},
			"allowed_cidrs": schema.ListAttribute{
				Description: "Source CIDRs allowed to reach this listener. Omit to allow all sources.",
				Optional:    true,
				ElementType: types.StringType,
				// SizeAtLeast(1): the server omits an empty collection entirely, so
				// `= []` plans as empty and applies as null — a hard "inconsistent
				// result after apply". Omit the attribute instead of writing an
				// empty one; they mean the same thing here.
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: replaceList,
			},
			"denied_cidrs": schema.ListAttribute{
				Description: "Source CIDRs refused by this listener.",
				Optional:    true,
				ElementType: types.StringType,
				// SizeAtLeast(1): the server omits an empty collection entirely, so
				// `= []` plans as empty and applies as null — a hard "inconsistent
				// result after apply". Omit the attribute instead of writing an
				// empty one; they mean the same thing here.
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: replaceList,
			},
			"geo_block_mode": schema.StringAttribute{
				Description: "Country filtering: `off`, `allow` (only `geo_countries`) or `deny` " +
					"(everything except `geo_countries`).",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("off", "allow", "deny")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"geo_countries": schema.ListAttribute{
				Description:   "ISO 3166-1 alpha-2 country codes. Required when `geo_block_mode` is `allow` or `deny`.",
				Optional:      true,
				ElementType:   types.StringType,
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: replaceList,
			},
			"rate_limit_rps": schema.Int64Attribute{
				Description: "Requests per second permitted per source address.",
				Optional:    true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.Between(1, 1000000)},
				PlanModifiers: replaceInt,
			},
			"rate_limit_burst": schema.Int64Attribute{
				Description: "Burst allowance above `rate_limit_rps`.",
				Optional:    true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.Between(1, 1000000)},
				PlanModifiers: replaceInt,
			},
			"max_connections": schema.Int64Attribute{
				Description: "Maximum concurrent connections on this listener.",
				Optional:    true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.Between(1, 1000000)},
				PlanModifiers: replaceInt,
			},
			"waf_policy_id": schema.StringAttribute{
				Description: "The WAF policy applied to this listener, if any — an `overlay`-scoped " +
					"policy. Read-only here: attach one with " +
					"`frostmoln_appgw_waf_policy_attachment`, which is where the attachment's " +
					"lifecycle lives.",
				Computed: true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the listener is serving.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "The creation timestamp.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "The last update timestamp.",
				Computed:    true,
			},
		},
	}
}

// ValidateConfig mirrors the server's two cross-field rules at plan time, so a
// mistake is caught before anything is created rather than as a 400 halfway
// through an apply that has already built the gateway.
func (r *listenerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg ListenerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !cfg.Protocol.IsUnknown() && cfg.Protocol.ValueString() != "https" {
		hasDefault := !cfg.DefaultCertificateID.IsNull() && !cfg.DefaultCertificateID.IsUnknown()
		// Element COUNT, not null-ness: an empty list is not "certificates are
		// set", and the server checks len() > 0 too.
		hasSNI := !cfg.SNICertificateIDs.IsNull() && !cfg.SNICertificateIDs.IsUnknown() &&
			len(cfg.SNICertificateIDs.Elements()) > 0
		if hasDefault || hasSNI {
			resp.Diagnostics.AddAttributeError(path.Root("default_certificate_id"),
				"Certificates Require protocol = \"https\"",
				"An http listener terminates no TLS, so a certificate on it would never be served. "+
					"Set protocol = \"https\", or move the certificate to the https listener.")
		}
	}

	if !cfg.GeoBlockMode.IsUnknown() {
		mode := cfg.GeoBlockMode.ValueString()
		if mode == "allow" || mode == "deny" {
			if cfg.GeoCountries.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root("geo_countries"),
					fmt.Sprintf("geo_countries Is Required With geo_block_mode = %q", mode),
					fmt.Sprintf("geo_block_mode = %q needs the list of countries it applies to. "+
						"With an empty list the filter would %s every request.", mode,
						map[string]string{"allow": "refuse", "deny": "allow"}[mode]))
			}
		}
	}
}

func (r *listenerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *listenerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ListenerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	gwID := plan.GatewayID.ValueString()
	apiResp, err := r.client.Post(ctx,
		r.client.TenantPath(fmt.Sprintf("/application-gateways/%s/listeners", gwID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Listener", err.Error())
		return
	}

	// 202 here carries the listener itself, not an operation envelope: the row
	// is written synchronously and 202 says only that it is not yet SERVING.
	listener, err := client.ParseResponse[apiListener](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
		return
	}
	plan.fromAPI(ctx, listener, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *listenerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ListenerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/listeners/%s", state.GatewayID.ValueString(), state.ID.ValueString(),
	)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Listener", err.Error())
		return
	}
	listener, err := client.ParseResponse[apiListener](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
		return
	}
	state.fromAPI(ctx, listener, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update cannot be reached: every attribute carries RequiresReplace. It exists
// because the Resource interface requires it, and it reports rather than
// silently doing nothing -- a no-op Update is how a provider tells Terraform a
// change succeeded when it did not happen.
func (r *listenerResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Listeners Cannot Be Updated In Place",
		"The Application Gateway API has no listener update operation, so every attribute of this "+
			"resource forces a replacement. Reaching this code means an attribute was added to the "+
			"schema without RequiresReplace.")
}

func (r *listenerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ListenerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/listeners/%s", state.GatewayID.ValueString(), state.ID.ValueString(),
	)))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete Listener", err.Error())
	}
}

func (r *listenerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "listener_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
