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
	Endpoint     types.String `tfsdk:"endpoint"`
	AccessKey    types.String `tfsdk:"access_key"`
	CreatedAt    types.String `tfsdk:"created_at"`
}

// apiBucket is the API representation of a bucket.
type apiBucket struct {
	Name         string            `json:"name"`
	Region       string            `json:"region"`
	StorageClass string            `json:"storageClass"`
	Versioning   string            `json:"versioning"`
	ObjectCount  int64             `json:"objectCount"`
	SizeBytes    int64             `json:"sizeBytes"`
	Endpoint     string            `json:"endpoint"`
	AccessKey    string            `json:"accessKey,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
	CreatedAt    string            `json:"createdAt"`
}

// apiCreateBucketRequest is the API request to create a bucket.
type apiCreateBucketRequest struct {
	Name         string            `json:"name"`
	Region       string            `json:"region,omitempty"`
	StorageClass string            `json:"storageClass,omitempty"`
	Versioning   string            `json:"versioning,omitempty"`
	Tags         map[string]string `json:"tags,omitempty"`
}

// apiUpdateBucketRequest is the API request to update a bucket.
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
		req.StorageClass = m.StorageClass.ValueString()
	}
	if !m.Versioning.IsNull() && !m.Versioning.IsUnknown() {
		req.Versioning = m.Versioning.ValueString()
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
	m.StorageClass = types.StringValue(b.StorageClass)
	m.Versioning = types.StringValue(b.Versioning)
	m.ObjectCount = types.Int64Value(b.ObjectCount)
	m.SizeBytes = types.Int64Value(b.SizeBytes)
	m.Endpoint = types.StringValue(b.Endpoint)
	m.CreatedAt = types.StringValue(b.CreatedAt)

	if b.AccessKey != "" {
		m.AccessKey = types.StringValue(b.AccessKey)
	}

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
