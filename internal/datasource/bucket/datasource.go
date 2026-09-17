// Package bucket implements the frostmoln_bucket Terraform data source: the
// resolver for an object storage bucket — the lookup a configuration needs to
// reach a bucket that Terraform did not create (a portal or `fm`-provisioned
// bucket can today be referenced only by a hardcoded name plumbed through
// terraform_remote_state).
//
// A bucket's name IS its id: the /buckets surface addresses a bucket by name
// (`GET /tenants/{t}/buckets/{bucket_name}` — router internal/handler/http), so
// there is exactly one identity and this data source carries it once. The data
// source takes `name` (refusing anything that cannot safely be one path
// segment) and computes `id` mirroring the name; the direct GET is the whole
// resolver — there is no list-based resolution, because a name the path can
// carry directly needs no search.
//
// A FLAT 404 fails the read with one cause: the bucket is absent or not
// visible to this caller, and both mean stop. A data source has no state to
// drop, so the honest answer is a failed read, never a rendered row no service
// promised. A NESTED 404 (the api-gateway's unrouted-path envelope) is not a
// verdict about the bucket and takes the generic arm.
//
// The CORS rules, lifecycle rules and website configuration are deliberately
// NOT exported here: those collections are owned by the dedicated resources
// (`frostmoln_bucket_cors_configuration`, `frostmoln_bucket_lifecycle_configuration`)
// and they exist on their own endpoints (`/cors`, `/lifecycle`, `/website`) —
// a second rendering of them on this data source would be a second owner of
// one collection, exactly what the surface-contract doctrine forbids. Reach
// them through those resources.
package bucket

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &bucketDataSource{}

// NewDataSource returns a new frostmoln_bucket data source factory.
func NewDataSource() datasource.DataSource {
	return &bucketDataSource{}
}

type bucketDataSource struct {
	client *client.Client
}

// bucketModel is the Terraform state model. Attribute names mirror the wire
// (storage/internal/domain/bucket.go), so a data source read can be fed
// straight into a reference without translating names. The name is required —
// it IS the id — and `id` is the computed mirror of it.
type bucketModel struct {
	Name                types.String `tfsdk:"name"`
	ID                  types.String `tfsdk:"id"`
	Region              types.String `tfsdk:"region"`
	ACL                 types.String `tfsdk:"acl"`
	Versioning          types.String `tfsdk:"versioning"`
	DefaultStorageClass types.String `tfsdk:"default_storage_class"`
	ObjectCount         types.Int64  `tfsdk:"object_count"`
	TotalSize           types.Int64  `tfsdk:"total_size"`
	QuotaBytes          types.Int64  `tfsdk:"quota_bytes"`
	Tags                types.Map    `tfsdk:"tags"`
	CreatedAt           types.String `tfsdk:"created_at"`
	UpdatedAt           types.String `tfsdk:"updated_at"`
	TenantID            types.String `tfsdk:"tenant_id"`
}

