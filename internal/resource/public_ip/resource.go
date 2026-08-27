package public_ip

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/schemadoc"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

var (
	_ resource.Resource                = &publicIPResource{}
	_ resource.ResourceWithImportState = &publicIPResource{}
	_ resource.ResourceWithModifyPlan  = &publicIPResource{}
)

type publicIPResource struct {
	client *client.Client
}

// NewResource returns a new public IP resource.
func NewResource() resource.Resource {
	return &publicIPResource{}
}

func (r *publicIPResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip"
}

// MutualExclusivityNote is the ONE statement of the conflict between
// `frostmoln_public_ip.instance_id` and the `frostmoln_public_ip_association`
// resource. Both express the same attachment, so a practitioner reading either
// surface is choosing between them and has to be told the other exists.
//
// It is written once and rendered on BOTH — the resource description and
// `instance_id` here, the association's description there — because two copies
// of "these conflict" drift into two different explanations of the same
// unconverging plan. It lives in this package because the association imports
// it, and not the other way round.
const MutualExclusivityNote = "~> **`frostmoln_public_ip.instance_id` and the " +
	"`frostmoln_public_ip_association` resource are mutually exclusive — never use both for the " +
	"same address.** Both express the SAME attachment, so both would manage it: whichever applies " +
	"second undoes what the first did, every subsequent plan proposes the change again, and the " +
	"configuration never converges. (It is the same conflict the AWS provider documents between " +
	"`aws_eip.instance` and `aws_eip_association`.)\n\n" +
	"Pick one per address. Use **`frostmoln_public_ip.instance_id`** when the SAME configuration " +
	"allocates the address, so the address and its attachment are created and destroyed together.\n\n" +
	"Use **`frostmoln_public_ip_association`** when the address ALREADY EXISTS (look it up with " +
	"the `frostmoln_public_ip` data source), or when it has to outlive the instance it is attached " +
	"to. Destroying that resource detaches the address and leaves it allocated to your tenant, " +
	"whereas destroying a `frostmoln_public_ip` RELEASES the address for good."

