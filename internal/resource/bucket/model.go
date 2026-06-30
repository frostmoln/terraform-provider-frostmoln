// Package bucket implements the fm_bucket Terraform resource.
package bucket

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// BucketModel is the Terraform state model for a bucket.
type BucketModel struct {
	Name         types.String `tfsdk:"name"`
	Region       types.String `tfsdk:"region"`
	StorageClass types.String `tfsdk:"storage_class"`
	Versioning   types.String `tfsdk:"versioning"`
	Tags         types.Map    `tfsdk:"tags"`
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
type apiUpdateBucketRequest struct {
	Versioning *string           `json:"versioning,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *BucketModel) toCreateRequest(ctx context.Context) (apiCreateBucketRequest, diag.Diagnostics) {
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

	var diags diag.Diagnostics
	if !m.Tags.IsNull() && !m.Tags.IsUnknown() {
		tags := make(map[string]string)
		diags = m.Tags.ElementsAs(ctx, &tags, false)
		req.Tags = tags
	}

	return req, diags
}

// toUpdateRequest converts the Terraform model to an API update request.
func (m *BucketModel) toUpdateRequest(ctx context.Context) (apiUpdateBucketRequest, diag.Diagnostics) {
	req := apiUpdateBucketRequest{}

	if !m.Versioning.IsNull() && !m.Versioning.IsUnknown() {
		v := m.Versioning.ValueString()
		req.Versioning = &v
	}

	var diags diag.Diagnostics
	if !m.Tags.IsNull() {
		tags := make(map[string]string)
		diags = m.Tags.ElementsAs(ctx, &tags, false)
		req.Tags = tags
	}

	return req, diags
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

	if b.Tags != nil {
		tags, diags := types.MapValueFrom(ctx, types.StringType, b.Tags)
		if diags.HasError() {
			return diags
		}
		m.Tags = tags
	} else if m.Tags.IsNull() {
		m.Tags = types.MapNull(types.StringType)
	}

	return nil
}