// apiBucket is the API representation of one bucket — the same wire shape
// storage serializes (see storage/internal/domain/bucket.go).
//
// corsRules / lifecycleRules / website are deliberately ABSENT from this
// struct: those collections are owned by the dedicated resources
// (frostmoln_bucket_cors_configuration, frostmoln_bucket_lifecycle_configuration)
// and sit on their own endpoints. Nothing here may grow a second rendering of
// them — decode only what this data source renders.
type apiBucket struct {
	Name                string            `json:"name"`
	TenantID            string            `json:"tenantId"`
	Region              string            `json:"region,omitempty"`
	ACL                 string            `json:"acl"`
	Versioning          string            `json:"versioning"`
	DefaultStorageClass string            `json:"defaultStorageClass"`
	ObjectCount         int64             `json:"objectCount"`
	TotalSize           int64             `json:"totalSize"`
	QuotaBytes          int64             `json:"quotaBytes,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
	CreatedAt           string            `json:"createdAt"`
	UpdatedAt           string            `json:"updatedAt"`
}

func (d *bucketDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket"
}

func (d *bucketDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up an object storage bucket by name. The bucket's name IS its id — " +
			"the `/buckets` surface addresses a bucket by name — so `name` is the one identity " +
			"and `id` mirrors it.\n\n" +

			"This is how a configuration reaches a bucket that Terraform did not create: a " +
			"bucket provisioned in the portal or with `fm` can today be referenced only by a " +
			"hardcoded name plumbed through `terraform_remote_state` — look it up by name here " +
			"and the reference survives re-provisioning elsewhere. The resolver is one direct " +
			"GET of the bucket, never a list search: a name the path can carry directly needs " +
			"no search, and a flat 404 fails the read (a nested unrouted-path 404 is not a " +
			"verdict about the bucket and surfaces as a generic failure).\n\n" +

			"The CORS rules, lifecycle rules and website configuration are deliberately NOT " +
			"exported here: those collections are owned by `frostmoln_bucket_cors_configuration` " +
			"and `frostmoln_bucket_lifecycle_configuration` (the website surface has no dedicated " +
			"resource today), and one collection must have one owner and one shape — read them " +
			"through those resources, never thirst for a second rendering of them here.",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "The name of the bucket. Also serves as the unique identifier — " +
					"a bucket's name IS its id, exactly as `frostmoln_bucket`'s import " +
					"addresses a bucket by name.",
				Required: true,
				Validators: []validator.String{
					validBucketNameValidator{},
				},
			},
			"id": schema.StringAttribute{
				Description: "The identifier of the bucket — the same string as `name`, " +
					"deliberately mirrored rather than distinct: the object-storage surface " +
					"has no server-side id beyond the name.",
				Computed: true,
			},
			"region": schema.StringAttribute{
				Description: "The region where the bucket is located, null when the platform " +
					"has not recorded one.",
				Computed: true,
			},
			"acl": schema.StringAttribute{
				Description: "The access control setting of the bucket (private, public-read, " +
					"public-read-write, or authenticated-read).",
				Computed: true,
			},
			"versioning": schema.StringAttribute{
				Description: "The versioning state of the bucket (disabled, enabled, or " +
					"suspended).",
				Computed: true,
			},
			"default_storage_class": schema.StringAttribute{
				Description: "The default storage class for new objects in the bucket " +
					"(STANDARD, STANDARD_IA, REDUCED_REDUNDANCY, or GLACIER).",
				Computed: true,
			},
			"object_count": schema.Int64Attribute{
				Description: "The number of objects in the bucket. Zero renders as 0 — an " +
					"empty bucket is a measured answer, not an absence.",
				Computed: true,
			},
			"total_size": schema.Int64Attribute{
				Description: "The total size of all objects in the bucket, in bytes. Zero " +
					"renders as 0 — an empty bucket is a measured answer, not an absence.",
				Computed: true,
			},
			"quota_bytes": schema.Int64Attribute{
				Description: "The maximum size quota of the bucket in bytes, null when the " +
					"bucket is unlimited (the wire omits the quota, which IS \"no limit\").",
				Computed: true,
			},
			"tags": schema.MapAttribute{
				Description: "The user-defined tags on the bucket, null when it carries none. " +
					"The platform stores its own control-plane metadata in the same tag set " +
					"and strips those keys from this map; what renders here is the customer's " +
					"tags only.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the bucket was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the bucket was last updated.",
				Computed:    true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this bucket.",
				Computed:    true,
			},
		},
	}
}

func (d *bucketDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *bucketDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg bucketModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The schema validator catches a bad name at plan time; this is the same
	// guard at the request boundary, because Read must never trust that a
	// validated configuration is the only thing that reaches it.
	pathStr, pathErr := bucketPath(d.client, cfg.Name.ValueString())
	if pathErr != nil {
		resp.Diagnostics.AddError("Invalid Bucket Name", pathErr.Error())
		return
	}

	apiResp, err := d.client.Get(ctx, pathStr, nil)
	if err != nil {
		// THE 404 WITH ONE CAUSE: a FLAT 404 from the bucket read is the
		// service's collapse doctrine in one voice — the bucket is absent or
		// not visible to this caller, and both mean stop. A data source has no
		// state to drop; the honest answer is to fail the read, never to render
		// a row no service promised. A NESTED 404 (the api-gateway's
		// unrouted-path envelope) is not a verdict at all and falls to the
		// generic arm below.
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError(
				"The bucket does not exist",
				fmt.Sprintf("The name %q answers 404 — there is no such object storage bucket "+
					"(or none this caller can see), so there is nothing to resolve. Correct "+
					"`name`, or create the bucket.\n\n%s", cfg.Name.ValueString(), err.Error()),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Bucket", err.Error())
		return
	}

	var bkt apiBucket
	if err := json.Unmarshal(apiResp.Body, &bkt); err != nil {
		resp.Diagnostics.AddError("Failed to Parse Bucket Response", err.Error())
		return
	}

	// THE 200 MUST NAME THE BUCKET THE PATH ASKS FOR. The path addresses the
	// bucket by name, and an answer that names a different bucket is not the
	// bucket read this provider builds its contract on.
	if bkt.Name != cfg.Name.ValueString() {
		resp.Diagnostics.AddError(
			"This bucket read did not identify the requested bucket",
			fmt.Sprintf("The response does not carry the name this path asks for (%q). Whatever "+
				"answered is not the bucket read this provider builds its contract on, so the "+
				"lookup refuses rather than render a row no service promised. The path was %q.",
				cfg.Name.ValueString(), pathStr),
		)
		return
	}

	bkt.renderInto(ctx, &cfg, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// bucketPath builds one bucket read path behind the same guard the name
// carries at plan time. A "/" INSIDE the name is the exact vector this guard
// exists for: the client joins with path.Join, which CLEANS — "a/b" does not
// stay one path segment, and the cleaned URL addresses a DIFFERENT bucket. In
// a URL, quite literally: a different path.
func bucketPath(c *client.Client, name string) (string, error) {
	if err := validBucketName(name); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/buckets/%s", name)), nil
}

// validBucketName refuses a name that cannot safely be one path segment — a
// bucket name on this surface is a single label (`GET /buckets/{bucket_name}`),
// and it IS the identity: anything that would clean to a different path — a
// "." or ".." whole name, a real or swept-in "/" — must be refused before a
// request dares address another bucket by accident.
func validBucketName(name string) error {
	if name == "" {
		return fmt.Errorf("a bucket name is required")
	}
	if name == "." || name == ".." || strings.ContainsAny(name, `/\?#%`) {
		return fmt.Errorf("invalid bucket name %q", name)
	}
	return nil
}

