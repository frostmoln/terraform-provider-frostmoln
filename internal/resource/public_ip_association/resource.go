package public_ip_association

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/resource/public_ip"
)

var (
	_ resource.Resource                = &publicIPAssociationResource{}
	_ resource.ResourceWithImportState = &publicIPAssociationResource{}
)

// Defaults for the wait on a provisioning operation.
const (
	defaultPollInterval = 2 * time.Second
	defaultPollTimeout  = 5 * time.Minute
)

type publicIPAssociationResource struct {
	client *client.Client

	// pollInterval and pollTimeout bound the wait for the operation a write
	// starts. They are fields rather than constants so a test can drive the
	// timeout in milliseconds: the branch that decides what to record when the
	// platform has NOT answered in time is the one that most needs covering —
	// it is the difference between a stranded live attachment and a state row —
	// and a five-minute constant cannot be exercised any other way.
	pollInterval time.Duration
	pollTimeout  time.Duration
}

// NewResource returns a new public IP association resource factory.
func NewResource() resource.Resource {
	return &publicIPAssociationResource{
		pollInterval: defaultPollInterval,
		pollTimeout:  defaultPollTimeout,
	}
}

func (r *publicIPAssociationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_public_ip_association"
}

func (r *publicIPAssociationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Attaches an existing public IP to an instance, as that instance's inbound address.\n\n" +
			"This is the resource for an address you already have — one reserved earlier, published in " +
			"DNS, or already sitting in a partner's allow-list. Nothing about the ADDRESS is managed " +
			"here, only the attachment: look the address up with the `frostmoln_public_ip` data source " +
			"and pass its `id`. Destroying this detaches the address and **leaves it allocated to your " +
			"tenant**, ready for the next instance — which is the point, and the difference from " +
			"destroying a `frostmoln_public_ip`, where the address itself is released and does not come " +
			"back.\n\n" +
			public_ip.MutualExclusivityNote + "\n\n" +
			public_ip.GatewayOrderingNote("frostmoln_public_ip_association") + "\n\n" +
			"Every configurable attribute forces replacement. The platform has no in-place " +
			"re-point — moving an address is a disassociate followed by an associate — so Terraform " +
			"sequences it as a destroy and a create, and the instance is without the address in " +
			"between.\n\n" +
			"~> **`create_before_destroy` does not work on this resource.** With it, Terraform creates " +
			"the replacement BEFORE destroying the old one, and the old one still holds the address: " +
			"the platform refuses the second attachment with `409 Public IP is already associated` and " +
			"the apply fails, leaving the original attachment in place. There is one address and it can " +
			"be in one place at a time, so a replacement is necessarily destroy-then-create. Plan for " +
			"the gap instead — the address is off the instance for the duration of the two calls.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The composite identifier of the association ({public_ip_id}/{instance_id}).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"public_ip_id": schema.StringAttribute{
				Description: "The ID of the public IP to attach. The address must already be allocated — " +
					"this resource never allocates one. Use the `frostmoln_public_ip` data source to " +
					"resolve a reserved address to its id, or `frostmoln_public_ip.<name>.id` when the " +
					"same configuration allocates it (in which case leave that resource's `instance_id` " +
					"unset).",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the instance to attach the public IP to. The address is bound to " +
					"the instance's FIRST network port unless `port_id` names another one.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"port_id": schema.StringAttribute{
				Description: "The instance network port to bind the address to. The platform associates " +
					"an address with a PORT, not with an instance, so this is what the attachment really " +
					"is — and what `frostmoln_public_ip.attachment.resource_id` reports for " +
					"`kind = \"port\"`.\n\n" +
					"Leave it unset for a single-homed instance and it resolves to the instance's first " +
					"network port. Set it for a multi-NIC instance to choose which interface answers on " +
					"the address — the same choice `fm network public-ip associate --port-id` offers. " +
					"It must be one of `instance_id`'s own ports; anything else is refused before a " +
					"request is sent, because an address bound to a port outside the instance is an " +
					"attachment this resource would drop from state on its next refresh.\n\n" +
					"Changing it forces replacement (disassociate, then associate).",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.String{
					// RequiresReplaceIfConfigured, not RequiresReplace: the
					// attribute is Optional AND Computed, so a value the
					// PLATFORM reports (the address was moved to another of the
					// instance's ports out of band) must not plan a replacement
					// nobody asked for. Only a change to a value the
					// configuration states does.
					stringplanmodifier.RequiresReplaceIfConfigured(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *publicIPAssociationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

// readPublicIP fetches what currently holds the address — the bound port and
// the platform's explicit attachment statement; see apiPublicIP for why both.
func (r *publicIPAssociationResource) readPublicIP(ctx context.Context, publicIPID string) (*apiPublicIP, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/public-ips/"+publicIPID), nil)
	if err != nil {
		return nil, err
	}
	var fip apiPublicIP
	if err := json.Unmarshal(apiResp.Body, &fip); err != nil {
		return nil, fmt.Errorf("failed to parse public IP %s response: %w", publicIPID, err)
	}
	return &fip, nil
}

// waitOutcome is what is known about a write once the wait for its operation
// has ended. The two failure values need OPPOSITE advice, which is why they are
// not one error: a refusal is safe to retry and has changed nothing, while a
// wait that gave up may be sitting on a change the platform is about to make
// and Terraform has not recorded.
type waitOutcome int

const (
	// outcomeCompleted — the operation finished successfully.
	outcomeCompleted waitOutcome = iota

	// outcomeRefused — the operation reached a terminal failure. The platform
	// decided, and it decided no: nothing was changed, and applying again once
	// the reason is dealt with is safe.
	outcomeRefused

	// outcomeUnknown — the wait gave up with the operation still in flight
	// (timed out, cancelled, or the operation could not be read back). The
	// write may still complete, so the practitioner has to be told the platform
	// may end up somewhere Terraform has not recorded.
	outcomeUnknown
)

// waitForOperation blocks on the provisioning operation a 202 started. Every
// write on this surface routes through provisioning, which answers 202 with an
// operation id BEFORE the platform has decided anything, so returning on the
// 202 would record an attachment that may still be refused.
func (r *publicIPAssociationResource) waitForOperation(ctx context.Context, apiResp *client.Response) (waitOutcome, error) {
	if !apiResp.IsAccepted() {
		return outcomeCompleted, nil
	}
	op, err := client.ParseResponse[client.Operation](apiResp)
	if err != nil {
		// The write was accepted and its id is unreadable, so nothing here can
		// say what became of it.
		return outcomeUnknown, fmt.Errorf("failed to parse operation response: %w", err)
	}
	if _, err := r.client.WaitForOperation(ctx, op.OperationID, r.pollInterval, r.pollTimeout); err != nil {
		return r.classifyWaitFailure(ctx, op.OperationID), err
	}
	return outcomeCompleted, nil
}

// classifyWaitFailure asks the OPERATION what happened rather than reading the
// wait error's text. A terminal status is the platform's own decision and the
// only evidence that separates "refused, nothing happened" from "still running,
// something may have happened" — and everything the practitioner should do next
// hangs off which of those it was.
//
// Anything it cannot establish is "unknown": a cancelled workflow may have got
// part-way, and an operation that cannot be read has said nothing at all.
func (r *publicIPAssociationResource) classifyWaitFailure(ctx context.Context, operationID string) waitOutcome {
	op, err := r.client.GetOperation(ctx, operationID)
	if err != nil {
		return outcomeUnknown
	}
	if op.Status == client.OperationStatusFailed {
		return outcomeRefused
	}
	return outcomeUnknown
}

func (r *publicIPAssociationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan PublicIPAssociationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	publicIPID := plan.PublicIPID.ValueString()
	instanceID := plan.InstanceID.ValueString()

	// The associate endpoint binds a PORT, not an instance.
	ports, err := public_ip.InstancePortIDs(ctx, r.client, instanceID)
	if err != nil {
		public_ip.AddPortResolutionError(&resp.Diagnostics, instanceID, err)
		return
	}
	portID, ok := r.targetPort(plan, ports, instanceID, &resp.Diagnostics)
	if !ok {
		return
	}

	// LOOK BEFORE ATTACHING. The associate is not idempotent — a second one
	// against an address that already holds the binding is refused with 409
	// "Public IP is already associated" — and the wait below is bounded, so a
	// slow-but-successful associate that this provider gave up on leaves a LIVE
	// attachment with nothing in state. Without this read the next apply, and
	// every apply after it, fails on that 409 for ever.
	//
	// So the requested end state is checked first: if the address is already on
	// the port this configuration asks for, the attachment exists and is
	// recorded. That is adoption of an ATTACHMENT, not of an address — destroy
	// still only detaches, and the address stays allocated to the tenant either
	// way — so it takes on nothing the practitioner has not asked for.
	existing, err := r.readPublicIP(ctx, publicIPID)
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError(
				"Public IP Not Found",
				fmt.Sprintf("Public IP %s does not exist. This resource attaches an address you "+
					"ALREADY have — it never allocates one — so `public_ip_id` has to name an "+
					"allocated address: look it up with the `frostmoln_public_ip` data source, or "+
					"allocate one with a `frostmoln_public_ip` resource (leaving its `instance_id` "+
					"unset). Nothing was changed.", publicIPID),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Public IP", err.Error())
		return
	}
	switch {
	case existing.PortID == portID:
		resp.Diagnostics.AddWarning(
			"Public IP Was Already Attached To This Port",
			fmt.Sprintf("Public IP %s is already attached to port %s (instance %s), which is exactly "+
				"the attachment this resource describes, so nothing was sent and the existing "+
				"attachment has been recorded in Terraform state.\n\nThis is what a re-apply after a "+
				"timed-out or interrupted apply looks like, and it is the intended outcome. If you did "+
				"NOT expect the attachment to exist, something else made it — check that no "+
				"`frostmoln_public_ip` resource sets `instance_id` for this address, because the two "+
				"manage the same attachment and a configuration using both never converges.",
				publicIPID, portID, instanceID),
		)
		r.record(ctx, resp, plan, publicIPID, instanceID, portID)
		return

	case existing.PortID != "":
		resp.Diagnostics.AddError(
			"Public IP Is Already Attached Elsewhere",
			fmt.Sprintf("Public IP %s is attached to port %s, not to port %s (instance %s) as this "+
				"resource describes. An address serves one thing at a time, so the platform would "+
				"refuse the attachment with a bare `409 Public IP is already associated`; nothing was "+
				"sent and nothing was changed.\n\n%s", publicIPID, existing.PortID, portID, instanceID,
				alreadyAttachedAdvice(existing.PortID, portID, instanceID, ports)),
		)
		return
	}

	assocResp, err := r.client.Post(ctx,
		r.client.TenantPath(fmt.Sprintf("/public-ips/%s/associate", publicIPID)),
		apiAssociateRequest{PortID: portID})
	if err != nil {
		// Shared mapping: attaching an address that is a VPC's outbound source
		// is refused with PUBLIC_IP_IN_USE_BY_GATEWAY, and that refusal reads as
		// an obscure 409 unless it is translated.
		public_ip.AddAPIError(&resp.Diagnostics, "Failed to Associate Public IP", err)
		return
	}
	outcome, err := r.waitForOperation(ctx, assocResp)
	if err != nil {
		if outcome == outcomeRefused {
			// The platform decided: no. Nothing is attached, so there is nothing
			// to import and nothing to clean up — which is worth SAYING, because
			// the alternative failure a line below reads very differently.
			public_ip.AddOperationErrorContext(&resp.Diagnostics, "Public IP Association Failed",
				fmt.Sprintf("The platform refused to attach public IP %s to port %s (instance %s). "+
					"Nothing was attached and nothing has been recorded in Terraform state, so applying "+
					"again is safe once the reason below is dealt with.", publicIPID, portID, instanceID),
				err)
			return
		}
		r.reportUnknownAssociateOutcome(ctx, resp, plan, publicIPID, instanceID, portID, err)
		return
	}

	// Confirm the platform ended up where the plan said. A completed operation
	// that left the address on a different port is a state row that would plan
	// a no-op forever while the instance has no public address.
	//
	// Both failures below leave the apply errored with NOTHING in state while
	// the associate has already completed, so both say so: an error that hides
	// a change it made is worse than the change.
	fip, err := r.readPublicIP(ctx, publicIPID)
	if err != nil {
		resp.Diagnostics.AddError(
			"Failed to Read Public IP After Association",
			fmt.Sprintf("The association of public IP %s with instance %s COMPLETED, but reading the "+
				"address back to confirm it failed, so nothing was recorded in Terraform state. The "+
				"attachment is live on the platform; check it with the portal or `fm network public-ip "+
				"list`, then either import it (`terraform import "+
				"frostmoln_public_ip_association.<name> %s/%s`) or detach it before applying again.\n\n"+
				"The read failed with: %s", publicIPID, instanceID, publicIPID, instanceID, err.Error()),
		)
		return
	}
	if fip.PortID != portID {
		resp.Diagnostics.AddError(
			"Public IP Association Mismatch",
			fmt.Sprintf("Public IP %s was asked to attach to port %s (instance %s) and the operation "+
				"completed, but the platform now reports the address on port %q instead. Something else "+
				"moved it in between, so nothing has been recorded in Terraform state and the address is "+
				"NOT where this configuration says it should be.\n\nCheck what holds it — "+
				"`frostmoln_public_ip.attachment` on the data source reports the port — before applying "+
				"again.", publicIPID, portID, instanceID, fip.PortID),
		)
		return
	}

	r.record(ctx, resp, plan, publicIPID, instanceID, portID)
}

// record writes the attachment to state. One place, so no path can record a
// half-filled row.
func (r *publicIPAssociationResource) record(ctx context.Context, resp *resource.CreateResponse, plan PublicIPAssociationModel, publicIPID, instanceID, portID string) {
	plan.ID = types.StringValue(compositeID(publicIPID, instanceID))
	plan.PortID = types.StringValue(portID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// targetPort is the port this association binds: the one the configuration
// names, or the instance's first when it names none.
//
// A configured port that is not the instance's is refused here rather than
// sent. The platform would accept it — it binds ports, and does not care which
// instance the configuration mentions — and Read would then find the address on
// a port outside `instance_id` and drop the association out of state on the
// next refresh, every refresh.
func (r *publicIPAssociationResource) targetPort(plan PublicIPAssociationModel, ports []string, instanceID string, diags *diag.Diagnostics) (string, bool) {
	if plan.PortID.IsNull() || plan.PortID.IsUnknown() {
		portID, err := public_ip.SelectInstancePort(ports, instanceID)
		if err != nil {
			diags.AddError("Failed to Resolve Instance Port", err.Error())
			return "", false
		}
		return portID, true
	}

	portID := plan.PortID.ValueString()
	if !slices.Contains(ports, portID) {
		diags.AddAttributeError(
			path.Root("port_id"),
			"Port Is Not On This Instance",
			fmt.Sprintf("`port_id` is %s, which is not one of instance %s's network ports (%s). The "+
				"address would be bound to a port this configuration does not describe, and the next "+
				"refresh would drop the association out of state. Nothing was changed.",
				portID, instanceID, portList(ports)),
		)
		return "", false
	}
	return portID, true
}

// reportUnknownAssociateOutcome decides what to record when the wait gave up
// with the associate still in flight.
//
// The wait is bounded; the WRITE is not. An associate that merely took longer
// than the provider waits has still happened, and erroring without recording it
// strands a live attachment that no later apply can re-create: the associate is
// not idempotent, so every subsequent apply is refused with 409 "Public IP is
// already associated" — for ever, and with a diagnostic that would mention
// neither state nor import. So the platform is asked once more before anything
// is concluded, and when that cannot settle it either, the practitioner gets
// the exact import command rather than a bare timeout.
func (r *publicIPAssociationResource) reportUnknownAssociateOutcome(ctx context.Context, resp *resource.CreateResponse, plan PublicIPAssociationModel, publicIPID, instanceID, portID string, waitErr error) {
	if fip, err := r.readPublicIP(ctx, publicIPID); err == nil && fip.PortID == portID {
		resp.Diagnostics.AddWarning(
			"Public IP Association Took Longer Than The Provider Waited",
			fmt.Sprintf("The wait for the association of public IP %s with instance %s gave up, but the "+
				"address is now attached to port %s — the attachment WAS made, and it has been recorded "+
				"in Terraform state. Nothing further is needed.\n\nThe wait gave up with: %s",
				publicIPID, instanceID, portID, waitErr.Error()),
		)
		r.record(ctx, resp, plan, publicIPID, instanceID, portID)
		return
	}

	resp.Diagnostics.AddError(
		"Public IP Association Outcome Unknown",
		fmt.Sprintf("The attach of public IP %s to port %s (instance %s) was accepted by the platform, "+
			"but it had not finished when this provider stopped waiting, and reading the address back "+
			"did not settle it either. It may yet succeed.\n\nNOTHING has been recorded in Terraform "+
			"state, and the attach is NOT idempotent: if it does complete, a later apply will be "+
			"refused with `409 Public IP is already associated` until state matches reality. Check "+
			"where the address ended up — the portal, or `fm network public-ip list` — then either:\n\n"+
			"  * it is attached: `terraform import frostmoln_public_ip_association.<name> %s/%s`\n"+
			"  * it is not: apply again.\n\nThe wait gave up with: %s",
			publicIPID, portID, instanceID, publicIPID, instanceID, waitErr.Error()),
	)
}

// alreadyAttachedAdvice says what to do about an address that is attached
// somewhere other than where this configuration wants it. A port of the SAME
// instance is a different problem from a port of something else — one is a
// multi-NIC choice this resource can express, the other is a second owner.
func alreadyAttachedAdvice(boundPort, wantPort, instanceID string, instancePorts []string) string {
	if slices.Contains(instancePorts, boundPort) {
		return fmt.Sprintf("Port %s belongs to instance %s too, so the address is already on this "+
			"instance — on a different interface. Set `port_id = %q` to describe the attachment that "+
			"exists (and `terraform import frostmoln_public_ip_association.<name> ...` to adopt it), or "+
			"detach the address first if it belongs on port %s.", boundPort, instanceID, boundPort, wantPort)
	}
	return "Detach it where it is attached now, or check that no `frostmoln_public_ip` resource sets " +
		"`instance_id` for this address — that attribute and this resource manage the same attachment, " +
		"and a configuration using both never converges."
}

// portList renders an instance's ports for a diagnostic.
func portList(ports []string) string {
	if len(ports) == 0 {
		return "it has none"
	}
	return strings.Join(ports, ", ")
}

func (r *publicIPAssociationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state PublicIPAssociationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	publicIPID := state.PublicIPID.ValueString()
	instanceID := state.InstanceID.ValueString()

	fip, err := r.readPublicIP(ctx, publicIPID)
	if err != nil {
		if client.IsNotFound(err) {
			// The ADDRESS is gone, so the attachment is too.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Public IP", err.Error())
		return
	}

	// Nothing holds the address any more.
	if fip.PortID == "" {
		resp.State.RemoveResource(ctx)
		return
	}

	// The address holds SOMETHING — but this resource only exists while that
	// something is one of this instance's ports. The whole port set is compared,
	// not just the first one, because an instance can have more than one and the
	// address does not have to be on the first.
	ports, err := public_ip.InstancePortIDs(ctx, r.client, instanceID)
	if err != nil {
		if client.IsNotFound(err) {
			// The instance is gone, so this association cannot exist. The
			// address survives — it is not this resource's to release.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Instance", err.Error())
		return
	}

	if !slices.Contains(ports, fip.PortID) {
		// The address moved to something else out of band.
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(compositeID(publicIPID, instanceID))
	state.PortID = types.StringValue(fip.PortID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is unreachable: every configurable attribute forces replacement and
// the rest are computed. It is implemented to fail loudly rather than silently
// record a plan it did not carry out, should that ever stop being true.
func (r *publicIPAssociationResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"A public IP association cannot be updated in place: the platform has no re-point operation, "+
			"so every attribute change requires replacement (disassociate, then associate).",
	)
}

func (r *publicIPAssociationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state PublicIPAssociationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	publicIPID := state.PublicIPID.ValueString()

	// Look before detaching. `disassociate` takes no port and simply removes
	// whatever binding the address currently has, so issuing it blind would take
	// the address off a port this resource never attached it to — and that port
	// belongs to a running instance that loses its inbound address with no plan
	// line anywhere saying so. The address itself is never released here.
	fip, err := r.readPublicIP(ctx, publicIPID)
	if err != nil {
		if client.IsNotFound(err) {
			// The address is gone; there is nothing left to detach.
			return
		}
		resp.Diagnostics.AddError("Failed to Read Public IP", err.Error())
		return
	}
	if fip.PortID == "" {
		// NO PORT IS NOT NO ATTACHMENT. An address re-purposed as a VPC's
		// outbound source has no port either — that is the whole reason the
		// platform ships an explicit `attachment` — so treating an empty
		// `portId` as "already detached, desired end state reached" would report
		// a clean destroy for an address a whole VPC is egressing through.
		if fip.heldByNonPort() {
			resp.Diagnostics.AddWarning(
				"Public IP Is Attached To Something Else",
				fmt.Sprintf("Public IP %s is not attached to any network port, so this association no "+
					"longer exists and has been removed from Terraform state — but the address is NOT "+
					"idle: the platform reports it held by %s. Nothing was disassociated. The address "+
					"is untouched and still allocated to your tenant.",
					publicIPID, fip.holderDescription()),
			)
			return
		}
		// Nothing holds it: already detached, which is the desired end state.
		return
	}

	// Something holds the address. Detach it only if it is still THIS
	// association's binding.
	if bound := state.PortID.ValueString(); bound != "" {
		if fip.PortID != bound {
			resp.Diagnostics.AddWarning(
				"Public IP Is Attached To Something Else",
				fmt.Sprintf("Public IP %s is no longer attached to port %s (the port this association bound "+
					"it to); the platform reports port %s. It was re-attached outside this configuration, so "+
					"nothing was disassociated — detaching it here would take the address away from whatever "+
					"holds it now. The association has been removed from Terraform state; the address is "+
					"untouched and still allocated to your tenant.",
					publicIPID, bound, fip.PortID),
			)
			return
		}
	} else {
		// The row does not say which port this association bound — an imported
		// row destroyed before its first refresh. That is the ONE case where
		// ownership is unknown, and so the last one that may fall through to a
		// blind disassociate. Ownership is established the way Read establishes
		// it instead: the binding is this association's only while it sits on
		// one of the instance's ports.
		instanceID := state.InstanceID.ValueString()
		ports, portsErr := public_ip.InstancePortIDs(ctx, r.client, instanceID)
		if portsErr != nil && !client.IsNotFound(portsErr) {
			public_ip.AddPortResolutionError(&resp.Diagnostics, instanceID, portsErr)
			return
		}
		// A missing instance holds nothing, so a binding that survives it is
		// somebody else's — ports is nil and the check below refuses.
		if !slices.Contains(ports, fip.PortID) {
			resp.Diagnostics.AddWarning(
				"Public IP Is Attached To Something Else",
				fmt.Sprintf("Public IP %s is attached to port %s, which is not one of instance %s's "+
					"network ports, and this state row does not record which port the association bound. "+
					"Nothing was disassociated — detaching it here would take the address away from "+
					"whatever holds it now. The association has been removed from Terraform state; the "+
					"address is untouched and still allocated to your tenant.",
					publicIPID, fip.PortID, instanceID),
			)
			return
		}
	}

	disResp, err := r.client.Post(ctx, r.client.TenantPath(fmt.Sprintf("/public-ips/%s/disassociate", publicIPID)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		public_ip.AddAPIError(&resp.Diagnostics, "Failed to Disassociate Public IP", err)
		return
	}
	// A 202 IS NOT A DETACH. Returning here would drop the row from state while
	// the workflow is still running, and a refusal would then land on an
	// operation nobody reads — leaving the address attached with nothing in
	// Terraform that knows about it. Fail the apply instead; the row stays,
	// which is the correct record.
	if _, err := r.waitForOperation(ctx, disResp); err != nil {
		public_ip.AddOperationError(&resp.Diagnostics, "Public IP Disassociation Failed", err)
		return
	}
}

func (r *publicIPAssociationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID in the format {public_ip_id}/{instance_id}, got: %s", req.ID),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("public_ip_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), parts[1])...)
}
