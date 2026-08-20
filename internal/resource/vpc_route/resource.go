package vpc_route

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &vpcRouteResource{}
	_ resource.ResourceWithImportState = &vpcRouteResource{}
)

// writeConflictRetryInterval and writeConflictRetryTimeout bound the retry of
// the ONE transient route refusal.
//
// Short, because ROUTE_WRITE_CONFLICT means the service already exhausted its
// own compare-and-swap retries against a route set other requests kept moving.
// Waiting minutes for that to settle is not a better answer than telling the
// practitioner to run apply again.
const (
	writeConflictRetryInterval = 2 * time.Second
	writeConflictRetryTimeout  = 30 * time.Second
)

type vpcRouteResource struct {
	client *client.Client
}

// NewResource returns a new VPC route resource.
func NewResource() resource.Resource {
	return &vpcRouteResource{}
}

func (r *vpcRouteResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_route"
}

func (r *vpcRouteResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages one static route on a VPC in the Frostmoln Cloud Platform.\n\n" +
			"ONE RESOURCE IS ONE ROUTE, deliberately. A whole-table resource would be authoritative " +
			"over a set that also contains platform-owned routes the tenant can neither see nor " +
			"delete, so it would fight the platform and show a diff that never converges.\n\n" +
			"A route sends traffic for `destination` to `next_hop`. The next hop is an address on a " +
			"subnet attached to the VPC, or the reserved token `internet`, which means \"out this " +
			"VPC's own internet gateway\". `next_hop` is therefore NOT validated as an IP address.\n\n" +
			"A route is matched on its DESTINATION ONLY — a VPC router has no per-source routing — " +
			"so a route applies to every instance in the VPC, not to one of them. That is what makes " +
			"the forced-tunnel pattern (`0.0.0.0/0` to an appliance, plus `<peer>/32` to `internet` " +
			"so the appliance's own tunnel still works) take every instance off the tunnel for the " +
			"peer's address, not just the appliance.\n\n" +
			"Platform-owned routes are invisible here: they are not listed, cannot be created, and " +
			"cannot be imported.\n\n" +
			"Every attribute forces replacement. A route has no server-side identity beyond its " +
			"destination and there is no in-place update for one, so a change destroys and " +
			"re-creates the route.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The identifier of the route, `{vpc_id}/{destination}`. A route has no " +
					"server-side id: a destination is unique within a VPC and identifies it.",
				Computed: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"vpc_id": schema.StringAttribute{
				Description: "The ID of the VPC this route belongs to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"destination": schema.StringAttribute{
				Description: "Destination CIDR, in canonical (masked) form — `203.0.113.0/24`, not " +
					"`203.0.113.5/24`. A default route (`0.0.0.0/0`, `::/0`) is permitted. A " +
					"destination that falls inside a platform route, or inside a subnet attached to " +
					"this VPC, is refused.",
				Required: true,
				Validators: []validator.String{
					canonicalPrefixValidator{},
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"next_hop": schema.StringAttribute{
				Description: "Next hop: an address on a subnet attached to this VPC, of the same " +
					"address family as `destination` — or the reserved token `internet`.\n\n" +
					"`internet` is resolved by the platform when the route is written and is read " +
					"back as the token, never as the address it resolved to, so a gateway rebuild " +
					"cannot leave a stale literal in state. It requires the VPC to have an internet " +
					"gateway; add a `frostmoln_gateway` and let this route depend on it.\n\n" +
					"Traffic sent via `internet` leaves through the platform's own gateway and is " +
					"source-NAT'd to an address the platform does not publish. If the far end pins " +
					"or allow-lists your source address, attach a public IP to the instance instead.",
				Required: true,
				Validators: []validator.String{
					// NO IP-ADDRESS VALIDATOR. `internet` is a legal value, and
					// which addresses are legal depends on the VPC's attached
					// subnets — knowledge only the service has. This validator
					// only rejects a spelling that would read back differently.
					canonicalAddressOrTokenValidator{},
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
		},
	}
}

func (r *vpcRouteResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *vpcRouteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan VPCRouteModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vpcID := plan.VPCID.ValueString()

	// PostWithConflictRetry with a code-specific predicate, NOT client.IsConflict:
	// this surface answers 409 for five permanent refusals as well, and a
	// blanket conflict retry would spin on a reserved destination for the whole
	// budget and then report a timeout instead of the refusal.
	routesPath, pathErr := r.routesPath(vpcID)
	if pathErr != nil {
		resp.Diagnostics.AddError("Invalid VPC ID", pathErr.Error())
		return
	}

	apiResp, err := r.client.PostWithConflictRetry(
		ctx, routesPath, plan.toCreateRequest(), isRouteWriteConflict,
		writeConflictRetryInterval, writeConflictRetryTimeout,
	)
	if err != nil {
		addRouteError(&resp.Diagnostics, "Failed to Create VPC Route", err)
		return
	}

	// The write answers with the tenant's WHOLE route set, not the one route, so
	// the created route is found in it rather than parsed from the body.
	route, findErr := findRoute(apiResp.Body, plan.Destination.ValueString())
	if findErr != nil {
		resp.Diagnostics.AddError("Failed to Parse VPC Route Response", findErr.Error())
		return
	}
	if route == nil {
		resp.Diagnostics.AddError(
			"VPC Route Not Present After Creation",
			fmt.Sprintf("the route to %s is not in the route set the VPC %s returned after the "+
				"create succeeded", plan.Destination.ValueString(), vpcID),
		)
		return
	}

	plan.fromAPI(vpcID, route)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *vpcRouteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state VPCRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vpcID := state.VPCID.ValueString()

	routesPath, pathErr := r.routesPath(vpcID)
	if pathErr != nil {
		resp.Diagnostics.AddError("Invalid VPC ID", pathErr.Error())
		return
	}

	apiResp, err := r.client.Get(ctx, routesPath, nil)
	if err != nil {
		if !client.IsNotFound(err) {
			addRouteError(&resp.Diagnostics, "Failed to Read VPC Routes", err)
			return
		}
		// A 404 HERE IS NOT ENOUGH TO DROP ANYTHING. The list operation answers
		// the plain `not_found` both when the VPC is gone — real parent drift,
		// where leaving state is correct — and when this deployment does not
		// serve route management at all, where dropping the resource would
		// silently destroy state over a configuration flag. The two are
		// indistinguishable in the body, so ask the VPC itself.
		if r.vpcExists(ctx, vpcID) {
			// NAMES BOTH CAUSES. The VPC is there but its route collection
			// answers 404, and the two things that produce that are
			// indistinguishable on the wire: route management is switched off
			// for this deployment, or this VPC has no router and therefore no
			// route table. Erroring is right either way — dropping the resource
			// would destroy state over a configuration flag — but a diagnostic
			// that asserts only the first leaves the practitioner with a
			// resource that fails every refresh and no way out, so say both and
			// give the escape hatch.
			resp.Diagnostics.AddError(
				"This VPC's routes could not be read",
				"The VPC exists, but its route collection answered 404. Two causes are possible "+
					"and cannot be told apart from here:\n\n"+
					"  - this deployment does not have VPC route management enabled — ask your "+
					"operator;\n"+
					"  - this VPC has no route table, which means it has no router.\n\n"+
					"If the VPC genuinely lost its router, remove this resource from state with "+
					"`terraform state rm` rather than letting every refresh fail.\n\n"+err.Error(),
			)
			return
		}
		resp.State.RemoveResource(ctx)
		return
	}

	route, findErr := findRoute(apiResp.Body, state.Destination.ValueString())
	if findErr != nil {
		resp.Diagnostics.AddError("Failed to Parse VPC Routes Response", findErr.Error())
		return
	}
	if route == nil {
		// The route is gone — deleted outside Terraform. This is the case
		// ROUTE_NOT_FOUND names on the single-route operations, and the only one
		// that legitimately drops the resource from state.
		resp.State.RemoveResource(ctx)
		return
	}

	state.fromAPI(vpcID, route)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *vpcRouteResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Every attribute is RequiresReplace, so this is unreachable.
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"A VPC route has no in-place update. All changes require replacement.",
	)
}

func (r *vpcRouteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state VPCRouteModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The destination travels as a QUERY parameter. A CIDR contains a slash, so
	// a path-segment shape is unroutable end to end: the gateway rejects a
	// %2F-encoded segment on a routing-significant prefix before routing, and
	// gin matches the decoded path, so the slash can never fit one segment.
	query := url.Values{}
	query.Set("destination", state.Destination.ValueString())

	routesPath, pathErr := r.routesPath(state.VPCID.ValueString())
	if pathErr != nil {
		resp.Diagnostics.AddError("Invalid VPC ID", pathErr.Error())
		return
	}

	deadline := time.Now().Add(writeConflictRetryTimeout)
	for {
		_, err := r.client.DeleteWithQuery(ctx, routesPath, query)
		if err == nil {
			return
		}
		// ROUTE_NOT_FOUND: the route is already gone, which is the state a
		// delete is trying to reach.
		if isRouteNotFound(err) {
			return
		}
		// A PLAIN 404 IS NOT ENOUGH TO CALL THIS DONE, and Read learned that
		// lesson first. Three different things answer a plain `not_found` on
		// this collection and only ONE of them means the route is gone:
		//
		//   - the VPC is gone, which takes its routes with it — done;
		//   - this deployment has route management switched off, which answers
		//     404 for every verb deliberately;
		//   - the request never reached the route handler at all.
		//
		// Swallowing all three makes `terraform destroy` print "Destroy
		// complete", drop the resource from state, and leave the route
		// installed on the VPC — a leak with no record of it anywhere. So ask
		// the VPC, exactly as Read does. vpcExists fails safe: anything other
		// than a definite 404 counts as "still there", so an unrelated failure
		// surfaces rather than silently succeeding.
		if client.IsNotFound(err) {
			if !r.vpcExists(ctx, state.VPCID.ValueString()) {
				return
			}
			addRouteError(&resp.Diagnostics, "Failed to Delete VPC Route", err)
			return
		}
		if !isRouteWriteConflict(err) || !time.Now().Before(deadline) {
			addRouteError(&resp.Diagnostics, "Failed to Delete VPC Route", err)
			return
		}
		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError("Failed to Delete VPC Route", ctx.Err().Error())
			return
		case <-time.After(writeConflictRetryInterval):
		}
	}
}

