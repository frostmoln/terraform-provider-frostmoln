package egress_gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &egressGatewayResource{}
	_ resource.ResourceWithImportState = &egressGatewayResource{}
	_ resource.ResourceWithModifyPlan  = &egressGatewayResource{}

	_ validator.String = modeValidator{}
)

type egressGatewayResource struct {
	client *client.Client
}

// NewResource returns a new egress gateway resource.
func NewResource() resource.Resource {
	return &egressGatewayResource{}
}

func (r *egressGatewayResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_egress_gateway"
}

func (r *egressGatewayResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a VPC's outbound internet path. A VPC has at most one egress " +
			"gateway; a VPC without one is an isolated network with no inbound and no " +
			"outbound connectivity. " + connectivityLossWarning,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The gateway's identifier. It survives a mode change, so it is stable " +
					"for the life of the gateway. For a gateway with no stored record " +
					"(`origin` = \"legacy\") the API answers under the VPC's id instead.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC this gateway serves. Changing it replaces the resource — and the " +
					"replacement destroys this gateway first, which is refused unless " +
					"`acknowledge_connectivity_loss = true` is already in the configuration. Set it, " +
					"apply, and only then change `vpc_id`.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"mode": schema.StringAttribute{
				Description: "How outbound traffic is addressed. `\"public_ip\"` is the only value " +
					"a configuration can set: the VPC gets its own outbound gateway as its " +
					"outbound path. Leave `public_ip_id` out and the platform draws the gateway an " +
					"address of its own — not a public IP of yours, and not pinned; name one and the " +
					"VPC egresses from an address of your own, which is what a partner allow-list " +
					"entry or a DNS record needs.\n\n" +
					"There is no default and no third value. A VPC with NO egress gateway — this " +
					"resource simply not declared — is an isolated network, and that is how \"no " +
					"connectivity\" is expressed: connectivity is a stated choice, never one a VPC " +
					"acquires because a field was omitted.\n\n" +
					"`\"nat\"`, which sent a VPC's outbound traffic through an address shared with " +
					"other VPCs, is **WITHDRAWN** and is refused. It could not coexist with public " +
					"IPs on instances in the same VPC, which is why it is gone. A gateway created " +
					"before the withdrawal still REPORTS `\"nat\"`, and Terraform reads, refreshes and " +
					"imports it normally — but not while the CONFIGURATION still says `\"nat\"`, which " +
					"is refused at validate time and stops all of those. Set `mode = \"public_ip\"` " +
					"(with `acknowledge_connectivity_loss = true`) to move it off, which is applied in " +
					"place.\n\n" +
					"Terraform uses the wire spelling `public_ip`. The `fm` CLI additionally accepts " +
					"the hyphenated `public-ip` and normalises it, so a value copied from a CLI " +
					"command has to be written with the underscore here.\n\n" +
					"Changing the mode is applied IN PLACE, never as a destroy/create — a " +
					"replacement would drop the recorded source address and leave the VPC with no " +
					"internet, no DNS and no managed-service connectivity in between. It still " +
					"re-addresses the VPC's egress and drops in-flight connections, so it requires " +
					"`acknowledge_connectivity_loss = true`, and `source_address` is planned as " +
					"\"(known after apply)\" because the platform re-records it.",
				Required: true,
				// Deliberately NOT RequiresReplace: a mode change is an in-place
				// PATCH. The API has that endpoint precisely so the recorded
				// source address survives and the tenant is not taken off-net
				// between a destroy and a create.
				Validators: []validator.String{
					// One validator, not OneOf(ModePublicIP) plus a withdrawal
					// check: a config that says `nat` would then collect TWO
					// diagnostics, and the generic "value must be one of" is the
					// one that reads like the whole story. It is not — `nat` is
					// not a typo, it is a mode that existed, and the practitioner
					// needs to be told that and told what replaces it.
					modeValidator{},
				},
			},
			"public_ip_id": schema.StringAttribute{
				Description: "Under `mode = \"public_ip\"`, the public IP (`frostmoln_public_ip`) this " +
					"VPC's outbound traffic leaves from. Naming one is what makes the address STABLE: " +
					"it is a resource of your tenant's, so it survives this gateway being rebuilt, the " +
					"VPC being recreated, and the address being handed to a partner to allow-list or " +
					"published in DNS.\n\n" +
					"Omit it and the platform draws the gateway an address itself. That address is NOT a " +
					"public IP of yours: it has no id, it does not appear in your public IP list, it draws " +
					"on none of your public IP quota, and nothing pins it — it may change if the gateway is " +
					"rebuilt. It is the right default for a VPC whose source address nobody outside it " +
					"needs to know, and this attribute stays null there because there is no public IP for " +
					"the platform to name.\n\n" +
					"Naming an address is the option you take when something outside the VPC DOES need to " +
					"know it. The address is then a resource of your tenant's — listed, and counted " +
					"against your public IP quota like any other — and it is pinned as this gateway's " +
					"source address.\n\n" +
					"A gateway left on the WITHDRAWN \"nat\" mode has no public IP at all, so this reads " +
					"as null there.\n\n" +
					"Changing it is applied IN PLACE, never as a destroy/create: the platform " +
					"re-addresses the existing gateway rather than tearing the VPC's only outbound path " +
					"down and building a new one. It still changes the address the VPC's traffic arrives " +
					"from, so it requires `acknowledge_connectivity_loss = true` exactly as a `mode` " +
					"change does.\n\n" +
					"REMOVING this attribute from the configuration does not release the address, and does " +
					"not hand the gateway back to a platform-drawn one. The attribute is Computed as well " +
					"as Optional, so an absent value resolves to the id already in state: there is no " +
					"diff, no request is sent, and the gateway goes on egressing from the address you " +
					"named. The API reads an absent `publicIpId` the same way — as \"keep the address " +
					"this gateway has\" — so there is no way back to a platform-drawn address once one " +
					"is named. To move the gateway to a different address, name the different address.\n\n" +
					"The public IP must belong to this tenant and must not be attached to anything else. " +
					"A public IP that is in use by an instance is refused, and so is one already serving " +
					"another VPC's egress.",
				Optional: true,
				Computed: true,
				// UseStateForUnknown ONLY. No RequiresReplace: re-pointing the
				// gateway is an in-place PATCH, and a replacement would destroy
				// the VPC's outbound path (and with it DNS and managed-service
				// connectivity) to rebuild it — the opposite of the stable-address
				// promise this attribute exists to make.
				//
				// The modifier is what keeps the omitted case free of a perpetual
				// diff: with a null config the framework marks the planned value
				// unknown whenever state holds a null, and null-versus-unknown is
				// a diff on every run. Resolving it back to the state value (null
				// included) leaves nothing to plan. Where the apply legitimately
				// recomputes the value, ModifyPlan puts the unknown back.
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"acknowledge_connectivity_loss": schema.BoolAttribute{
				Description: "Set to true to allow this gateway to be removed, or its `mode` or " +
					"`public_ip_id` to be changed. " + connectivityLossWarning + "\n\nThe API refuses " +
					"the removal and the mode change " +
					"without the acknowledgement, so this provider refuses them too — before any " +
					"request is sent — rather than supplying the flag on the practitioner's behalf. It " +
					"applies the same rule to a `public_ip_id` change, which re-addresses the VPC's " +
					"egress just as visibly. " +
					"Set it to true and apply, then destroy or change the mode. Leaving it unset " +
					"(the default) makes `terraform destroy` fail on this resource instead of " +
					"silently disconnecting the VPC.\n\n" +
					"**Remove it from the configuration again once the removal or mode change is " +
					"done, and do not leave it set on a steady-state resource.** It is ordinary " +
					"configuration, so a `true` left behind stays in state for the life of the " +
					"resource and permanently disarms this control: a later `terraform destroy " +
					"-target`, a module removal, or a `vpc_id` change that forces replacement then " +
					"disconnects the VPC with nothing in the plan beyond an ordinary \"will be " +
					"destroyed\" line.",
				Optional: true,
			},
			"source_address": schema.StringAttribute{
				Description: "The public IPv4 address outbound traffic appears to come from. Null " +
					"while the gateway is detached or the address is not yet known. It is dedicated to " +
					"this VPC — no other VPC egresses from it — but dedicated is not the same as " +
					"STABLE. Where `public_ip_id` names an address, this IS that address and it is " +
					"pinned. Where it does not, this is the address the platform drew for the gateway: " +
					"nothing pins it, so it may change if the gateway is rebuilt, and it is not an " +
					"address to publish or hand a partner to allow-list. On a gateway left on the " +
					"withdrawn \"nat\" mode it is an address shared with other VPCs. A `mode` or " +
					"`public_ip_id` change re-records it, so it plans as \"(known after apply)\" then " +
					"and keeps its recorded value otherwise.",
				Computed: true,
			},
			"status": schema.StringAttribute{
				Description: "Observed state of the gateway, read from the cloud rather than from " +
					"stored desired state: \"active\" or \"detached\". It is not configurable — " +
					"Terraform records whatever the platform reports and produces no plan of its own " +
					"from it. \"detached\" is not on its own a verdict that something is broken or " +
					"that an operator is needed: what the platform observes depends on how the mode " +
					"is realised. Read it as a prompt to check the VPC's outbound path, not as proof " +
					"that the platform and the cloud disagree.",
				Computed: true,
			},
			"origin": schema.StringAttribute{
				Description: "Who asked for the gateway: \"explicit\" (a client created it " +
					"directly), \"explicit_public_ip\" (a client created it and named the public IP it " +
					"egresses from, via `public_ip_id`), \"implicit_public_ip\" (the platform attached " +
					"it because a public " +
					"IP was associated, which requires a gateway), \"vpc_create\" (it came with the " +
					"VPC because a mode was chosen at VPC create), or \"legacy\" — which means NO " +
					"STORED RECORD EXISTS, so the provenance is UNKNOWN. \"legacy\" usually means " +
					"the VPC predates this resource, but the record write is deliberately " +
					"non-fatal, so a gateway created minutes ago can report it too. Read it as " +
					"\"unknown\", never as \"old\".",
				Computed: true,
			},
		},
	}
}