func (r *publicIPResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a public IP in the Frostmoln Cloud Platform.\n\n" +
			"**Destroying this resource RELEASES THE ADDRESS, and the address does not come back.** " +
			"It returns to a shared regional pool and is re-issued to whoever asks for one next — " +
			"possibly another tenant, within minutes. Anything that named it stops matching: a " +
			"partner's allow-list entry, a DNS record, a firewall rule at the other end.\n\n" +
			"There is no undo and no support request that recovers it, so an address anyone else " +
			"depends on is worth protecting in the configuration itself with Terraform's own " +
			"`lifecycle { prevent_destroy = true }` — see the example below. It fails the PLAN, so a " +
			"module removal, a `terraform destroy -target`, or CI running `terraform destroy " +
			"-auto-approve` stops before anything is sent. This provider additionally refuses to " +
			"release an address that is serving a VPC's outbound path unless `acknowledge_address_loss = " +
			"true` — see that attribute.\n\n" +
			MutualExclusivityNote,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the public IP.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"address": schema.StringAttribute{
				Description: "The allocated IP address.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the instance to associate with. Set to associate, remove to disassociate.\n\n" +
					MutualExclusivityNote + "\n\n" +
					schemadoc.GatewayOrderingNote("frostmoln_public_ip"),
				Optional: true,
			},
			"tags": schema.MapAttribute{
				Description: "Tags for the public IP.",
				Optional:    true,
				ElementType: types.StringType,
				PlanModifiers: []planmodifier.Map{
					mapplanmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The status of the public IP.",
				Computed:    true,
			},
			"acknowledge_address_loss": schema.BoolAttribute{
				Description: "Set to true to allow this public IP to be released while it is serving a " +
					"VPC's outbound traffic (`attachment.kind` = \"gateway\").\n\n" +
					"**Releasing it loses the address permanently.** It returns to a shared regional " +
					"pool and is re-issued to whoever asks next, so a partner's allow-list entry or a " +
					"DNS record naming it stops matching — and cannot be restored by re-creating " +
					"anything.\n\n" +
					"The provider refuses that release without this flag, and refuses it BEFORE any " +
					"request is sent, because the platform cannot refuse it for you on the path a " +
					"correct configuration produces. When `frostmoln_gateway.public_ip_id` " +
					"refers to this resource, Terraform destroys the gateway first; the platform then " +
					"hands the address back as an ordinary unattached address, and the release that " +
					"follows looks — to the platform — like the release of something idle.\n\n" +
					"Set it to true and apply, then destroy. Leaving it unset (the default) makes " +
					"`terraform destroy` fail on this resource instead of silently giving the address " +
					"away.\n\n" +
					"**Remove it from the configuration again once the destroy is done, and do not " +
					"leave it set on a steady-state resource.** It is ordinary configuration, so a " +
					"`true` left behind stays in state for the life of the resource and permanently " +
					"disarms this control.\n\n" +
					"It is not a substitute for `lifecycle { prevent_destroy = true }`, which fails the " +
					"plan rather than the apply and covers every reason an address can be destroyed, " +
					"including ones this attribute does not gate.",
				Optional: true,
			},
			"attachment": schema.SingleNestedAttribute{
				Description: "What is currently using this address. Never null — an address nothing " +
					"is using reports `kind = \"none\"`, so `attachment.kind` can always be read.\n\n" +
					"Read this, not `instance_id`, to tell whether the address is free. An address " +
					"serving a VPC's outbound traffic has no instance and no port, so a configuration " +
					"that infers \"unused\" from an empty `instance_id` would offer to release the " +
					"address a whole VPC leaves the platform from — and that release cannot be undone.",
				Computed: true,
				Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{
						Description: "\"none\" (allocated, nothing using it — still yours, still " +
							"counted against quota and still billed), \"port\" (attached to an " +
							"instance or load balancer, its inbound address), \"gateway\" " +
							"(it is a VPC's outbound source address; see `frostmoln_gateway`), " +
							"or \"unknown\".\n\n" +
							"While it is \"gateway\" the platform refuses to attach this address " +
							"to an instance, and this provider refuses to release it without " +
							"`acknowledge_address_loss = true`.\n\n" +
							"\"unknown\" means the platform did not report an attachment for this " +
							"address. Read it as \"not established\", never as \"free\": an address " +
							"serving a VPC's outbound path has no port either, so nothing here distinguishes " +
							"the two. New kinds can appear without a provider upgrade; a kind this " +
							"provider build does not recognise is passed through unchanged and treated " +
							"as attached.",
						Computed: true,
					},
					"resource_id": schema.StringAttribute{
						Description: "What holds the address: the network port for \"port\", the " +
							"gateway for \"gateway\". Null for \"none\".",
						Computed: true,
					},
					"vpc_id": schema.StringAttribute{
						Description: "The VPC whose outbound traffic leaves from this address. Set " +
							"only for \"gateway\".",
						Computed: true,
					},
				},
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP of the associated instance.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The creation timestamp.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *publicIPResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData),
		)
		return
	}

	r.client = c
}

// ModifyPlan does two things the framework's defaults get wrong for this
// resource.
//
// 1. It puts the attachment IN THE PLAN, as a warning, when a destroy is
// planned for an address something is holding. Nothing else in the plan says
// so: `terraform plan` renders one ordinary "will be destroyed" line, and the
// attribute that would give it away — `instance_id` — is empty precisely
// BECAUSE the address is an outbound source rather than an instance's. The
// practitioner reads "an unused address goes away" and approves an apply that
// takes a whole VPC's outbound path with it, and gives the address itself away
// for good.
//
// A warning here, and the hard refusal in Delete. The two are not redundant:
// this one arrives BEFORE the plan is approved, which is when a human can still
// change their mind, while Delete's refusal is what stops an unattended
// `terraform destroy -auto-approve` — a run where warnings scroll past nobody.
//
// 2. It marks the three values the platform recomputes on an association
// change — `attachment`, `private_ip` and `status` — unknown. For a
// Computed-only attribute the proposed new state carries the PRIOR value and
// the framework only rewrites a null one to unknown, so moving the address from
// one instance to another would plan the OLD port, private IP and status as
// known while the apply legitimately returns new ones. Terraform core then
// aborts with "Provider produced inconsistent result after apply", which no
// retry clears. With `instance_id` unchanged the state value is kept, so an
// unchanged resource still plans empty.
func (r *publicIPResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		// Create: every null Computed attribute is unknown already.
		return
	}

	var state PublicIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if req.Plan.Raw.IsNull() {
		if state.RequiresAddressLossAcknowledgement() {
			summary, detail := addressLossWarning(&state)
			resp.Diagnostics.AddWarning(summary, detail)
		}
		return
	}

	var plan PublicIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.InstanceID.Equal(state.InstanceID) {
		return
	}

	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("attachment"), types.ObjectUnknown(AttachmentAttrTypes))...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("private_ip"), types.StringUnknown())...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
}

