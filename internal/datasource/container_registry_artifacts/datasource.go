// Package container_registry_artifacts implements the
// frostmoln_container_registry_artifacts Terraform data source — a READ-ONLY
// listing of the artifacts (image manifests) inside ONE repository of the
// tenant's registry namespace.
//
// It is the second half of the inventory that
// frostmoln_container_registry_repositories starts: that one says which
// repositories exist, this one says what is in a repository — digests, tags,
// sizes, and when each artifact was last pushed and pulled. The two together
// are what lets a configuration find the untagged, unreachable manifests that
// are still occupying the namespace's storage cap.
//
// Nothing here writes. Deleting an artifact is not offered as a resource: an
// artifact is created by a `docker push`, so Terraform does not own its
// lifecycle, and a resource that could only destroy would be a delete button
// wearing a lifecycle's clothes.
package container_registry_artifacts

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &artifactsDataSource{}

// artifactsPath is the tenant-scoped collection. It stays TWO-SEGMENT
// (`/registry/artifacts`, never a bare `/registry`): the api-gateway's feature
// gate matches on an unanchored substring of the whole path, so a bare
// `/registry` would capture a customer's own resources named "registry".
//
// The repository is a QUERY PARAMETER, never a path segment. A repository name
// legally contains `/` ("team/api/server" is one repository, not three), so
// splicing it into the path would restructure the URL — and because the final
// URL is assembled with path cleaning, a name containing a dot segment could
// walk the request off the tenant-scoped route entirely, carrying the caller's
// credential with it.
const artifactsPath = "/registry/artifacts"

// requestedPageSize is what each page asks for. The server CLAMPS pageSize to
// its own maximum (100) and echoes what it actually applied, so the paging loop
// below terminates on the ECHOED size and never on this constant.
const requestedPageSize = 100

// maxArtifactPages bounds the paging loop (100 * this = 100k artifacts) so a
// gateway that never stops answering full pages cannot hang a plan or apply
// forever. Exhausting it is an ERROR, not a truncated list: Terraform has no
// concept of a partial data source, so a short list would silently plan against
// a view of the registry that is missing rows.
const maxArtifactPages = 1000

// reasonNotEnabled marks the 409 answered to a tenant that has not opted in. It
// is NOT distinguishable by status or by code alone — the opt-in route answers a
// different 409 on this same surface — so the `details.reason` discriminator is
// the only reliable test. Mirrors the constant of the same name in
// internal/resource/container_registry_credential, which is unexported there;
// the registry packages keep per-package copies rather than share an internal
// type, the same way the registry data source keeps its own storageAttrs.
const reasonNotEnabled = "REGISTRY_NOT_ENABLED"

// NewDataSource returns a new frostmoln_container_registry_artifacts data
// source factory.
func NewDataSource() datasource.DataSource {
	return &artifactsDataSource{}
}

type artifactsDataSource struct {
	client *client.Client
}

// artifactsModel is the Terraform state model for one repository's artifacts.
type artifactsModel struct {
	Repository types.String `tfsdk:"repository"`
	Artifacts  types.List   `tfsdk:"artifacts"`
}

// artifactItemModel represents a single artifact in the list.
type artifactItemModel struct {
	Digest    types.String `tfsdk:"digest"`
	Tags      types.List   `tfsdk:"tags"`
	SizeBytes types.Int64  `tfsdk:"size_bytes"`
	PushedAt  types.String `tfsdk:"pushed_at"`
	PulledAt  types.String `tfsdk:"pulled_at"`
}

// apiArtifact is the API representation of an artifact. camelCase on the wire,
// snake_case in the schema — the platform-wide split.
type apiArtifact struct {
	Digest string   `json:"digest"`
	Tags   []string `json:"tags"`
	// SizeBytes is the artifact's OWN size. It does not sum to the namespace's
	// storage usage — see the schema description, which is the copy a
	// practitioner actually reads.
	SizeBytes int64  `json:"sizeBytes"`
	PushedAt  string `json:"pushedAt"`
	// PulledAt is a POINTER because the field is OMITTED for an artifact that
	// has never been pulled, and "never" must reach Terraform as null. Decoded
	// into a plain string, an omitted field would land as "" and then, once
	// rendered, as an empty-string timestamp — a value that reads as data.
	PulledAt *string `json:"pulledAt"`
}

