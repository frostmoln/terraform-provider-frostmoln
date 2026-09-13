// Package bucket implements the fm_bucket Terraform resource.
package bucket

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/tftags"
)

// BucketModel is the Terraform state model for a bucket.
type BucketModel struct {
	Name         types.String `tfsdk:"name"`
	Region       types.String `tfsdk:"region"`
	StorageClass types.String `tfsdk:"storage_class"`
	Versioning   types.String `tfsdk:"versioning"`
	Tags         types.Map    `tfsdk:"tags"`
	TagsAll      types.Map    `tfsdk:"tags_all"`
	ObjectCount  types.Int64  `tfsdk:"object_count"`
	SizeBytes    types.Int64  `tfsdk:"size_bytes"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

// apiBucket is the API representation of a bucket. Field names match the storage
// service (storage/internal/domain/bucket.go): the storage class is
// `defaultStorageClass`, the size is `totalSize`, versioning is a string enum on
// read, and there is no endpoint/accessKey (S3 creds come from the dedicated
// credentials endpoint, ADR-0030).
type apiBucket struct {
	Name                string            `json:"name"`
	Region              string            `json:"region,omitempty"`
	DefaultStorageClass string            `json:"defaultStorageClass"`
	Versioning          string            `json:"versioning"`
	ObjectCount         int64             `json:"objectCount"`
	TotalSize           int64             `json:"totalSize"`
	Tags                map[string]string `json:"tags,omitempty"`
	CreatedAt           string            `json:"createdAt"`
}

// apiCreateBucketRequest is the API request to create a bucket. On create the
// storage service expects `versioning` as a bool (enable on/off) and the storage
// class under `defaultStorageClass`.
type apiCreateBucketRequest struct {
	Name                string            `json:"name"`
	Region              string            `json:"region,omitempty"`
	DefaultStorageClass string            `json:"defaultStorageClass,omitempty"`
	Versioning          bool              `json:"versioning,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
}

// apiUpdateBucketRequest is the API request to update a bucket. On update
// `versioning` is a string enum (disabled/enabled/suspended).
// Tags carries no omitempty and is always sent: the server replaces the
// bucket's user tags only when the key is present, so with omitempty there was
// no way to express "remove every tag" — `tags = {}` marshalled to nothing, the
// server kept the old tags, and reading them back against a config that says
// none is an inconsistent-result error with no HCL that can fix it.
type apiUpdateBucketRequest struct {
	Versioning *string           `json:"versioning,omitempty"`
	Tags       map[string]string `json:"tags"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *BucketModel) toCreateRequest() apiCreateBucketRequest {
	req := apiCreateBucketRequest{
		Name: m.Name.ValueString(),
	}

	if !m.Region.IsNull() && !m.Region.IsUnknown() {
		req.Region = m.Region.ValueString()
	}
	if !m.StorageClass.IsNull() && !m.StorageClass.IsUnknown() {
		req.DefaultStorageClass = m.StorageClass.ValueString()
	}
	// Create takes versioning as a bool (enable on/off). A string of "enabled"
	// turns it on; everything else (disabled/suspended/unset) leaves it off, and
	// "suspended" can be applied with a follow-up update.
	if !m.Versioning.IsNull() && !m.Versioning.IsUnknown() {
		req.Versioning = m.Versioning.ValueString() == "enabled"
	}

	// Tags are set by Create, which merges the provider's default_tags into
	// them (tftags.ForCreate).
	return req
}

// toUpdateRequest converts the Terraform model to an API update request. Tags
// are set by Update: always the full desired set, merged with the provider's
// default_tags and the keys it does not manage (tftags.ForUpdate), so a
// removed tags block or an empty map clears the bucket's tags rather than
// silently leaving them in place.
func (m *BucketModel) toUpdateRequest() apiUpdateBucketRequest {
	req := apiUpdateBucketRequest{}

	if !m.Versioning.IsNull() && !m.Versioning.IsUnknown() {
		v := m.Versioning.ValueString()
		req.Versioning = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *BucketModel) fromAPI(ctx context.Context, b *apiBucket) diag.Diagnostics {
	m.Name = types.StringValue(b.Name)
	m.Region = types.StringValue(b.Region)
	m.StorageClass = types.StringValue(b.DefaultStorageClass)
	m.Versioning = types.StringValue(b.Versioning)
	m.ObjectCount = types.Int64Value(b.ObjectCount)
	m.SizeBytes = types.Int64Value(b.TotalSize)
	m.CreatedAt = types.StringValue(b.CreatedAt)

	// storage omits an empty tag map, so an absent `tags` is "no tags", never
	// "unchanged": keeping the model's tags here hid an out-of-band removal.
	// storage already strips its six control-plane keys from this map
	// (IsReservedBucketTagKey), so there is nothing to filter here.
	var diags diag.Diagnostics
	m.Tags, m.TagsAll = tftags.ReadBack(ctx, b.customerTags(), m.Tags, &diags)

	return diags
}

// customerTags is the tag set the platform holds on the object, as Terraform
// sees it: platform-reserved keys filtered out. The read-back and the fresh
// read an update makes before it writes (tftags.Prior.WithCurrent) both use
// it, so the two cannot disagree about what counts as a tag.
func (a *apiBucket) customerTags() map[string]string {
	return a.Tags
}