// addressLossWarning builds the plan-time warning, and the same text carries
// the Delete refusal so the two cannot drift apart.
//
// Two shapes, because an unrecognised attachment kind is a different statement
// from an gateway binding and must not be described as one — a diagnostic that
// asserts a VPC is affected when the provider does not actually know that sends
// the practitioner to check the wrong thing.
func addressLossWarning(state *PublicIPModel) (summary, detail string) {
	// What is lost is the same either way, and it is the part practitioners
	// under-estimate: the resource is replaceable, the address is not.
	const irreversible = "Releasing it gives the ADDRESS up permanently: it returns to a shared regional " +
		"pool and is re-issued to whoever asks for one next. A partner's allow-list entry or a DNS " +
		"record naming it stops matching, and re-creating this resource gets a different address.\n\n" +
		"The plan shows nothing else about this — the address has no instance attached precisely " +
		"BECAUSE it is not an instance's address."

	if state.IsGatewayBound() {
		return "This public IP is a VPC's outbound source address",
			fmt.Sprintf("Public IP %s (%s) is not idle: it is the address VPC %s's outbound traffic "+
				"leaves the platform from. %s\n\nDestroying it also takes that VPC's internet path "+
				"down, and with it platform DNS resolution and managed-service connectivity for every "+
				"instance in the VPC — though that part comes back when the gateway does, and the "+
				"address does not.\n\nIf you mean to keep the address, change the VPC's gateway "+
				"instead of destroying this (`frostmoln_gateway`): point its `public_ip_id` at "+
				"another address.\n\nIf you really mean to give the address "+
				"up, set `acknowledge_address_loss = true` on this resource and apply that first.",
				state.ID.ValueString(), state.Address.ValueString(),
				vpcOrUnknown(state.AttachedVPCID()), irreversible)
	}

	return "This public IP is attached to something this provider does not recognise",
		fmt.Sprintf("Public IP %s (%s) reports `attachment.kind = %q`, which this provider build does "+
			"not know. That is the platform stating positively that SOMETHING is using the address — "+
			"a kind added after this provider was built — not that the address is idle. %s\n\nUpgrade "+
			"the provider to find out what holds it. If you mean to give the address up regardless, "+
			"set `acknowledge_address_loss = true` on this resource and apply that first.",
			state.ID.ValueString(), state.Address.ValueString(), state.AttachmentKind(), irreversible)
}

// vpcOrUnknown keeps the destroy warning readable when the platform reported an
// gateway attachment without naming the VPC.
func vpcOrUnknown(vpcID string) string {
	if vpcID == "" {
		return "(id not reported)"
	}
	return vpcID
}

// ResolveInstancePortID looks up the Neutron port to attach a public IP to by
// reading the instance and returning its first network port. The provisioning
// associate endpoint requires a portId; instance_id is the user-friendly input
// the provider resolves on their behalf (mirrors how the portal associates by
// port).
//
// Exported because `frostmoln_public_ip_association` has to resolve the SAME
// port for the same instance. Two copies of "the instance's first network port"
// that drift would let the two resources bind an address to different ports for
// one instance, and each would then read the other's binding as drift.
func ResolveInstancePortID(ctx context.Context, c *client.Client, instanceID string) (string, error) {
	ports, err := InstancePortIDs(ctx, c, instanceID)
	if err != nil {
		return "", err
	}
	return SelectInstancePort(ports, instanceID)
}

// SelectInstancePort is the rule for "which port does a configuration that
// names only an INSTANCE mean?" — the first port the platform reports.
//
// It is exported and separate from ResolveInstancePortID because
// `frostmoln_public_ip_association` already holds the whole port set (it needs
// it to validate a configured `port_id`) and must not re-read the instance just
// to apply the same rule. One definition, so the two resources cannot drift
// into binding different ports for the same instance.
func SelectInstancePort(ports []string, instanceID string) (string, error) {
	if len(ports) == 0 {
		return "", fmt.Errorf("instance %s has no network port available to associate the public IP with", instanceID)
	}
	return ports[0], nil
}

