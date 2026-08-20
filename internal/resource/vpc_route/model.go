// Package vpc_route implements the frostmoln_vpc_route Terraform resource.
package vpc_route

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// NextHopInternet is the reserved next-hop token meaning "out this VPC's own
// internet gateway".
//
// IT IS NOT AN ADDRESS, and nothing in this resource may validate next_hop as
// one. The service resolves the token at write time and renders it back AS THE
// TOKEN on every read, so a converging client sees no diff.
const NextHopInternet = "internet"

// VPCRouteModel is the Terraform state model for one static route on a VPC.
type VPCRouteModel struct {
	ID          types.String `tfsdk:"id"`
	VPCID       types.String `tfsdk:"vpc_id"`
	Destination types.String `tfsdk:"destination"`
	NextHop     types.String `tfsdk:"next_hop"`
}

// apiVPCRoute is the API representation of one route.
//
// camelCase `nextHop`: this is the VPC-scoped surface. The older router-scoped
// one spells the same field `nexthop`, and a model shared across the two drops
// the field on one of them.
type apiVPCRoute struct {
	Destination string `json:"destination"`
	NextHop     string `json:"nextHop"`
}

// apiVPCRouteList is what EVERY operation on the collection answers with — the
// tenant's whole route set, never the single route that was written.
type apiVPCRouteList struct {
	Routes []apiVPCRoute `json:"routes"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *VPCRouteModel) toCreateRequest() apiVPCRoute {
	return apiVPCRoute{
		Destination: m.Destination.ValueString(),
		NextHop:     m.NextHop.ValueString(),
	}
}

// fromAPI populates the Terraform model from one route in an API response.
func (m *VPCRouteModel) fromAPI(vpcID string, route *apiVPCRoute) {
	m.VPCID = types.StringValue(vpcID)
	m.Destination = types.StringValue(route.Destination)
	m.NextHop = types.StringValue(route.NextHop)
	m.ID = types.StringValue(buildID(vpcID, route.Destination))
}

// buildID is the resource's synthetic id, and the import id.
//
// A route has no server-side id — a destination identifies it, and a
// destination is unique per VPC. SplitN on the FIRST slash is what makes this
// reversible: a VPC id contains no slash, and everything after the first one is
// the destination CIDR, which does.
func buildID(vpcID, destination string) string {
	return vpcID + "/" + destination
}
