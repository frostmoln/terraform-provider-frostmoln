// Package vpc_routes implements the frostmoln_vpc_routes Terraform data source:
// the read-only listing of one VPC's tenant-visible route table — the drift
// detector the per-route resource cannot be.
package vpc_routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &vpcRoutesDataSource{}

// NewDataSource returns a new frostmoln_vpc_routes data source factory.
func NewDataSource() datasource.DataSource {
	return &vpcRoutesDataSource{}
}

type vpcRoutesDataSource struct {
	client *client.Client
}

// vpcRoutesModel is the Terraform state model for the route listing.
type vpcRoutesModel struct {
	VPCID  types.String `tfsdk:"vpc_id"`
	ID     types.String `tfsdk:"id"`
	Routes types.List   `tfsdk:"routes"`
}

// vpcRouteRowModel is one row of the listing.
type vpcRouteRowModel struct {
	Destination types.String `tfsdk:"destination"`
	NextHop     types.String `tfsdk:"next_hop"`
}

// apiVPCRoute is the API representation of one route on the VPC-scoped surface.
// camelCase `nextHop`: this surface spells the field that way; the older
// router-scoped one spells it `nexthop` and is deliberately never used. It
// mirrors the resource's model (internal/resource/vpc_route) and must stay in
// step with it.
type apiVPCRoute struct {
	Destination string `json:"destination"`
	NextHop     string `json:"nextHop"`
}

// apiVPCRouteList is what the collection answers with — the tenant's WHOLE
// route set, which is exactly what a listing data source wants.
//
// `Routes` is a POINTER so an answer without the set is distinguishable from an
// answer with an EMPTY set. The resource's copy (internal/resource/vpc_route)
// uses a plain slice because it only ever picks one route by destination, where
// the bluntness is safe; here a missing key would otherwise flow through into
// `routes = []`, which every check block over this listing reads as "no drift".
// The service always renders the key, so only something that is not the route
// collection can leave it absent — and that is never a verdict to render.
type apiVPCRouteList struct {
	Routes *[]apiVPCRoute `json:"routes"`
}

func (d *vpcRoutesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vpc_routes"
}