// InstancePortIDs returns every network port the instance has, in the order the
// platform reports them.
//
// ResolveInstancePortID picks the first of these to ATTACH to. Deciding whether
// an address is still attached to an instance is the other question and needs
// the whole set: an address can sit on a port that is not the first one (it was
// associated out of band, or the instance grew a second interface afterwards),
// and reading only the first port would report that as detached and silently
// drop a live association out of state.
//
// A 404 for the instance is returned as the client's not-found error, so
// callers can tell "the instance is gone" from "the read failed".
func InstancePortIDs(ctx context.Context, c *client.Client, instanceID string) ([]string, error) {
	readResp, err := c.Get(ctx, c.TenantPath("/instances/"+instanceID), nil)
	if err != nil {
		if client.IsNotFound(err) {
			return nil, err
		}
		return nil, fmt.Errorf("failed to read instance %s: %w", instanceID, err)
	}
	var inst apiInstanceForPort
	if err := json.Unmarshal(readResp.Body, &inst); err != nil {
		return nil, fmt.Errorf("failed to parse instance %s response: %w", instanceID, err)
	}
	ports := make([]string, 0, len(inst.Networks))
	for _, n := range inst.Networks {
		if n.PortID != "" {
			ports = append(ports, n.PortID)
		}
	}
	return ports, nil
}

func (r *publicIPResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PublicIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	allocateReq := plan.toAllocateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/public-ips"), allocateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Allocate Public IP", err.Error())
		return
	}

	// Allocate routes through provisioning → 202 + an Operation envelope
	// (operationId only, NOT the public IP). Resolve the FIP id from the
	// operation; a non-202 body is parsed directly for a sync backend.
	var fip apiPublicIP
	var fipID string
	if apiResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](apiResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		done, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute)
		if waitErr != nil {
			resp.Diagnostics.AddError("Public IP Allocation Failed", waitErr.Error())
			return
		}
		fipID = done.ResourceID
		if fipID == "" {
			resp.Diagnostics.AddError("Public IP Operation Returned No Resource ID",
				"The public IP allocate operation completed but returned no resource ID. Check `fm network public-ip list` and import it if necessary.")
			return
		}
	} else {
		if err := json.Unmarshal(apiResp.Body, &fip); err != nil {
			resp.Diagnostics.AddError("Failed to Parse Public IP Response", err.Error())
			return
		}
		fipID = fip.ID
	}

	// If instance_id is set, associate after allocation (also async via provisioning).
	if !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() {
		portID, portErr := ResolveInstancePortID(ctx, r.client, plan.InstanceID.ValueString())
		if portErr != nil {
			AddPortResolutionError(&resp.Diagnostics, plan.InstanceID.ValueString(), portErr)
			return
		}
		assocReq := apiAssociatePublicIPRequest{PortID: portID}
		assocResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s/associate", fipID)), assocReq)
		if err != nil {
			AddAPIError(&resp.Diagnostics, "Failed to Associate Public IP", err)
			return
		}
		if assocResp.IsAccepted() {
			op, opErr := client.ParseResponse[client.Operation](assocResp)
			if opErr != nil {
				resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
				return
			}
			if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); waitErr != nil {
				resp.Diagnostics.AddError("Public IP Association Failed", waitErr.Error())
				return
			}
		}
	}

	// Read the final state by the resolved id (covers allocate-only and the
	// post-association state).
	readResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", fipID)), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Public IP After Creation", err.Error())
		return
	}
	if err := json.Unmarshal(readResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Public IP Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *publicIPResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PublicIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", state.ID.ValueString())), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Public IP", err.Error())
		return
	}

	var fip apiPublicIP
	if err := json.Unmarshal(apiResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Public IP Response", err.Error())
		return
	}

	state.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// fetch reads one public IP.
func (r *publicIPResource) fetch(ctx context.Context, fipID string) (*apiPublicIP, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/public-ips/"+fipID), nil)
	if err != nil {
		return nil, err
	}
	var fip apiPublicIP
	if err := json.Unmarshal(apiResp.Body, &fip); err != nil {
		return nil, fmt.Errorf("failed to parse public IP %s response: %w", fipID, err)
	}
	return &fip, nil
}