// validBucketNameValidator carries validBucketName into plan time, so a
// configuration with an unusable name fails before any request is built.
type validBucketNameValidator struct{}

func (v validBucketNameValidator) Description(_ context.Context) string {
	return "value must be a usable bucket name: non-empty, and a single URL path segment"
}

func (v validBucketNameValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v validBucketNameValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	if err := validBucketName(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Bucket Name",
			fmt.Sprintf("%s: %s", err.Error(), "a bucket name must be non-empty and must not "+
				"contain a '/', a backslash, '?', '#' or '%', so it can only ever address the "+
				"one bucket named."),
		)
	}
}

// renderInto maps one verified bucket onto the model. Absent-optionals are
// null, never "" or 0 — a check block must be able to tell "no value" apart
// from "empty value". The measured counts render what the wire carries: an
// empty bucket answers objectCount 0 EXPLICITLY (no omitempty on the wire),
// so zero is a measured answer and renders as 0.
func (b *apiBucket) renderInto(ctx context.Context, state *bucketModel, diags *diag.Diagnostics) {
	state.Name = types.StringValue(b.Name)
	state.ID = types.StringValue(b.Name)
	state.Region = stringFromWire(b.Region)
	state.ACL = types.StringValue(b.ACL)
	state.Versioning = types.StringValue(b.Versioning)
	state.DefaultStorageClass = types.StringValue(b.DefaultStorageClass)
	state.ObjectCount = types.Int64Value(b.ObjectCount)
	state.TotalSize = types.Int64Value(b.TotalSize)
	// quotaBytes rides the wire with omitempty and 0 means unlimited: an
	// absent quota is "no limit", rendered as null, never a misleading 0-byte
	// cap.
	if b.QuotaBytes == 0 {
		state.QuotaBytes = types.Int64Null()
	} else {
		state.QuotaBytes = types.Int64Value(b.QuotaBytes)
	}
	// Tags ride the wire with omitempty: an absent map is "no tags", rendered
	// as null, never {}.
	if len(b.Tags) > 0 {
		tagsMap, mapDiags := types.MapValueFrom(ctx, types.StringType, b.Tags)
		diags.Append(mapDiags...)
		if diags.HasError() {
			return
		}
		state.Tags = tagsMap
	} else {
		state.Tags = types.MapNull(types.StringType)
	}
	state.CreatedAt = types.StringValue(b.CreatedAt)
	state.UpdatedAt = types.StringValue(b.UpdatedAt)
	state.TenantID = types.StringValue(b.TenantID)
}

// stringFromWire maps an empty optional string to null, not "".
func stringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}