func (r *egressGatewayResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", "expected *client.Client")
		return
	}
	r.client = c
}

// modeValidator accepts the one mode a configuration may set, and refuses the
// WITHDRAWN one by name.
//
// It exists instead of stringvalidator.OneOf(ModePublicIP) for the `nat` case
// alone. OneOf renders "value must be one of: [\"public_ip\"]", which reads as
// "you made that up" — and `nat` was a real, documented, applied mode, so the
// practitioner reading it has a working configuration that has just stopped
// validating and no idea why. The diagnostic has to say the mode is withdrawn,
// say what replaces it, and say that the gateway they already have still reads.
//
// It also subsumes the `public_ip_id` + `mode = "nat"` pair check this resource
// used to run in ValidateConfig: the pair is unreachable once `nat` itself is
// refused, and a second diagnostic saying "public_ip_id is only meaningful with
// public_ip" would imply the mode was otherwise fine.
//
// A null or unknown value is not judged — the framework does not call a
// validator for either, and an unknown `mode` (a module output, another
// resource's attribute) that resolves to `nat` during the apply is refused by
// the API instead (see errCodeModeUnavailable).
type modeValidator struct{}

func (modeValidator) Description(_ context.Context) string {
	return fmt.Sprintf("value must be %q (%q is withdrawn and cannot be set)", ModePublicIP, ModeNAT)
}