// detachFromRecordedInstance takes the address off the instance this resource
// has RECORDED it against — after confirming that is where it actually is. It
// reports whether the update may continue; diagnostics say why when it may not.
//
// The confirmation is the whole point. `disassociate` takes no port: it removes
// whatever binding the address holds at that moment, so issuing it blind is the
// same hazard `frostmoln_public_ip_association.Delete` was written to avoid —
// and it is worse from an UPDATE, because nothing in the plan hints at it. A
// `frostmoln_public_ip` whose `instance_id` moves from X to Y, while a
// `frostmoln_public_ip_association` (or anyone else) holds that same address,
// would rip the address off the port the association bound it to; the running
// instance loses its inbound address with no plan line saying so, and the
// association's next Read quietly drops itself from state.
func (r *publicIPResource) detachFromRecordedInstance(ctx context.Context, fipID, oldInstanceID, newInstanceID string, diags *diag.Diagnostics) bool {
	fip, err := r.fetch(ctx, fipID)
	if err != nil {
		diags.AddError("Failed to Read Public IP", err.Error())
		return false
	}
	if fip.PortID == "" {
		// No port holds the address, so there is nothing for a disassociate to
		// remove. (An address that is a VPC's outbound source also has no port;
		// the associate that may follow is what the platform refuses, with the
		// gateway text AddAPIError renders.)
		return true
	}

	ports, portsErr := InstancePortIDs(ctx, r.client, oldInstanceID)
	switch {
	case portsErr != nil && client.IsNotFound(portsErr):
		// The instance is gone, so it holds nothing: whatever the address is on
		// now is not this resource's to remove.
		ports = nil
	case portsErr != nil:
		AddPortResolutionError(diags, oldInstanceID, portsErr)
		return false
	}

	if slices.Contains(ports, fip.PortID) {
		return r.disassociate(ctx, fipID, diags)
	}

	const summary = "Public IP Is Attached To Something Else"
	detail := fmt.Sprintf("Public IP %s is recorded against instance %s, but the platform reports it "+
		"attached to port %s, which is not one of that instance's ports. It was attached outside this "+
		"resource — by a `frostmoln_public_ip_association`, by the portal, or by the CLI.\n\n"+
		"Detaching it here would take the address away from whatever holds it now, with nothing in "+
		"the plan saying so, so nothing was disassociated.", fipID, oldInstanceID, fip.PortID)

	if newInstanceID == "" {
		// The plan only asks for the address to stop being this resource's. It
		// already is: something else owns the binding.
		diags.AddWarning(summary, detail+" The address is untouched and still allocated to your tenant.")
		return true
	}

	diags.AddError(summary, detail+fmt.Sprintf("\n\nNothing has been changed — the address was NOT "+
		"moved to instance %s. Decide which configuration owns this attachment first: remove "+
		"`instance_id` from this resource if a `frostmoln_public_ip_association` manages it, or "+
		"detach it where it is attached now.", newInstanceID))
	return false
}

// disassociate removes the address's current binding and waits for the outcome.
func (r *publicIPResource) disassociate(ctx context.Context, fipID string, diags *diag.Diagnostics) bool {
	disResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s/disassociate", fipID)), nil)
	if err != nil {
		diags.AddError("Failed to Disassociate Public IP", err.Error())
		return false
	}
	if disResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](disResp)
		if opErr != nil {
			diags.AddError("Failed to Parse Operation Response", opErr.Error())
			return false
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); waitErr != nil {
			diags.AddError("Public IP Disassociation Failed", waitErr.Error())
			return false
		}
	}
	return true
}

