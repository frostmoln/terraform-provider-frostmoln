// Package container_registry_repositories implements the
// frostmoln_container_registry_repositories Terraform data source — a READ-ONLY
// inventory of the repositories in the tenant's registry namespace.
//
// It answers the question the registry's own surface could not: the
// frostmoln_container_registry data source reports how many BYTES the namespace
// occupies, but nothing said WHAT is in it. A practitioner who has hit the
// storage cap, or who needs to know which repositories a pipeline has been
// pushing to, had to leave Terraform entirely and reach for `fm registry` or the
// portal.
//
// Nothing here writes. There is deliberately no resource counterpart: a
// repository is created by a `docker push` and removed by deleting what is in
// it, so a Terraform resource would be claiming a lifecycle it does not own.
package container_registry_repositories

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &repositoriesDataSource{}

// repositoriesPath is the tenant-scoped collection. It stays TWO-SEGMENT
// (`/registry/repositories`, never a bare `/registry`): the api-gateway's
// feature gate matches on an unanchored substring of the whole path, so a bare
// `/registry` would capture a customer's own resources named "registry".
const repositoriesPath = "/registry/repositories"

// reasonNotEnabled marks the 409 answered to a tenant that has not opted in. It
// is NOT distinguishable by status or by code alone — the opt-in route answers a
// different 409 on this same surface — so the `details.reason` discriminator is
// the only reliable test. Mirrors the constant of the same name in
// internal/resource/container_registry_credential, which is unexported there;
// the registry packages keep per-package copies rather than share an internal
// type, the same way the registry data source keeps its own storageAttrs.
const reasonNotEnabled = "REGISTRY_NOT_ENABLED"

// NewDataSource returns a new frostmoln_container_registry_repositories data
// source factory.
func NewDataSource() datasource.DataSource {
	return &repositoriesDataSource{}
}

type repositoriesDataSource struct {
	client *client.Client
}

// repositoriesModel is the Terraform state model for the repository list.
type repositoriesModel struct {
	Repositories types.List `tfsdk:"repositories"`
}