func (d *vpcRoutesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the tenant's routes on one VPC — every row the routes panel and " +
			"`fm network vpc route list` would show, rendered exactly as `frostmoln_vpc_route` " +
			"writes it.\n\n" +

			"This is the drift detector the per-route resource cannot be. `frostmoln_vpc_route` is " +
			"one resource per route, deliberately non-authoritative — no resource owns the table — " +
			"so a route added out of band (in the portal, with `fm`, through the API, or by a " +
			"colleague) is invisible to every plan. A more-specific added route can even silently " +
			"shadow a managed one: longest prefix wins, the plan stays clean, and traffic goes " +
			"where the configuration does not say. This data source READS the set, so out-of-band " +
			"routes appear in it, and a `check` block or `lifecycle` postcondition over it turns " +
			"that difference into a failed plan (see the example and the surface-contract guide).\n\n" +

			"TENANT VIEW ONLY: platform-owned routes — the system routes DNS and managed services " +
			"ride — are NEVER listed, by design. What this returns is exactly what " +
			"`frostmoln_vpc_route` sees, which is what lets a check treat the list as the WHOLE " +
			"visible table rather than a partial one.\n\n" +

			"IT REFUSES RATHER THAN LIE: a 404 from the route collection has two causes that " +
			"cannot be told apart on the wire — this deployment does not serve route management " +
			"at all, or this VPC has no router and therefore no route table. The data source asks " +
			"the VPC itself to separate what it can, and errors either way. An empty table that " +
			"is really a refusal would read as \"no drift\", which is the one wrong answer this " +
			"data source exists to prevent.",
		Attributes: map[string]schema.Attribute{
			"vpc_id": schema.StringAttribute{
				Description: "The ID of the VPC whose tenant-visible route table to list.",
				Required:    true,
				Validators: []validator.String{
					// Refusing here, at plan time, means a mistyped id fails the
					// configuration instead of becoming a request with a meaningful
					// neighbor: a "." or ".." id would not stay one path segment, and
					// the cleaned URL addresses a DIFFERENT resource (see
					// internal/resource/vpc_route, where this guard was earned).
					validVPCIDValidator{},
				},
			},
			"id": schema.StringAttribute{
				Description: "The identifier of this listing — the same VPC id as `vpc_id`, " +
					"deliberately mirrored rather than distinct: one VPC's visible table is the " +
					"whole identity of this listing, and there is no server-side id beyond it.",
				Computed: true,
			},

			"routes": schema.ListNestedAttribute{
				Description: "Every tenant-visible route on the VPC, in the canonical rendering " +
					"the platform reports back: exactly the rows `frostmoln_vpc_route` can see. " +
					"A VPC with no tenant routes renders as an empty list, never null.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"destination": schema.StringAttribute{
							Description: "Destination CIDR, in the canonical (masked) form the " +
								"platform stores — `203.0.113.0/24`. A platform default route is " +
								"reported back as the single `0.0.0.0/0` it was written as.",
							Computed: true,
						},
						"next_hop": schema.StringAttribute{
							Description: "Next hop, exactly as the platform renders it back: an " +
								"address on a subnet attached to the VPC, or the reserved token " +
								"`internet` — which reads back as the token, never as the address " +
								"it resolved to, so a check over these rows never sees a gateway " +
								"rebuild as drift.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

func (d *vpcRoutesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *vpcRoutesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg vpcRoutesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	vpcID := cfg.VPCID.ValueString()

	// The schema validator catches a bad id at plan time; this is the same guard
	// at the request boundary, because Read must never trust that a validated
	// configuration is the only thing that reaches it.
	routesPath, pathErr := routesPath(d.client, vpcID)
	if pathErr != nil {
		resp.Diagnostics.AddError("Invalid VPC ID", pathErr.Error())
		return
	}

	apiResp, err := d.client.Get(ctx, routesPath, nil)
	if err != nil {
		// THE RESOURCE'S 404 HANDLING, INHERITED. `terraform refresh` of the
		// resource below this listing has the same two-cause 404 and the same
		// rule: the route collection answering 404 is never a set to render.
		if !client.IsNotFound(err) {
			resp.Diagnostics.AddError("Failed to Read VPC Routes", err.Error())
			return
		}
		// Two causes produce that 404 and they are indistinguishable in the
		// body: route management switched off for this deployment, or this VPC
		// has no router and therefore no route table. Ask the VPC itself.
		if vpcExists(ctx, d.client, vpcID) {
			resp.Diagnostics.AddError(
				"This VPC's routes could not be read",
				"The VPC exists, but its route collection answered 404. Two causes are possible "+
					"and cannot be told apart from here:\n\n"+
					"  - this deployment does not have VPC route management enabled — ask your "+
					"operator;\n"+
					"  - this VPC has no route table, which means it has no router.\n\n"+
					"The listing cannot resolve until one of the two changes. If this VPC is "+
					"router-less by design, or this deployment does not offer route management, "+
					"remove this data source — and any check block over it — from the configuration "+
					"instead of keeping a plan that can never pass;\n\n"+err.Error(),
			)
			return
		}
		// Real parent drift: the VPC is gone, so its routes are too. A data
		// source has no state to drop — the honest answer is to fail the read.
		resp.Diagnostics.AddError(
			"The VPC does not exist",
			fmt.Sprintf("The route collection answers 404 and the VPC %q does not exist either — "+
				"its routes went with it. Correct `vpc_id` or recreate the VPC.\n\n"+err.Error(), vpcID),
		)
		return
	}

	var list apiVPCRouteList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		resp.Diagnostics.AddError("Failed to Parse VPC Routes Response", err.Error())
		return
	}

	// A 200 WITHOUT THE SET IS NOT AN EMPTY SET, and only an uncritical call is
	// needed to make that wrong. `json.Unmarshal` into a plain slice leaves it
	// nil when the body carries no `routes` key at all — an object that is not
	// the route collection's answer — and rendering that as `routes = []` would
	// tell every check block "no drift" about a table no service ever approved.
	// The empty-if-genuinely-empty case is handled below the guard.
	if list.Routes == nil {
		resp.Diagnostics.AddError(
			"This VPC's route collection did not answer with a route set",
			fmt.Sprintf("The response carried no `routes` set to render. Whatever answered is "+
				"not the route collection this provider builds its contract on, so the listing "+
				"refuses rather than render an empty table that no service promised.\n\n"+
				"The escaped route-collection path was %s. Check the deployment and retry; the "+
				"error persists if what answers this path is not the route API.", routesPath),
		)
		return
	}

	// Non-nil, so an empty set renders `routes = []` rather than null: to a
	// check block those are different answers, and null is the dishonest one.
	rows := make([]vpcRouteRowModel, 0, len(*list.Routes))
	for _, route := range *list.Routes {
		// VERBATIM, on purpose. The service renders `internet` back as the token
		// and a stored default route back as the single `0.0.0.0/0`, so the rows
		// can pass through untouched — the same values `frostmoln_vpc_route`
		// stores, which is what makes a check over this list comparable to what
		// the configuration declares.
		rows = append(rows, vpcRouteRowModel{
			Destination: types.StringValue(route.Destination),
			NextHop:     types.StringValue(route.NextHop),
		})
	}

	routesList, listDiags := types.ListValueFrom(ctx, rowObjectType(), rows)
	resp.Diagnostics.Append(listDiags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := vpcRoutesModel{
		VPCID:  types.StringValue(vpcID),
		ID:     types.StringValue(vpcID),
		Routes: routesList,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// routesPath is the same VPC-scoped route collection the resource reads —
// never the router-scoped surface, which returns the ADDRESS the `internet`
// token resolved to rather than the token.
func routesPath(c *client.Client, vpcID string) (string, error) {
	if err := validVPCID(vpcID); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/vpcs/%s/routes", vpcID)), nil
}

// validVPCID refuses an id that cannot safely be one path segment. A deliberate
// copy of the resource's guard (internal/resource/vpc_route) — both surfaces
// build the same URL, so both must refuse the same values. Keep them in step.
func validVPCID(vpcID string) error {
	if vpcID == "" {
		return fmt.Errorf("a VPC ID is required")
	}
	if vpcID == "." || vpcID == ".." || strings.ContainsAny(vpcID, `/\?#%`) {
		return fmt.Errorf("invalid VPC ID %q", vpcID)
	}
	return nil
}

// validVPCIDValidator carries validVPCID into plan time, so a configuration
// with an unusable id fails before any request is built.
type validVPCIDValidator struct{}

func (v validVPCIDValidator) Description(_ context.Context) string {
	return "value must be a usable VPC ID: non-empty, and a single URL path segment"
}

func (v validVPCIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v validVPCIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	if err := validVPCID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid VPC ID",
			fmt.Sprintf("%s: %s", err.Error(), "a VPC ID must be non-empty and must not contain "+
				"a '/', a backslash, '?', '#' or '%', so it can only ever address the one VPC named."),
		)
	}
}

// rowObjectType is the framework type of one `routes` row.
func rowObjectType() attr.Type {
	return types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"destination": types.StringType,
			"next_hop":    types.StringType,
		},
	}
}

// vpcExists reports whether the VPC itself is still there, to tell real parent
// drift from a deployment that does not serve route management. A deliberate
// copy of the resource's seam (internal/resource/vpc_route) with the same
// fail-safe reading: anything other than a definite 404 on the VPC counts as
// "still there", so an unrelated failure surfaces rather than becoming a
// verdict about the route table.
func vpcExists(ctx context.Context, c *client.Client, vpcID string) bool {
	if err := validVPCID(vpcID); err != nil {
		// Not a usable id, so nothing can be concluded about the VPC. Fail
		// safe: never a reason to answer "gone" for anything.
		return true
	}
	_, err := c.Get(ctx, c.TenantPath(fmt.Sprintf("/vpcs/%s", vpcID)), nil)

	// A DEFINITE 404 MEANS THE SERVICE SAID SO, and only a FLAT envelope is the
	// service speaking. client.IsNotFound reads the status alone, and the
	// api-gateway answers 404 (nested under `error`) for any path it does not
	// route — so a gateway-wide misroute made both calls 404 and would read as
	// "the VPC is gone" without this envelope check.
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && !apiErr.FlatEnvelope {
		return true
	}
	return !client.IsNotFound(err)
}