// apiArtifactList is one PAGE of the artifact listing. Page and PageSize are
// what the server actually applied, not what was asked for; the loop in Read
// terminates on them.
type apiArtifactList struct {
	Data     []apiArtifact `json:"data"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
}

var artifactItemAttrTypes = map[string]attr.Type{
	"digest":     types.StringType,
	"tags":       types.ListType{ElemType: types.StringType},
	"size_bytes": types.Int64Type,
	"pushed_at":  types.StringType,
	"pulled_at":  types.StringType,
}

func (d *artifactsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_container_registry_artifacts"
}

func (d *artifactsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Lists the artifacts — image manifests — in ONE repository of this tenant's " +
			"container registry namespace.\n\n" +
			"The whole repository is returned: this data source follows the API's pagination itself " +
			"and assembles every page, because Terraform has no notion of a partial list and a " +
			"truncated one would silently plan against a registry that is missing images.\n\n" +
			"OBSERVATIONAL, like `storage_used_bytes` on `frostmoln_container_registry`: artifacts " +
			"appear on `docker push`, tags move between them, and `pulled_at` advances whenever " +
			"anything pulls. Expect the values to differ on every refresh, and do not build a " +
			"configuration that plans a change from them.\n\n" +
			"A tenant that has NOT enabled its registry is an error here, not an empty list — an " +
			"empty list would make \"this tenant has no registry\" and \"this repository holds no " +
			"images\" indistinguishable. A repository that does not exist is an error for the same " +
			"reason.\n\n" +
			"THERE IS NO ARTIFACT RESOURCE, AND THAT IS DELIBERATE. The API can delete an artifact " +
			"— `fm registry image delete <repository> <digest>`, the Remove button in the portal, " +
			"or `DELETE /registry/artifacts` — but Terraform is not where that belongs, for three " +
			"reasons.\n\n" +
			"FIRST, THIS PROVIDER IS ALREADY NON-DESTRUCTIVE FOR REGISTRY CONTENT. " +
			"`frostmoln_container_registry`'s own Delete removes the registry from state and " +
			"destroys nothing, warning that the namespace and its images survive. An artifact " +
			"resource would be the single exception — the one registry object a `terraform " +
			"destroy` in an unrelated module could actually annihilate, and it would annihilate " +
			"content a pipeline pushed and Terraform never made.\n\n" +
			"SECOND, THE DIGEST IS MINTED BY A BUILD OUTSIDE TERRAFORM. Hardcode one and it is " +
			"stale on the next push; compute it from this data source and the resource is keyed " +
			"off a value this very schema documents as observational and expected to change on " +
			"every refresh — a perpetual destroy/create plan.\n\n" +
			"THIRD, DRIFT IS GUARANTEED AND ONE-DIRECTIONAL. A `fm registry image delete`, a " +
			"portal Remove or a tag overwrite makes the object vanish underneath state, and every " +
			"later plan proposes recreating something Terraform cannot create.\n\n" +
			"If declarative cleanup is ever genuinely wanted, the object to model is the RETENTION " +
			"POLICY on the tenant's namespace — a real desired-state object — never the artifact. " +
			"Meanwhile: use this data source to FIND what to remove, and remove it with a tool " +
			"that knows it is destroying content.",
		Attributes: map[string]schema.Attribute{
			"repository": schema.StringAttribute{
				Description: "The repository to list, RELATIVE to the tenant's namespace — the `name` of " +
					"a `frostmoln_container_registry_repositories` entry, not a full image reference and " +
					"not prefixed with the endpoint or namespace. It may contain `/`. A repository that " +
					"does not exist is an error, not an empty list.",
				Required: true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
				},
			},
			"artifacts": schema.ListNestedAttribute{
				Description: "Every artifact in the repository, tagged and untagged alike.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"digest": schema.StringAttribute{
							Description: "The artifact's content digest (`sha256:...`) — the immutable " +
								"reference, `<endpoint>/<namespace>/<repository>@<digest>`. Unlike a tag it " +
								"cannot be moved to different content by a later push.",
							Computed: true,
						},
						"tags": schema.ListAttribute{
							Description: "The tags pointing at this artifact, without any repository prefix.\n\n" +
								"AN EMPTY LIST IS NOT AN ERROR AND NOT A MISSING VALUE: it means the artifact " +
								"is an UNTAGGED, ORPHANED manifest — a later push moved the tag it used to " +
								"carry onto new content. Such an artifact can no longer be pulled by name, only " +
								"by digest, and it still consumes the namespace's storage. They are listed " +
								"deliberately, precisely so a configuration can find them.",
							Computed:    true,
							ElementType: types.StringType,
						},
						"size_bytes": schema.Int64Attribute{
							Description: "The size of THIS artifact in bytes, as the registry reports it for " +
								"the artifact alone.\n\n" +
								"DO NOT SUM THESE, and do not compare a sum with `storage_used_bytes` on " +
								"`frostmoln_container_registry`. A layer shared between artifacts in the " +
								"same namespace is counted ONCE against that namespace's cap, but counted IN FULL in every " +
								"artifact that references it — so a sum over this list overstates what the " +
								"namespace occupies, usually by a lot, since images built from a common base " +
								"share nearly all of their bytes. For the same reason, deleting an artifact " +
								"frees only the layers nothing else references, which is normally far fewer " +
								"bytes than this number. This attribute answers \"how big is this image\", " +
								"never \"how much quota does it cost\"; `storage_used_bytes` is the only " +
								"correct answer to the second.",
							Computed: true,
						},
						"pushed_at": schema.StringAttribute{
							Description: "When the artifact was pushed, RFC 3339.",
							Computed:    true,
						},
						"pulled_at": schema.StringAttribute{
							Description: "When the artifact was last pulled, RFC 3339.\n\n" +
								"NULL MEANS NEVER PULLED. It does not mean \"unknown\", and it is never " +
								"reported as a zero or epoch timestamp — so `a.pulled_at == null` is a sound " +
								"test for an image that nothing has ever fetched. OBSERVATIONAL: a single pull " +
								"between two refreshes changes it.",
							Computed: true,
						},
					},
				},
			},
		},
	}
}