// repositoryItemModel represents a single repository in the list.
type repositoryItemModel struct {
	Name          types.String `tfsdk:"name"`
	ArtifactCount types.Int64  `tfsdk:"artifact_count"`
	PullCount     types.Int64  `tfsdk:"pull_count"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
}

// apiRepository is the API representation of a repository. camelCase on the
// wire, snake_case in the schema — the platform-wide split.
type apiRepository struct {
	Name          string `json:"name"`
	ArtifactCount int64  `json:"artifactCount"`
	PullCount     int64  `json:"pullCount"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

// apiRepositoryList is the API response for listing repositories. The route is
// UNPAGINATED — it answers with the whole set and a totalCount — so there is no
// paging loop here, unlike the artifacts data source.
//
// TotalCount is CHECKED against the rows rather than merely decoded: with no
// paging loop it is the only cross-check this response carries, and a listing
// short of its own count is exactly the invisible failure Read refuses on.
type apiRepositoryList struct {
	Data       []apiRepository `json:"data"`
	TotalCount int64           `json:"totalCount"`
}

var repositoryItemAttrTypes = map[string]attr.Type{
	"name":           types.StringType,
	"artifact_count": types.Int64Type,
	"pull_count":     types.Int64Type,
	"created_at":     types.StringType,
	"updated_at":     types.StringType,
}

func (d *repositoriesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_repositories"
}

func (d *repositoriesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the repositories in this tenant's container registry namespace.\n\n" +
			"OBSERVATIONAL, like `storage_used_bytes` on `frostmoln_container_registry`: a repository " +
			"appears on its first `docker push` and disappears when the last artifact in it is " +
			"deleted, and its counters move on every push and pull. Expect the values to differ on " +
			"every refresh, and do not build a configuration that plans a change from them.\n\n" +
			"A tenant that has NOT enabled its registry is an error here, not an empty list. An empty " +
			"list would make \"this tenant has no registry\" and \"this registry holds no images\" " +
			"indistinguishable, and a configuration reading the second would quietly do nothing when " +
			"the first was true.",
		Attributes: map[string]schema.Attribute{
			"repositories": schema.ListNestedAttribute{
				Description: "Every repository in the namespace. Empty when the registry is enabled and " +
					"nothing has been pushed to it yet.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name": schema.StringAttribute{
							Description: "The repository name RELATIVE to the tenant's namespace, so a full " +
								"image reference is `<endpoint>/<namespace>/<name>`. It may itself contain " +
								"`/` — treat it as a path, never as a single segment.",
							Computed: true,
						},
						"artifact_count": schema.Int64Attribute{
							Description: "How many artifacts (image manifests) the repository holds, " +
								"including untagged ones. OBSERVATIONAL: every push changes it.",
							Computed: true,
						},
						"pull_count": schema.Int64Attribute{
							Description: "How many times the repository has been pulled, as the registry " +
								"counts it. OBSERVATIONAL: it moves whenever anything pulls, with no " +
								"Terraform change involved.",
							Computed: true,
						},
						"created_at": schema.StringAttribute{
							Description: "When the repository first appeared — the timestamp of its first " +
								"push, RFC 3339.",
							Computed: true,
						},
						"updated_at": schema.StringAttribute{
							Description: "When the repository last changed, RFC 3339. OBSERVATIONAL: a push " +
								"moves it.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

func (d *repositoriesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *repositoriesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state repositoriesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := d.client.Get(ctx, d.client.TenantPath(repositoriesPath), nil)
	if err != nil {
		if isNotEnabled(err) {
			// Deliberately an error rather than an empty list — see the schema
			// description. It names the remedy because the server's own message
			// cannot: it does not know the practitioner is in Terraform.
			resp.Diagnostics.AddError(
				"This tenant's container registry is not enabled",
				"There is no repository inventory to read, because the tenant has not opted in. Enable "+
					"the registry with a `frostmoln_container_registry` resource (or `fm registry enable`) "+
					"and read it again — and if this configuration creates the registry itself, make the "+
					"data source depend on that resource so the two are ordered.\n\nError: "+err.Error(),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to list the container registry repositories", err.Error())
		return
	}

	list, err := client.ParseResponse[apiRepositoryList](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse the container registry repositories response", err.Error())
		return
	}

	// The count the server reports must match the rows it sent. This route is
	// unpaginated and the server cannot truncate today — but a SHORT LIST IS THE
	// DANGEROUS FAILURE for every reader of this inventory, and it is invisible:
	// a truncated listing looks exactly like a smaller registry, so a
	// configuration written to find and prune what a namespace holds would prune
	// against a view missing rows and report success. `totalCount` is required by
	// the API contract and is the only thing on this response that can catch it,
	// so it is read rather than decoded and ignored. fm-cli refuses on the same
	// signal.
	if list.TotalCount != int64(len(list.Data)) {
		resp.Diagnostics.AddError(
			"The container registry repository listing is inconsistent",
			fmt.Sprintf("The API reported %d repositories but returned %d. This listing is unpaginated, so "+
				"the two must agree; a shorter list would be indistinguishable from a smaller registry and "+
				"a configuration reading it would plan against repositories it cannot see. Refusing rather "+
				"than returning a list that may be incomplete.", list.TotalCount, len(list.Data)),
		)
		return
	}

	// Built with a length, not appended to from nil: an enabled registry holding
	// nothing must render as an EMPTY list, and a nil slice would render as a
	// null one — which reads to a configuration as "unknown", not "none".
	items := make([]repositoryItemModel, 0, len(list.Data))
	for _, r := range list.Data {
		items = append(items, repositoryItemModel{
			Name:          types.StringValue(r.Name),
			ArtifactCount: types.Int64Value(r.ArtifactCount),
			PullCount:     types.Int64Value(r.PullCount),
			CreatedAt:     types.StringValue(r.CreatedAt),
			UpdatedAt:     types.StringValue(r.UpdatedAt),
		})
	}

	repositories, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: repositoryItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Repositories = repositories
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// isNotEnabled reports whether err is the refusal issued to a tenant that has
// not opted in.
func isNotEnabled(err error) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	reason, _ := apiErr.Details["reason"].(string)
	return reason == reasonNotEnabled
}