func (r *vpcRouteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// Import ID format: {vpc_id}/{destination}.
	//
	// SplitN with n=2 is load-bearing: the destination is a CIDR and contains a
	// slash of its own, so only the FIRST separator may be treated as one.
	parts := strings.SplitN(req.ID, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("Expected import ID format: {vpc_id}/{destination}, got: %s", req.ID),
		)
		return
	}
	if err := validVPCID(parts[0]); err != nil {
		// Caught HERE rather than left to the following Read, so the diagnostic
		// points at the import id the practitioner typed instead of at a
		// resource they have not written yet.
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("%q is not a usable VPC ID: %s. Expected import ID format: "+
				"{vpc_id}/{destination}.", parts[0], err),
		)
		return
	}
	if _, err := netip.ParsePrefix(parts[1]); err != nil {
		resp.Diagnostics.AddError(
			"Invalid Import ID",
			fmt.Sprintf("%q is not a destination CIDR. Expected import ID format: "+
				"{vpc_id}/{destination}, e.g. \"vpc-abc123/203.0.113.0/24\".", parts[1]),
		)
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("vpc_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("destination"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), buildID(parts[0], parts[1]))...)
}

// routesPath is the VPC-SCOPED route collection.
//
// NEVER the router-scoped surface: that one returns the ADDRESS the `internet`
// token resolved to rather than the token, so a converging client would see a
// diff on every plan and never settle.
//
// It returns an error rather than escaping, because url.PathEscape does NOT
// give the "one path segment" guarantee it looks like it gives. The client
// joins with path.Join, which CLEANS the URL, and "." and ".." are unreserved
// so the escape leaves them intact. Measured:
//
//	vpcID "."  -> /v1/tenants/{t}/vpcs/routes    (the DELETE VPC endpoint)
//	vpcID ".." -> /v1/tenants/{t}/routes
//
// A "." id would therefore turn this resource's Delete into a DELETE against a
// VPC whose id is "routes". Escaping cannot prevent that; refusing the value
// can.
func (r *vpcRouteResource) routesPath(vpcID string) (string, error) {
	if err := validVPCID(vpcID); err != nil {
		return "", err
	}
	return r.client.TenantPath(fmt.Sprintf("/vpcs/%s/routes", vpcID)), nil
}

// validVPCID refuses an id that cannot safely be one path segment.
func validVPCID(vpcID string) error {
	if vpcID == "" {
		return fmt.Errorf("a VPC ID is required")
	}
	if vpcID == "." || vpcID == ".." || strings.ContainsAny(vpcID, `/\?#%`) {
		return fmt.Errorf("invalid VPC ID %q", vpcID)
	}
	return nil
}

// vpcExists reports whether the VPC itself is still there, to tell real parent
// drift from a deployment that does not serve route management.
//
// Read as a conservative gate: anything other than a definite 404 on the VPC
// counts as "still there", so an unrelated failure never becomes a reason to
// drop a resource from state.
func (r *vpcRouteResource) vpcExists(ctx context.Context, vpcID string) bool {
	if err := validVPCID(vpcID); err != nil {
		// Not a usable id, so nothing can be concluded about the VPC. Fail
		// safe: never a reason to drop a resource from state.
		return true
	}
	_, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf("/vpcs/%s", vpcID)), nil)
	return !client.IsNotFound(err)
}

// findRoute picks one route out of a route-set body by destination.
//
// Matching is on the PARSED prefix, not the string: the service stores and
// returns the masked, canonical spelling, so a value that differs only in form
// must still match rather than read as a missing route — which would drop the
// resource from state and recreate a route that never went anywhere.
func findRoute(body []byte, destination string) (*apiVPCRoute, error) {
	var list apiVPCRouteList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("failed to parse route set: %w", err)
	}
	for i := range list.Routes {
		if samePrefix(list.Routes[i].Destination, destination) {
			return &list.Routes[i], nil
		}
	}
	return nil, nil
}

// samePrefix compares two CIDR strings on the parsed prefix, falling back to a
// textual compare when either does not parse. Mirrors domain.SamePrefix in the
// network service, for the same reason it exists there.
func samePrefix(a, b string) bool {
	pa, ea := netip.ParsePrefix(a)
	pb, eb := netip.ParsePrefix(b)
	if ea != nil || eb != nil {
		return a == b
	}
	return pa.Masked() == pb.Masked()
}