func (v modeValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (modeValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}

	switch req.ConfigValue.ValueString() {
	case ModePublicIP:
		return
	case ModeNAT:
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Egress gateway mode \"nat\" has been withdrawn",
			"`mode = \"nat\"` sent this VPC's outbound traffic through an address shared with other "+
				"VPCs. The mode is no longer offered and cannot be set: a VPC on it could not give any "+
				"of its instances a public IP, because the platform needs the VPC's own gateway to "+
				"attach one.\n\nUse `mode = \"public_ip\"`, which gives the VPC its own outbound "+
				"gateway. Name a `public_ip_id` to egress from an address of your own — the one to give "+
				"a partner for an allow-list or publish in DNS — or leave it out and the platform picks "+
				"the address.\n\nA VPC that should have NO outbound path at all does not need a "+
				"different mode: remove this resource entirely (which requires "+
				"`acknowledge_connectivity_loss = true`), and the VPC is an isolated network.\n\n"+
				"The GATEWAY you already have is not affected by this refusal: a gateway still on "+
				"\"nat\" keeps working, and Terraform reads, refreshes and imports it normally — the "+
				"provider records whatever mode the platform reports and never rewrites it. What is "+
				"refused is the VALUE IN YOUR CONFIGURATION. This is an attribute validator, so it runs "+
				"wherever Terraform validates the configuration — `terraform validate` and `plan`, and "+
				"the validation `refresh` and `import` do first: while `mode = \"nat\"` is still written "+
				"in the configuration, every one of them stops here. They read the gateway again as soon "+
				"as `mode` is no longer \"nat\" in the configuration.\n\n"+
				"To move it off, set `mode = \"public_ip\"` together with "+
				"`acknowledge_connectivity_loss = true` — the change is applied in place, and it "+
				"re-addresses the VPC's egress.",
		)
	default:
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid egress gateway mode",
			fmt.Sprintf("`mode` must be %q. Got %q.\n\n\"none\" is not a mode: a VPC with no outbound "+
				"path is this resource NOT DECLARED, not a gateway carrying a special value.",
				ModePublicIP, req.ConfigValue.ValueString()),
		)
	}
}

