// Package appgw_certificate implements the frostmoln_appgw_certificate
// Terraform resource.
package appgw_certificate

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/writeonly"
)

var (
	_ resource.Resource                     = &certificateResource{}
	_ resource.ResourceWithImportState      = &certificateResource{}
	_ resource.ResourceWithConfigure        = &certificateResource{}
	_ resource.ResourceWithConfigValidators = &certificateResource{}
)

// CertificateModel is the Terraform state model for a gateway certificate.
type CertificateModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	Name      types.String `tfsdk:"name"`

	ChainPEM           types.String `tfsdk:"chain_pem"`
	PrivateKeyPEM      types.String `tfsdk:"private_key_pem"`
	PrivateKeyPEMWO    types.String `tfsdk:"private_key_pem_wo"`
	PrivateKeyPEMWOVer types.String `tfsdk:"private_key_pem_wo_version"`

	Source            types.String `tfsdk:"source"`
	Status            types.String `tfsdk:"status"`
	CommonName        types.String `tfsdk:"common_name"`
	SANs              types.List   `tfsdk:"sans"`
	Issuer            types.String `tfsdk:"issuer"`
	SerialNumber      types.String `tfsdk:"serial_number"`
	FingerprintSHA256 types.String `tfsdk:"fingerprint_sha256"`
	NotBefore         types.String `tfsdk:"not_before"`
	NotAfter          types.String `tfsdk:"not_after"`
	AutoRenew         types.Bool   `tfsdk:"auto_renew"`
	DNSZoneID         types.String `tfsdk:"dns_zone_id"`
	CreatedAt         types.String `tfsdk:"created_at"`
	UpdatedAt         types.String `tfsdk:"updated_at"`
}