func (d *artifactsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *artifactsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var state artifactsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	repository := state.Repository.ValueString()

	artifacts, err := d.listAll(ctx, repository)
	if err != nil {
		addListRefusal(&resp.Diagnostics, repository, err)
		return
	}

	// Built with a length, not appended to from nil: a repository whose
	// artifacts were all deleted must render as an EMPTY list, and a nil slice
	// would render as a null one — which reads to a configuration as "unknown",
	// not "none".
	items := make([]artifactItemModel, 0, len(artifacts))
	for _, a := range artifacts {
		// Same rule one level down: an untagged manifest is the whole point of
		// this listing, and it must render as an empty tag list rather than a
		// null one, or `length(a.tags) == 0` stops finding it.
		tagValues := a.Tags
		if tagValues == nil {
			tagValues = []string{}
		}
		tags, diags := types.ListValueFrom(ctx, types.StringType, tagValues)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}

		items = append(items, artifactItemModel{
			Digest:    types.StringValue(a.Digest),
			Tags:      tags,
			SizeBytes: types.Int64Value(a.SizeBytes),
			PushedAt:  types.StringValue(a.PushedAt),
			PulledAt:  pulledAt(a.PulledAt),
		})
	}

	list, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: artifactItemAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state.Artifacts = list
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// listAll fetches EVERY page of the repository's artifacts and returns them as
// one slice. Terraform has no pagination concept: whatever this returns is what
// the configuration believes the repository contains, so the loop either
// assembles the whole listing or fails — it never returns a short list.
//
// THE ORDER IS UNSPECIFIED. The API does not ask the upstream registry to sort
// and promises no order, so the page boundary is not stable: a push landing
// between two of these calls shifts rows across it and the same artifact can be
// served on two consecutive pages. That makes a multi-page walk a SNAPSHOT, not
// a transaction. Duplicates are dropped here by digest — for Terraform they are
// worse than for a CLI that merely prints one, because state records the
// duplicate and every later plan is computed against it. Dedupe removes only
// that half of the hazard: an artifact that moves the OTHER way across a
// boundary mid-walk is still MISSED, and nothing here makes the listing
// consistent.
func (d *artifactsDataSource) listAll(ctx context.Context, repository string) ([]apiArtifact, error) {
	var all []apiArtifact
	// Keyed on the digest because that is the artifact's identity: a tag can be
	// moved onto different content by a later push, a digest cannot.
	seen := map[string]bool{}

	for page := 1; page <= maxArtifactPages; page++ {
		q := url.Values{}
		// The repository goes in the QUERY STRING. See artifactsPath: a
		// repository name legally contains "/".
		q.Set("repository", repository)
		q.Set("page", strconv.Itoa(page))
		q.Set("pageSize", strconv.Itoa(requestedPageSize))

		apiResp, err := d.client.Get(ctx, d.client.TenantPath(artifactsPath), q)
		if err != nil {
			return nil, err
		}
		got, err := client.ParseResponse[apiArtifactList](apiResp)
		if err != nil {
			return nil, fmt.Errorf("failed to parse the container registry artifacts response: %w", err)
		}

		// A server that ignores `page` would answer page 1 forever: without this
		// the loop would run to maxArtifactPages appending the SAME rows over and
		// over, and the caller would get a list a hundred thousand entries long
		// made of duplicates. Checked before the rows are kept, so none of that
		// reaches state.
		if got.Page != page {
			return nil, fmt.Errorf(
				"the artifact listing did not advance: page %d was requested and the API answered with page %d",
				page, got.Page,
			)
		}

		for _, a := range got.Data {
			if seen[a.Digest] {
				continue
			}
			seen[a.Digest] = true
			all = append(all, a)
		}

		// Every length test below reads the page AS THE SERVER SENT IT, never the
		// count kept: a page whose rows were all duplicates of an earlier page is
		// still a full page, and measuring the deduplicated count against
		// pageSize would end the listing early — silently truncating, which is
		// the exact failure the dedupe was added alongside.

		// An empty page ends the listing whatever the server says about sizes —
		// there is nothing after it, and this is also what keeps an unreported
		// pageSize from spinning.
		if len(got.Data) == 0 {
			return all, nil
		}

		// Terminate on the size the SERVER applied, not the one we asked for:
		// it clamps pageSize to its own maximum, and a full page measured
		// against the larger requested number would look short and stop the
		// listing one page in.
		if got.PageSize <= 0 {
			return nil, fmt.Errorf(
				"the artifact listing did not report the page size it applied (page %d returned %d artifacts), "+
					"so a final page cannot be told from a full one", page, len(got.Data),
			)
		}
		if len(got.Data) < got.PageSize {
			return all, nil
		}
	}

	// Deliberately an error. Returning `all` here would be a truncated listing
	// presented as a complete one.
	return nil, fmt.Errorf("the artifact listing exceeded %d pages without ending; aborting rather than "+
		"returning a partial list", maxArtifactPages)
}