// ModifyPlan resolves the three attributes the platform recomputes —
// `source_address`, `status` and `origin` — because neither of the framework's
// defaults is right for them.
//
// For a Computed-only attribute the proposed new state carries the PRIOR STATE
// value; the framework only rewrites a null one to unknown. Both halves of that
// are wrong here:
//
//   - On a `mode` change the plan would pin the OLD values as known while the
//     PATCH legitimately returns new ones — the platform re-records the source
//     address, sets `origin` to "explicit" (so an imported "vpc_create" or
//     "legacy" gateway changes) and reports the gateway active. Terraform core
//     then aborts the apply with "Provider produced inconsistent result after
//     apply", which no retry clears.
//   - With `mode` unchanged and a null `source_address` in state — a detached
//     gateway, or an external port with no IPv4 — the null is marked unknown,
//     and null-versus-unknown is a diff on every single run. `terraform plan
//     -detailed-exitcode` would return 2 forever, which breaks any drift gate
//     built on it.
//
// So: unknown when the apply will re-address the gateway, and the state value
// (UseStateForUnknown semantics, nulls included) when it will not.
//
// `public_ip_id` is Optional+Computed and gets the same treatment for one case
// its UseStateForUnknown modifier CANNOT cover. Switching mode from "nat" to
// "public_ip" without naming an address leaves a null config against a null
// state, so the modifier would pin the plan to null ACROSS AN APPLY THAT
// RE-ADDRESSES THE GATEWAY — and core rejects any planned value the apply
// contradicts as "Provider produced inconsistent result after apply". Under
// ADR-0114 an unnamed gateway runs on a platform-drawn address and the API
// reports no `publicIpId` for it at all, so null is what comes back today; the
// unknown is what stops the plan asserting that in advance, and keeps this
// attribute consistent with the three observed values above. A configured value
// is never touched here: it is the practitioner's choice, and overwriting it
// with unknown would hide the very change being planned.
func (r *egressGatewayResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// A create marks every null Computed attribute unknown already, and a
	// destroy plan has no attributes to resolve.
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}

	var plan, state, config EgressGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// A vpc_id change is checked explicitly rather than through
	// resp.RequiresReplace: the framework hands ModifyPlan an EMPTY
	// RequiresReplace and only appends what this method adds to what the
	// attribute modifiers already recorded, so reading it here would always see
	// no replacement. vpc_id carries the only RequiresReplace modifier on this
	// resource.
	//
	// A public_ip_id change belongs in the same set: it moves the VPC onto a
	// different address, so source_address is re-recorded exactly as it is on a
	// mode change, and origin becomes "explicit_public_ip".
	//
	// publicIPPinChanged, not a plain Equal: an UNKNOWN planned pin is not a
	// change. It is what an unconfigured Computed attribute looks like before
	// UseStateForUnknown resolves it, and counting it as a change would mark
	// the observed values unknown on a run where nothing is being applied —
	// null-versus-unknown on every plan, forever.
	recomputed := !plan.Mode.Equal(state.Mode) ||
		!plan.VPCID.Equal(state.VPCID) ||
		publicIPPinChanged(plan.PublicIPID, state.PublicIPID)

	for _, attr := range []struct {
		name  string
		state types.String
	}{
		{"source_address", state.SourceAddress},
		{"status", state.Status},
		{"origin", state.Origin},
	} {
		value := attr.state
		if recomputed {
			value = types.StringUnknown()
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root(attr.name), value)...)
	}

	// Only where the practitioner left it out. A configured value is their
	// choice and is already in the plan; overwriting it would hide the very
	// change being planned. Resolving it here rather than leaning on the
	// attribute's UseStateForUnknown makes the outcome independent of the order
	// the framework happens to run the two in.
	if config.PublicIPID.IsNull() {
		value := state.PublicIPID
		if recomputed {
			value = types.StringUnknown()
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("public_ip_id"), value)...)
	}
}