type apiCertificate struct {
	ID        string `json:"id"`
	GatewayID string `json:"gatewayId"`
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Status    string `json:"status"`

	CommonName        string   `json:"commonName"`
	SANs              []string `json:"sans"`
	Issuer            string   `json:"issuer,omitempty"`
	SerialNumber      string   `json:"serialNumber,omitempty"`
	FingerprintSHA256 string   `json:"fingerprintSha256"`
	NotBefore         string   `json:"notBefore"`
	NotAfter          string   `json:"notAfter"`
	ChainPEM          string   `json:"chainPem,omitempty"`

	AutoRenew bool   `json:"autoRenew"`
	DNSZoneID string `json:"dnsZoneId,omitempty"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// apiUploadCertificateRequest is the create body.
//
// 🔴 privateKeyPem, NOT privateKey. The server binds it as
// `json:"privateKeyPem" binding:"required"`, so the wrong name is not a
// silently dropped field -- it is a 400 on every upload.
type apiUploadCertificateRequest struct {
	Name          string `json:"name"`
	ChainPEM      string `json:"chainPem"`
	PrivateKeyPEM string `json:"privateKeyPem"`
}

type certificateResource struct {
	client *client.Client
}

// NewResource returns a new certificate resource factory.
func NewResource() resource.Resource {
	return &certificateResource{}
}

// ConfigValidators keeps private_key_pem and its write-only twin mutually
// exclusive. private_key_pem was Required before private_key_pem_wo existed;
// relaxing it to Optional would otherwise let a config supply neither, and a
// certificate with no key is not a certificate.
func (r *certificateResource) ConfigValidators(_ context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(
			path.MatchRoot("private_key_pem"),
			path.MatchRoot("private_key_pem_wo"),
		),
	}
}

func (r *certificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_certificate"
}

func (r *certificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		Description: "Manages a TLS certificate on a Frostmoln Application Gateway.\n\n" +
			"~> **`private_key_pem` is written to Terraform state.** The provider never adopts a key " +
			"from an API response, so on that attribute your state is the only place it is kept " +
			"across refreshes — which means your state file holds key material and must be treated " +
			"as a secret: a " +
			"remote backend with encryption at rest and restricted access. Keeping the PEM out of " +
			"your `.tf` files does not keep it out of state — a value read from a variable or a " +
			"secret store is persisted exactly like a literal once it is assigned to " +
			"`private_key_pem`. Use `private_key_pem_wo` instead: it carries the same key on apply " +
			"and is never written to the plan or to state. The other way to keep a key out of " +
			"Terraform is not to hand it one — a platform-issued ACME certificate (`source` = " +
			"`acmeDns01`, obtained out of band — this resource only uploads) is attached to a " +
			"listener by ID and never carries key material. See the " +
			"[Secrets in Terraform state](" + docs.StateSecretsGuide + ") guide.\n\n" +
			"The certificate API has no update operation, so every attribute forces a new resource. " +
			"Rotating a certificate therefore creates a new one and destroys the old — attach it to " +
			"the listener via `create_before_destroy` if you cannot take the interruption.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The unique identifier of the certificate.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway this certificate belongs to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"name": schema.StringAttribute{
				Description:   "The name of the certificate.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"chain_pem": schema.StringAttribute{
				Description:   "The PEM certificate chain, leaf first.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"private_key_pem": schema.StringAttribute{
				Description: "The PEM private key for `chain_pem`. The provider preserves it from prior " +
					"state on every refresh and never adopts one from an API response. " + docs.StateSecretNote +
					" Prefer `private_key_pem_wo`, which carries the same key but is never written to " +
					"state; exactly one of the two must be set.",
				Optional:      true,
				Sensitive:     true,
				PlanModifiers: replaceStr,
				Validators: []validator.String{
					stringvalidator.PreferWriteOnlyAttribute(path.MatchRoot("private_key_pem_wo")),
				},
			},
			"private_key_pem_wo": schema.StringAttribute{
				Description: "The PEM private key for `chain_pem`, as a [write-only argument]" +
					"(https://developer.hashicorp.com/terraform/language/resources/ephemeral/write-only): the " +
					"key reaches the provider on apply and is never written to the plan or to state. Requires " +
					"Terraform 1.11 or later. Exactly one of `private_key_pem` or `private_key_pem_wo` must be " +
					"set, and `private_key_pem_wo_version` is required whenever this one is. The key must be " +
					"at least one character — omitting it is not a way to upload a certificate without one. " +
					"Terraform cannot see a write-only value, so it cannot detect a change to the key: " +
					"changing `private_key_pem_wo_version` is what makes the next apply send the current key, " +
					"and it does so by REPLACING the certificate, because the certificate API has no update " +
					"operation. Editing the key without touching the version does nothing.",
				Optional:  true,
				Sensitive: true,
				WriteOnly: true,
				// No plan modifiers: a write-only attribute is null in prior
				// state, plan and final state, so one here would compare null
				// against null and never fire. The version companion carries
				// the replacement.
				Validators: []validator.String{
					// An empty key is never what a practitioner meant — an
					// unset variable, a file() that read nothing — and "" is
					// not null, so it passes the null guard and reaches the
					// wire, where the server refuses it as missing. Refusing
					// at plan time names the actual mistake.
					stringvalidator.LengthAtLeast(1),
					stringvalidator.AlsoRequires(path.MatchRoot("private_key_pem_wo_version")),
				},
			},
			"private_key_pem_wo_version": schema.StringAttribute{
				Description: "Change tracker for `private_key_pem_wo`, required whenever that attribute is " +
					"set. Changing this value REPLACES the certificate and uploads the current " +
					"`private_key_pem_wo` — the same lifecycle a change to `private_key_pem` has, since the " +
					"certificate API has no update operation. Leaving it alone leaves the uploaded " +
					"certificate untouched however much the write-only key changes. Its content is arbitrary " +
					"— a counter or a date is typical — and unlike the key it is stored in state, so do not " +
					"derive it from the key or from anything in it: a digest of the key is printed verbatim " +
					"in `terraform plan` output, CI job logs and PR plan comments, and a digest is an " +
					"offline confirmation oracle — it lets anyone holding it test a guessed key and " +
					"correlate the same key across environments. `terraform import` leaves this unset, so " +
					"the first apply against an imported certificate plans a REPLACEMENT.",
				Optional:      true,
				PlanModifiers: replaceStr,
				Validators: []validator.String{
					stringvalidator.AlsoRequires(path.MatchRoot("private_key_pem_wo")),
				},
			},
			"source": schema.StringAttribute{
				Description: "How the platform obtained the certificate: `uploaded` or `acmeDns01`.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The certificate's lifecycle state.",
				Computed:    true,
			},
			"common_name": schema.StringAttribute{
				Description: "The certificate's common name.",
				Computed:    true,
			},
			"sans": schema.ListAttribute{
				Description: "The certificate's subject alternative names.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"issuer":             schema.StringAttribute{Description: "The issuing CA.", Computed: true},
			"serial_number":      schema.StringAttribute{Description: "The certificate's serial number.", Computed: true},
			"fingerprint_sha256": schema.StringAttribute{Description: "The SHA-256 fingerprint.", Computed: true},
			"not_before":         schema.StringAttribute{Description: "The start of the validity window.", Computed: true},
			"not_after":          schema.StringAttribute{Description: "The end of the validity window.", Computed: true},
			"auto_renew":         schema.BoolAttribute{Description: "Whether the platform renews it.", Computed: true},
			"dns_zone_id":        schema.StringAttribute{Description: "The DNS zone used for ACME validation, if any.", Computed: true},
			"created_at": schema.StringAttribute{
				Description:   "The creation timestamp.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{Description: "The last update timestamp.", Computed: true},
		},
	}
}

func (r *certificateResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// fromAPI copies the server's view into state.
//
// 🔴 IT MUST NOT TOUCH private_key_pem OR chain_pem FROM THE SERVER'S REPLY.
// Both are preserved from prior state. The chain comes back only on some reads,
// and overwriting either from a response would blank a configured attribute on
// the next refresh and produce a permanent diff -- or, for the key, lose the
// only copy that exists outside the platform.
//
// Note this is a property of the PROVIDER, not a claim about the server: what
// the API does or does not return is not something this repository can verify
// (the appgw service lives elsewhere), and the docs must not assert it either.
//
// That is also what makes private_key_pem_wo safe with no read-path guard, the
// leak stage 1 hit on frostmoln_secret: apiCertificate above has NO field to
// decode a returned key into, so adoption is a compile error rather than a
// judgement call. Adding one would put the key into state on the create
// read-back, where nothing restores it from the (null) prior value the way Read
// does. TestCertificateReadDoesNotAdoptAKeyFromTheAPI pins it by feeding a
// response that does carry one.
//
// private_key_pem_wo_version is likewise untouched: it is practitioner-set and
// lives only in state.
func (m *CertificateModel) fromAPI(ctx context.Context, c *apiCertificate) {
	m.ID = types.StringValue(c.ID)
	m.GatewayID = types.StringValue(c.GatewayID)
	m.Name = types.StringValue(c.Name)
	m.Source = types.StringValue(c.Source)
	m.Status = types.StringValue(c.Status)
	m.CommonName = types.StringValue(c.CommonName)
	sans, _ := types.ListValueFrom(ctx, types.StringType, c.SANs)
	if c.SANs == nil {
		sans = types.ListValueMust(types.StringType, nil)
	}
	m.SANs = sans
	m.Issuer = types.StringValue(c.Issuer)
	m.SerialNumber = types.StringValue(c.SerialNumber)
	m.FingerprintSHA256 = types.StringValue(c.FingerprintSHA256)
	m.NotBefore = types.StringValue(c.NotBefore)
	m.NotAfter = types.StringValue(c.NotAfter)
	m.AutoRenew = types.BoolValue(c.AutoRenew)
	m.DNSZoneID = types.StringValue(c.DNSZoneID)
	m.CreatedAt = types.StringValue(c.CreatedAt)
	m.UpdatedAt = types.StringValue(c.UpdatedAt)
}

func (r *certificateResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan CertificateModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A write-only attribute is null in the plan by construction; its value only
	// ever reaches the provider through the config.
	keyWO := privateKeyPEMWO.Read(ctx, req.Config, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	upload := apiUploadCertificateRequest{
		Name:          plan.Name.ValueString(),
		ChainPEM:      plan.ChainPEM.ValueString(),
		PrivateKeyPEM: plan.PrivateKeyPEM.ValueString(),
	}
	if !keyWO.IsNull() {
		upload.PrivateKeyPEM = keyWO.ValueString()
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/certificates", plan.GatewayID.ValueString(),
	)), upload)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Upload Certificate", err.Error())
		return
	}
	c, err := client.ParseResponse[apiCertificate](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Certificate Response", err.Error())
		return
	}
	plan.fromAPI(ctx, c)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *certificateResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state CertificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/certificates/%s",
		state.GatewayID.ValueString(), state.ID.ValueString(),
	)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Certificate", err.Error())
		return
	}
	c, err := client.ParseResponse[apiCertificate](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Certificate Response", err.Error())
		return
	}
	// chain_pem and private_key_pem are deliberately preserved from state; see
	// fromAPI.
	state.fromAPI(ctx, c)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update cannot be reached: every configurable attribute carries RequiresReplace.
func (r *certificateResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Certificates Cannot Be Updated In Place",
		"The Application Gateway API has no certificate update operation, so every attribute of "+
			"this resource forces a replacement. Reaching this code means an attribute was added to "+
			"the schema without RequiresReplace.")
}

func (r *certificateResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state CertificateModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/certificates/%s",
		state.GatewayID.ValueString(), state.ID.ValueString(),
	)))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete Certificate", err.Error())
	}
}

// ImportState cannot recover the key, and says so rather than producing a
// resource that looks complete and breaks on the next plan.
func (r *certificateResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "certificate_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
	resp.Diagnostics.AddWarning("Private Key Cannot Be Imported",
		"private_key_pem is empty in the imported state, so the next plan will show a "+
			"replacement. A refresh cannot fill it in: Read has no access to your configuration, "+
			"and the key is deliberately preserved from prior state rather than adopted from the "+
			"API, so `terraform apply -refresh-only` will NOT adopt a value you put in the "+
			"configuration. Either accept the replacement, or hold it off with "+
			"`lifecycle { ignore_changes = [private_key_pem, chain_pem] }` until you can take "+
			"one. On the write-only path the replacement is unavoidable in any case: "+
			"private_key_pem_wo_version imports as unset, and setting it is itself a replacement.")
}

// privateKeyPEMWO is the write-only triple for the certificate private key.
//
// ExactlyOne, because private_key_pem was Required before the write-only form
// existed. The resource-level ExactlyOneOf that replaced Required is then the
// only thing refusing a configuration with neither key form — and it is skipped
// wholesale when either path is unknown at plan, which is exactly when a key
// arrives from a data source. See the package comment on internal/writeonly.
var privateKeyPEMWO = writeonly.Attr{ // pragma: allowlist secret
	WO:         "private_key_pem_wo",
	Version:    "private_key_pem_wo_version",
	Legacy:     "private_key_pem",
	Subject:    "the certificate",
	ExactlyOne: true,
}