// zeroTimestampPrefixes are the two dates a zero time renders as once something
// upstream has formatted one: Go's own zero time is 0001-01-01, a zero Unix
// timestamp is 1970-01-01. Neither is a pull.
//
// The registry service cannot emit either today — it omits the field instead —
// so this is defence in depth, and it is here for PARITY: the portal already
// rejects both and the `fm` CLI now does too. A `pulled_at` that disagrees with
// them about whether an image has ever been fetched is worse than any single
// answer, because the schema promises `a.pulled_at == null` is a sound test for
// exactly that.
var zeroTimestampPrefixes = []string{"0001-01-01", "1970-01-01"}

// pulledAt renders the last-pull timestamp, with an absent, empty or ZERO value
// becoming NULL. All three mean the same thing — the artifact has never been
// pulled — and null is the only rendering that says so: "" would reach a
// configuration as a timestamp-shaped value that is not a timestamp, and a zero
// time is worse still, because "1970-01-01" reads as a real pull decades ago and
// a configuration pruning by age would act on it.
func pulledAt(v *string) types.String {
	if v == nil {
		return types.StringNull()
	}
	pulled := strings.TrimSpace(*v)
	if pulled == "" {
		return types.StringNull()
	}
	for _, prefix := range zeroTimestampPrefixes {
		if strings.HasPrefix(pulled, prefix) {
			return types.StringNull()
		}
	}
	return types.StringValue(*v)
}

// addListRefusal translates the two refusals whose server message cannot name
// the Terraform-level remedy, because the server does not know the caller is in
// Terraform.
func addListRefusal(diags interface{ AddError(string, string) }, repository string, err error) {
	switch {
	case isNotEnabled(err):
		diags.AddError(
			"This tenant's container registry is not enabled",
			"There are no artifacts to read, because the tenant has not opted in. Enable the registry "+
				"with a `frostmoln_container_registry` resource (or `fm registry enable`) and read it "+
				"again — and if this configuration creates the registry itself, make the data source "+
				"depend on that resource so the two are ordered.\n\nError: "+err.Error(),
		)
	case client.IsNotFound(err):
		// A genuine 404 from the registry service, not a gateway misroute:
		// client.IsNotFound tests the refusal ENVELOPE, so a gateway that failed
		// to route the path falls through to the generic message below instead
		// of being reported as an absent repository.
		diags.AddError(
			fmt.Sprintf("No such container registry repository: %q", repository),
			"The registry has no repository by that name. `repository` is RELATIVE to the tenant's "+
				"namespace — it is the `name` of a `frostmoln_container_registry_repositories` entry, not "+
				"a full image reference, so drop any endpoint or namespace prefix. A repository also "+
				"stops existing once the last artifact in it is deleted.\n\nError: "+err.Error(),
		)
	default:
		diags.AddError("Failed to list the container registry artifacts", err.Error())
	}
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