func (r *egressGatewayResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan EgressGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Post(ctx, r.client.TenantPath("/egress-gateways"), apiCreateEgressGatewayRequest{
		VPCID: plan.VPCID.ValueString(),
		Mode:  plan.Mode.ValueString(),
		// Omitted unless the practitioner named one — the platform reads the
		// absent field as "draw the gateway an address yourself", which
		// creates no public IP resource for the tenant.
		PublicIPID: configuredPublicIPID(plan.PublicIPID),
	})
	if err != nil {
		addEgressError(&resp.Diagnostics, "Failed to create egress gateway", err)
		return
	}

	var gw apiEgressGateway
	if err := json.Unmarshal(apiResp.Body, &gw); err != nil {
		resp.Diagnostics.AddError("Failed to parse egress gateway response", err.Error())
		return
	}

	// Checked against the PLAN value, before applyToModel overwrites it with
	// whatever came back.
	resp.Diagnostics.Append(checkRequestedPublicIP(plan.PublicIPID, &gw)...)
	resp.Diagnostics.Append(applyToModel(&plan, &gw)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// configuredPublicIPID returns the id to put on the wire, or "" to omit the
// field. Unknown and null both mean "the practitioner did not choose".
//
// The platform answers an omitted field by drawing the gateway an address of
// its OWN (ADR-0114) — not by creating a public IP for the tenant, and never by
// detaching one. On create that is the platform-drawn address: no id comes back,
// no identity row, no quota, no metering, and nothing pins it. On update it is
// read as "keep the address this gateway has", so an omitted field cannot
// unpin a named one either.
func configuredPublicIPID(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func (r *egressGatewayResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state EgressGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	gw, found, err := r.lookup(ctx, state.VPCID.ValueString(), state.ID.ValueString())
	if err != nil {
		addEgressError(&resp.Diagnostics, "Failed to read egress gateway", err)
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(applyToModel(&state, gw)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// lookup resolves the gateway, preferring the vpcId-filtered list.
//
// The filtered list answers "does this VPC have a gateway" with an empty result
// rather than a 404, so a gateway removed out of band is a clean removal from
// state instead of an error — but an empty result is NOT on its own proof that
// the gateway is gone, so it is confirmed against the object itself before it
// is believed (see below).
//
// It NEVER sends an empty vpcId. `?vpcId=` present-but-empty is a 400 by
// design, and the failure it guards against is worse than that refusal: were the
// parameter simply dropped, the request would return the TENANT-WIDE list, this
// resource would bind to element [0] — some unrelated VPC's gateway — and the
// next apply would PATCH or DELETE that VPC's internet, DNS and managed-service
// path. A state row with no vpc_id (a fresh import by gateway id) therefore
// resolves by id instead, which the API accepts as either the gateway id or the
// VPC id.
func (r *egressGatewayResource) lookup(ctx context.Context, vpcID, id string) (*apiEgressGateway, bool, error) {
	if vpcID == "" {
		if id == "" {
			return nil, false, fmt.Errorf("egress gateway state has neither vpc_id nor id; import it with `terraform import frostmoln_egress_gateway.<name> <vpc id>`")
		}
		return r.getByID(ctx, id)
	}

	q := url.Values{}
	q.Set("vpcId", vpcID)

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/egress-gateways"), q)
	if err != nil {
		return nil, false, err
	}

	var list apiEgressGatewayList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		return nil, false, fmt.Errorf("failed to parse egress gateway response: %w", err)
	}
	if len(list.EgressGateways) == 0 {
		// An empty filtered list is NOT proof that the gateway is gone. The API
		// documents the same empty body for a vpcId that is unknown or that this
		// tenant does not own, so a stale vpc_id — or a key scoped to another
		// tenant — would otherwise drop a live, address-spending gateway out of
		// state, and the next apply would create a SECOND one for the same VPC.
		//
		// GET /egress-gateways/{id} tells the two apart: it 404s only when the
		// object really is not there. It accepts the gateway id or the VPC id,
		// so whichever this state row has works.
		confirmID := id
		if confirmID == "" {
			confirmID = vpcID
		}
		return r.getByID(ctx, confirmID)
	}
	return &list.EgressGateways[0], true, nil
}

// getByID resolves a gateway from GET /egress-gateways/{id}, which accepts the
// gateway id OR the VPC id. Used on the import path, where only one id is known
// and which of the two it is has not been established yet.
func (r *egressGatewayResource) getByID(ctx context.Context, id string) (*apiEgressGateway, bool, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/egress-gateways/"+url.PathEscape(id)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}

	var gw apiEgressGateway
	if err := json.Unmarshal(apiResp.Body, &gw); err != nil {
		return nil, false, fmt.Errorf("failed to parse egress gateway response: %w", err)
	}
	return &gw, true, nil
}

func (r *egressGatewayResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state EgressGatewayModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if state.ID.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Egress gateway has no id in state", noIDDetail)
		return
	}

	// The updatable inputs are `mode` and `public_ip_id`;
	// `acknowledge_connectivity_loss` is a statement of intent the API never
	// stores. Toggling it alone must not send a mutation, so carry the observed
	// values over from state and stop — the next refresh re-reads them anyway.
	modeChanged := !plan.Mode.Equal(state.Mode)
	pinChanged := publicIPPinChanged(plan.PublicIPID, state.PublicIPID)

	if !modeChanged && !pinChanged {
		plan.ID = state.ID
		plan.SourceAddress = state.SourceAddress
		plan.Status = state.Status
		plan.Origin = state.Origin
		plan.PublicIPID = state.PublicIPID
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	// Refuse locally, before any request. The API requires the acknowledgement
	// for a mode change — the one that remains is moving a gateway off the
	// withdrawn `nat` mode, which re-attaches an external gateway — and it is
	// not ceremony: the VPC's egress source changes, in-flight connections drop,
	// and the platform routes are rebuilt.
	//
	// A public_ip_id change is held to the same rule even where the API would
	// take it without one. Re-pointing the gateway is applied in place, but the
	// address the VPC's traffic arrives from still changes: in-flight
	// connections drop, and every partner allow-list and DNS record naming the
	// old address goes stale. That is precisely the kind of change this
	// resource refuses to make on a plan approval alone.
	if !plan.AcknowledgeConnectivityLoss.ValueBool() {
		resp.Diagnostics.AddAttributeError(
			path.Root("acknowledge_connectivity_loss"),
			egressChangeAckSummary(modeChanged),
			egressChangeAckDetail(modeChanged, &plan, &state),
		)
		return
	}

	// PATCH by the gateway's own id, which survives both changes — that is why
	// this is an update rather than a replacement.
	apiResp, err := r.client.Patch(ctx,
		r.client.TenantPath("/egress-gateways/"+url.PathEscape(state.ID.ValueString())),
		apiUpdateEgressGatewayRequest{
			Mode: plan.Mode.ValueString(),
			// Omitted when the practitioner named no address, which the API
			// reads as "keep the address this gateway has" — never as "detach".
			PublicIPID:                  configuredPublicIPID(plan.PublicIPID),
			AcknowledgeConnectivityLoss: true,
		})
	if err != nil {
		addEgressError(&resp.Diagnostics, "Failed to change egress gateway", err)
		return
	}

	var gw apiEgressGateway
	if err := json.Unmarshal(apiResp.Body, &gw); err != nil {
		resp.Diagnostics.AddError("Failed to parse egress gateway response", err.Error())
		return
	}

	resp.Diagnostics.Append(checkRequestedPublicIP(plan.PublicIPID, &gw)...)
	resp.Diagnostics.Append(applyToModel(&plan, &gw)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// publicIPPinChanged reports whether the practitioner moved the gateway onto a
// different address.
//
// Neither an UNKNOWN nor a NULL plan value is a pin change on its own — both
// are shapes of "the practitioner named no address", and only a NAMED address
// can be a change:
//
//   - Unknown arises where ModifyPlan put it there, on a mode or vpc_id change
//     with no configured address. The mode change is what is being applied, and
//     there is no id to send.
//   - Null is what an unconfigured attribute resolves to when state holds none
//     either. Reading it as a change would re-address a gateway on nobody's
//     request, and demand the connectivity acknowledgement for an attribute the
//     practitioner never wrote.
func publicIPPinChanged(plan, state types.String) bool {
	if plan.IsUnknown() || plan.IsNull() {
		return false
	}
	return !plan.Equal(state)
}

func egressChangeAckSummary(modeChanged bool) string {
	if modeChanged {
		return "Egress gateway mode change requires acknowledgement"
	}
	return "Egress gateway address change requires acknowledgement"
}

func egressChangeAckDetail(modeChanged bool, plan, state *EgressGatewayModel) string {
	if modeChanged {
		return fmt.Sprintf("Changing mode from %q to %q re-addresses this VPC's only outbound path and "+
			"drops connections in flight. %s\n\nSet `acknowledge_connectivity_loss = true` on this "+
			"resource to allow the change.",
			state.Mode.ValueString(), plan.Mode.ValueString(), connectivityLossWarning)
	}
	return fmt.Sprintf("Changing `public_ip_id` from %s to %q moves this VPC's egress onto a different "+
		"address. The gateway itself is updated in place, but the address your outbound traffic "+
		"arrives from changes: connections in flight drop, and every partner allow-list entry and DNS "+
		"record naming the old address stops matching.\n\nSet `acknowledge_connectivity_loss = true` "+
		"on this resource to allow the change.",
		quotedOrNone(state.PublicIPID), plan.PublicIPID.ValueString())
}

// quotedOrNone renders a possibly-null id for a diagnostic. A gateway that had
// no recorded public IP is a real starting point (a legacy address, or one still
// on the withdrawn `nat` mode), so it is named as such rather than printed as
// `""`.
func quotedOrNone(v types.String) string {
	if v.IsNull() || v.ValueString() == "" {
		return "no recorded public IP"
	}
	return fmt.Sprintf("%q", v.ValueString())
}

func (r *egressGatewayResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state EgressGatewayModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Refuse an id-less row rather than sending a DELETE that would be cleaned
	// into a request against the COLLECTION — whose answer this method would
	// then read through IsNotFound as "already gone" and silently forget a
	// gateway that is still up.
	if state.ID.ValueString() == "" {
		resp.Diagnostics.AddAttributeError(path.Root("id"), "Egress gateway has no id in state", noIDDetail)
		return
	}

	// A plan approval is NOT the acknowledgement. `terraform destroy` shows this
	// resource being removed and says nothing about DNS or managed-service
	// connectivity going with it, and a gateway is routinely destroyed as a
	// side effect of tearing down something else entirely. The API demands an
	// explicit statement so that no client can disconnect a VPC silently;
	// supplying it unconditionally here would defeat that for every Terraform
	// user, so the refusal is reproduced locally, with the consequence spelled
	// out and no request sent.
	if !state.AcknowledgeConnectivityLoss.ValueBool() {
		resp.Diagnostics.AddAttributeError(
			path.Root("acknowledge_connectivity_loss"),
			"Egress gateway removal requires acknowledgement",
			connectivityLossWarning+"\n\nSet `acknowledge_connectivity_loss = true` on this resource "+
				"and run `terraform apply` first; the destroy then proceeds. Nothing has been changed.",
		)
		return
	}

	// The acknowledgement is a real query parameter — building it into the path
	// string would percent-encode the "?" into the path segment, and the API
	// would answer as if it had never been sent.
	q := url.Values{}
	q.Set("acknowledgeConnectivityLoss", "true")

	_, err := r.client.DeleteWithQuery(ctx,
		r.client.TenantPath("/egress-gateways/"+url.PathEscape(state.ID.ValueString())), q)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		addEgressError(&resp.Diagnostics, "Failed to delete egress gateway", err)
		return
	}
}

func (r *egressGatewayResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Only `id` is set. GET /egress-gateways/{id} accepts the gateway id OR the
	// VPC id, so Read resolves either and fills in vpc_id itself. Setting vpc_id
	// to the imported string as well would be wrong for the gateway-id form: the
	// following Read would filter the list by a vpcId that matches nothing, get
	// an empty result, and drop the freshly imported resource from state.
	//
	// acknowledge_connectivity_loss stays null, so an imported gateway cannot be
	// destroyed until the practitioner states the intent — the safe default.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
