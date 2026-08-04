// Package bucket_cors_configuration implements the
// frostmoln_bucket_cors_configuration Terraform resource.
package bucket_cors_configuration

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// BucketCORSConfigurationModel is the Terraform state model for a bucket's CORS
// configuration. The bucket name is the resource's identity — the API exposes
// CORS as a single sub-resource per bucket, so there is nothing else to key on.
type BucketCORSConfigurationModel struct {
	Bucket types.String    `tfsdk:"bucket"`
	Rules  []corsRuleModel `tfsdk:"rules"`
}

type corsRuleModel struct {
	ID             types.String `tfsdk:"id"`
	AllowedOrigins types.Set    `tfsdk:"allowed_origins"`
	AllowedMethods types.Set    `tfsdk:"allowed_methods"`
	AllowedHeaders types.Set    `tfsdk:"allowed_headers"`
	ExposeHeaders  types.Set    `tfsdk:"expose_headers"`
	MaxAgeSeconds  types.Int64  `tfsdk:"max_age_seconds"`
}

// apiCORSRule is the API representation of one CORS rule. Field names match the
// storage service (storage/internal/domain/bucket.go). Every optional field
// carries omitempty on the wire, so an absent field and a zero value are the
// same thing to the API — which is why fromAPI maps zero values back to null
// rather than to "" / 0 / []. Without that a practitioner who omits
// max_age_seconds would read back 0, differ from their null config, and plan a
// change on every run.
type apiCORSRule struct {
	ID             string   `json:"id,omitempty"`
	AllowedOrigins []string `json:"allowedOrigins"`
	AllowedMethods []string `json:"allowedMethods"`
	AllowedHeaders []string `json:"allowedHeaders,omitempty"`
	ExposeHeaders  []string `json:"exposeHeaders,omitempty"`
	MaxAgeSeconds  int64    `json:"maxAgeSeconds,omitempty"`
}

// apiCORSRules is the request and response envelope. The service wraps the list
// in a "rules" object on both PUT and GET.
type apiCORSRules struct {
	Rules []apiCORSRule `json:"rules"`
}

// toAPI converts the Terraform model to the API request body.
func (m *BucketCORSConfigurationModel) toAPI(ctx context.Context) (apiCORSRules, diag.Diagnostics) {
	var diags diag.Diagnostics
	out := apiCORSRules{Rules: make([]apiCORSRule, 0, len(m.Rules))}

	for i := range m.Rules {
		rule := &m.Rules[i]
		apiRule := apiCORSRule{
			ID:            rule.ID.ValueString(),
			MaxAgeSeconds: rule.MaxAgeSeconds.ValueInt64(),
		}
		apiRule.AllowedOrigins = stringsFromSet(ctx, rule.AllowedOrigins, &diags)
		apiRule.AllowedMethods = stringsFromSet(ctx, rule.AllowedMethods, &diags)
		apiRule.AllowedHeaders = stringsFromSet(ctx, rule.AllowedHeaders, &diags)
		apiRule.ExposeHeaders = stringsFromSet(ctx, rule.ExposeHeaders, &diags)
		out.Rules = append(out.Rules, apiRule)
	}

	return out, diags
}

// fromAPI replaces the model's rules with what the API returned.
func (m *BucketCORSConfigurationModel) fromAPI(ctx context.Context, rules []apiCORSRule) diag.Diagnostics {
	var diags diag.Diagnostics
	out := make([]corsRuleModel, 0, len(rules))

	for _, rule := range rules {
		converted := corsRuleModel{
			ID:            nullableString(rule.ID),
			MaxAgeSeconds: nullableInt64(rule.MaxAgeSeconds),
		}
		converted.AllowedOrigins = setFromStrings(ctx, rule.AllowedOrigins, &diags)
		converted.AllowedMethods = setFromStrings(ctx, rule.AllowedMethods, &diags)
		converted.AllowedHeaders = setFromStrings(ctx, rule.AllowedHeaders, &diags)
		converted.ExposeHeaders = setFromStrings(ctx, rule.ExposeHeaders, &diags)
		out = append(out, converted)
	}

	m.Rules = out
	return diags
}

// stringsFromSet extracts a set into a []string, leaving it nil when the set is
// null or unknown so omitempty drops the key entirely.
func stringsFromSet(ctx context.Context, set types.Set, diags *diag.Diagnostics) []string {
	if set.IsNull() || set.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(set.ElementsAs(ctx, &out, false)...)
	return out
}

// setFromStrings converts a []string back to a set, mapping empty to null so an
// omitted optional list stays omitted.
func setFromStrings(ctx context.Context, values []string, diags *diag.Diagnostics) types.Set {
	if len(values) == 0 {
		return types.SetNull(types.StringType)
	}
	set, d := types.SetValueFrom(ctx, types.StringType, values)
	diags.Append(d...)
	return set
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
