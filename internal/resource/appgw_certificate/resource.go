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
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/docs"
)

var (
	_ resource.Resource                = &certificateResource{}
	_ resource.ResourceWithImportState = &certificateResource{}
	_ resource.ResourceWithConfigure   = &certificateResource{}
)

// CertificateModel is the Terraform state model for a gateway certificate.
type CertificateModel struct {
	ID        types.String `tfsdk:"id"`
	GatewayID types.String `tfsdk:"gateway_id"`
	Name      types.String `tfsdk:"name"`

	ChainPEM      types.String `tfsdk:"chain_pem"`
	PrivateKeyPEM types.String `tfsdk:"private_key_pem"`

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

func (r *certificateResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_certificate"
}

func (r *certificateResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		Description: "Manages a TLS certificate on a Frostmoln Application Gateway.\n\n" +
			"~> **The private key is written to Terraform state.** The platform never returns it, " +
			"so Terraform is the only place it can be kept for a refresh — which means your state " +
			"file holds key material and must be treated as a secret: a remote backend with " +
			"encryption at rest and restricted access. Keeping the PEM out of your `.tf` files does " +
			"not keep it out of state — a value read from a variable or a secret store is persisted " +
			"exactly like a literal once it is assigned to `private_key_pem`. The only way to keep a " +
			"key out of Terraform entirely is not to hand it one: a platform-issued ACME certificate " +
			"(`source` = `acmeDns01`, obtained out of band — this resource only uploads) is attached " +
			"to a listener by ID and never carries key material. See the " +
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
				Description: "The PEM private key for `chain_pem`. The platform never returns it, so it is " +
					"preserved from state on every refresh. " + docs.StateSecretNote,
				Required:      true,
				Sensitive:     true,
				PlanModifiers: replaceStr,
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
// The key is never returned at all, and the chain comes back only on some
// reads. Overwriting either from a response would blank a Required attribute on
// the next refresh and produce a permanent diff -- or, for the key, lose the
// only copy that exists outside the platform.
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
	apiResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/certificates", plan.GatewayID.ValueString())),
		apiUploadCertificateRequest{
			Name:          plan.Name.ValueString(),
			ChainPEM:      plan.ChainPEM.ValueString(),
			PrivateKeyPEM: plan.PrivateKeyPEM.ValueString(),
		})
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
		state.GatewayID.ValueString(), state.ID.ValueString())), nil)
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
		state.GatewayID.ValueString(), state.ID.ValueString())))
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
		"The platform never returns a private key, so private_key_pem is empty in the imported "+
			"state and the next plan will show a replacement. Set private_key_pem (and chain_pem) "+
			"to the material you uploaded and run `terraform apply -refresh-only`, or accept the "+
			"replacement.")
}
