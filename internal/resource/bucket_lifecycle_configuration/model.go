// Package bucket_lifecycle_configuration implements the
// frostmoln_bucket_lifecycle_configuration Terraform resource.
package bucket_lifecycle_configuration

import (
	"cmp"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

// BucketLifecycleConfigurationModel is the Terraform state model for a bucket's
// lifecycle configuration. The bucket name is the resource's identity — the API
// exposes lifecycle as a single sub-resource per bucket.
type BucketLifecycleConfigurationModel struct {
	Bucket types.String         `tfsdk:"bucket"`
	Rules  []lifecycleRuleModel `tfsdk:"rules"`
}

type lifecycleRuleModel struct {
	ID                                 types.String `tfsdk:"id"`
	Enabled                            types.Bool   `tfsdk:"enabled"`
	Prefix                             types.String `tfsdk:"prefix"`
	ExpirationDays                     types.Int64  `tfsdk:"expiration_days"`
	NoncurrentVersionExpirationDays    types.Int64  `tfsdk:"noncurrent_version_expiration_days"`
	AbortIncompleteMultipartUploadDays types.Int64  `tfsdk:"abort_incomplete_multipart_upload_days"`
}

// apiLifecycleRule is the API representation of one lifecycle rule. Field names
// match the storage service (storage/internal/domain/bucket.go).
//
// The domain type also carries transitionDays, transitionStorageClass and a
// per-rule tags map. They are deliberately absent here: the object backend
// stores none of them, and the service rejects all three with 400. Adding them
// would offer a knob that cannot work.
//
// Every optional field carries omitempty on the wire, so an absent field and a
// zero value are indistinguishable to the API — which is why fromAPI maps zero
// values back to null rather than to "" / 0. Without that a practitioner who
// omits prefix would read back "" and plan a change on every run.
type apiLifecycleRule struct {
	ID                                 string `json:"id"`
	Enabled                            bool   `json:"enabled"`
	Prefix                             string `json:"prefix,omitempty"`
	ExpirationDays                     int64  `json:"expirationDays,omitempty"`
	NoncurrentVersionExpirationDays    int64  `json:"noncurrentVersionExpirationDays,omitempty"`
	AbortIncompleteMultipartUploadDays int64  `json:"abortIncompleteMultipartUploadDays,omitempty"`
}

// apiLifecycleRules is the request and response envelope. The service wraps the
// list in a "rules" object on both PUT and GET.
type apiLifecycleRules struct {
	Rules []apiLifecycleRule `json:"rules"`
}

// toAPI converts the Terraform model to the API request body.
func (m *BucketLifecycleConfigurationModel) toAPI() apiLifecycleRules {
	out := apiLifecycleRules{Rules: make([]apiLifecycleRule, 0, len(m.Rules))}

	for i := range m.Rules {
		rule := &m.Rules[i]
		out.Rules = append(out.Rules, apiLifecycleRule{
			ID:                                 rule.ID.ValueString(),
			Enabled:                            rule.Enabled.ValueBool(),
			Prefix:                             rule.Prefix.ValueString(),
			ExpirationDays:                     rule.ExpirationDays.ValueInt64(),
			NoncurrentVersionExpirationDays:    rule.NoncurrentVersionExpirationDays.ValueInt64(),
			AbortIncompleteMultipartUploadDays: rule.AbortIncompleteMultipartUploadDays.ValueInt64(),
		})
	}

	return out
}

// fromAPI replaces the model's rules with what the API returned, kept in the
// order the model already had them. RGW answers with the rules sorted by id,
// and `rules` is a list, so taking the API order verbatim planned a reorder on
// every run for any config not written in id order. Rule order carries no S3
// meaning, and the service rejects duplicate ids, so matching on id is safe
// (a duplicate would only affect order: nothing is dropped or repeated). Ids the
// model does not know (import, drift) go last, in API order.
func (m *BucketLifecycleConfigurationModel) fromAPI(rules []apiLifecycleRule) {
	pos := make(map[string]int, len(m.Rules))
	for i := range m.Rules {
		pos[m.Rules[i].ID.ValueString()] = i
	}
	rank := func(id string) int {
		if i, ok := pos[id]; ok {
			return i
		}
		return len(m.Rules)
	}
	rules = slices.Clone(rules)
	slices.SortStableFunc(rules, func(a, b apiLifecycleRule) int { return cmp.Compare(rank(a.ID), rank(b.ID)) })

	out := make([]lifecycleRuleModel, 0, len(rules))

	for _, rule := range rules {
		out = append(out, lifecycleRuleModel{
			ID:                                 types.StringValue(rule.ID),
			Enabled:                            types.BoolValue(rule.Enabled),
			Prefix:                             nullableString(rule.Prefix),
			ExpirationDays:                     nullableInt64(rule.ExpirationDays),
			NoncurrentVersionExpirationDays:    nullableInt64(rule.NoncurrentVersionExpirationDays),
			AbortIncompleteMultipartUploadDays: nullableInt64(rule.AbortIncompleteMultipartUploadDays),
		})
	}

	m.Rules = out
}

func nullableString(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

func nullableInt64(v int64) types.Int64 {
	if v == 0 {
		return types.Int64Null()
	}
	return types.Int64Value(v)
}