func (r *publicIPResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan PublicIPModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var state PublicIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	fipID := state.ID.ValueString()

	// Handle instance association/disassociation changes
	oldInstanceID := state.InstanceID.ValueString()
	newInstanceID := ""
	if !plan.InstanceID.IsNull() && !plan.InstanceID.IsUnknown() {
		newInstanceID = plan.InstanceID.ValueString()
	}

	if oldInstanceID != newInstanceID {
		// Disassociate if previously associated
		if oldInstanceID != "" {
			if !r.detachFromRecordedInstance(ctx, fipID, oldInstanceID, newInstanceID, &resp.Diagnostics) {
				return
			}
		}

		// Associate if new instance_id is set
		if newInstanceID != "" {
			portID, portErr := ResolveInstancePortID(ctx, r.client, newInstanceID)
			if portErr != nil {
				AddPortResolutionError(&resp.Diagnostics, newInstanceID, portErr)
				return
			}
			assocResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s/associate", fipID)), apiAssociatePublicIPRequest{PortID: portID})
			if err != nil {
				AddAPIError(&resp.Diagnostics, "Failed to Associate Public IP", err)
				return
			}
			if assocResp.IsAccepted() {
				op, opErr := client.ParseResponse[client.Operation](assocResp)
				if opErr != nil {
					resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
					return
				}
				if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); waitErr != nil {
					resp.Diagnostics.AddError("Public IP Association Failed", waitErr.Error())
					return
				}
			}
		}
	}

	// Handle tags update
	if !plan.Tags.Equal(state.Tags) {
		updateReq := apiUpdatePublicIPRequest{
			Tags: tftags.ForUpdate(ctx, plan.Tags, &resp.Diagnostics),
		}
		if resp.Diagnostics.HasError() {
			return
		}
		// PUT, not PATCH — network registers PUT for the in-place update and
		// nothing registers PATCH. See the frostmoln_vpc Update for the full note.
		_, err := r.client.Put(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", fipID)), updateReq)
		if err != nil {
			resp.Diagnostics.AddError("Failed to Update Public IP Tags", err.Error())
			return
		}
	}

	// Re-read the public IP to get the final state
	readResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", fipID)), nil)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Read Public IP", err.Error())
		return
	}

	var fip apiPublicIP
	if err := json.Unmarshal(readResp.Body, &fip); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Public IP Response", err.Error())
		return
	}

	plan.fromAPI(ctx, &fip, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *publicIPResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PublicIPModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// REFUSE, before any request, when the RECORDED attachment says something is
	// holding this address and the practitioner has not said they accept losing
	// it.
	//
	// This is the only thing standing between a correct configuration and the
	// permanent loss of a pinned gateway source address, because the platform's own
	// refusal cannot fire on that path. `frostmoln_gateway.public_ip_id`
	// makes the gateway depend on this resource, so `terraform destroy` destroys
	// the gateway FIRST; the platform then hands the address back as an ordinary
	// unattached address and the DELETE that follows is, from its side, the
	// release of something idle. It succeeds, and the address is gone.
	//
	// The state read here is the PRE-APPLY state — Terraform does not re-read a
	// resource between the two destroys — so it still records the gateway
	// binding that the gateway's destroy has just undone. That is precisely the
	// knowledge the platform no longer has at this moment, and the reason the
	// gate belongs here rather than being left to the API.
	//
	// A plan approval is NOT the acknowledgement. `terraform destroy` renders
	// one ordinary "will be destroyed" line for this resource, and a public IP
	// is routinely destroyed as a side effect of tearing down something else; an
	// unattended `-auto-approve` run shows a human nothing at all.
	if state.RequiresAddressLossAcknowledgement() && !state.AcknowledgeAddressLoss.ValueBool() {
		summary, detail := addressLossWarning(&state)
		resp.Diagnostics.AddAttributeError(
			path.Root("acknowledge_address_loss"),
			summary,
			detail+"\n\nNothing has been changed.",
		)
		return
	}

	delResp, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s", state.ID.ValueString())))
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		// A SYNCHRONOUS refusal — a pre-flight rejection before the release is
		// started. The raw envelope does not say why an apparently unused
		// address cannot go, so it is translated.
		AddAPIError(&resp.Diagnostics, "Failed to Delete Public IP", err)
		return
	}

	// A 202 IS NOT A RELEASE. The write endpoints route through provisioning,
	// which starts a workflow and answers 202 immediately — before the platform
	// has decided anything. Returning here on a 202 would make Terraform drop
	// this resource out of state while the release is still in flight, and a
	// refusal would then land on an operation nobody reads. The next apply sees
	// the resource missing, plans a create, and allocates a DIFFERENT address —
	// which destroys the stable-address promise precisely when it mattered.
	//
	// So wait for the outcome, and fail the apply on a refusal. Failing keeps
	// the row in state, which is the correct record: the address is still
	// there.
	if delResp.IsAccepted() {
		op, opErr := client.ParseResponse[client.Operation](delResp)
		if opErr != nil {
			resp.Diagnostics.AddError("Failed to Parse Operation Response", opErr.Error())
			return
		}
		if _, waitErr := r.client.WaitForOperation(ctx, op.OperationID, 2*time.Second, 5*time.Minute); waitErr != nil {
			AddOperationError(&resp.Diagnostics, "Public IP Release Failed", waitErr)
			return
		}
	}
}

func (r *publicIPResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
