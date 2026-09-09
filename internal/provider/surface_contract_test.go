package provider

import (
	"context"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

// allowedInlineCollections is the checked set of exceptions to the surface
// contract's two-shape rule (docs/guides/surface-contract.md): singleton
// configuration-document resources whose whole identity IS the collection —
// the aws_s3_bucket_cors_configuration shape. Their members have no per-ID
// existence (there is no bucket CORS rule resource to separate), so the rule
// that child members live in separate per-ID resources does not apply.
// Checked in both directions like the tables in planmodifier_order_test.go:
// an entry here that no longer exists fails, and a collection that appears
// without an entry fails, so neither side can drift silently.
var allowedInlineCollections = map[string][]string{
	"frostmoln_bucket_cors_configuration":      {"rules"},
	"frostmoln_bucket_lifecycle_configuration": {"rules"},
}

// TestSurfaceContract walks every registered resource's schema (the
// planmodifier_order_test.go pattern) and fails on ANY inline child
// collection — a nested list/set/map of objects, as an attribute or a block,
// at any depth — that is not allowlisted above.
//
// The deny is default, not name-based: the contract's answer to "should this
// child collection be inline?" is no for every name, so matching on `rules`,
// `routes`, `ingress`, `egress` would only catch the names already litigated
// and let the same shape sail through as `members` or `endpoints`. A new
// legitimate exception — a singleton configuration document, never a parent
// with per-ID children — is added here deliberately, with its justification,
// where a reviewer sees it.
func TestSurfaceContract(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p, ok := New("test")().(*FrostmolnProvider)
	if !ok {
		t.Fatal("expected New to return a *FrostmolnProvider")
	}

	seen := map[string][]string{}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()

		var mdResp resource.MetadataResponse
		r.Metadata(ctx, resource.MetadataRequest{ProviderTypeName: "frostmoln"}, &mdResp)
		var schemaResp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &schemaResp)

		typeName := mdResp.TypeName

		var found []string
		collectInlineChildCollections(t, "", schemaResp.Schema.Attributes, &found)
		collectInlineChildCollectionBlocks(t, "", schemaResp.Schema.Blocks, &found)
		sort.Strings(found)

		allowed := map[string]bool{}
		for _, attrPath := range allowedInlineCollections[typeName] {
			allowed[attrPath] = true
		}

		for _, attrPath := range found {
			if allowed[attrPath] {
				seen[typeName] = append(seen[typeName], attrPath)
				continue
			}
			t.Errorf("%s: %s is an inline child collection — child members must be separate per-ID "+
				"resources, never inlined into a parent schema and never mixed with them "+
				"(docs/guides/surface-contract.md)", typeName, attrPath)
		}

		for _, attrPath := range allowedInlineCollections[typeName] {
			if !containsPath(seen[typeName], attrPath) {
				t.Errorf("%s: %s is listed in allowedInlineCollections but no such inline collection "+
					"exists — remove the entry so the table cannot drift", typeName, attrPath)
			}
		}
	}

	for typeName := range allowedInlineCollections {
		if _, found := seen[typeName]; !found {
			t.Errorf("allowedInlineCollections lists %q, but it has no banned inline collection (or no longer exists)", typeName)
		}
	}
}

// collectInlineChildCollections reports the dotted path of every nested
// list/set/map attribute under attrs, recursing through nested objects at any
// depth. Scalar collections (a list of strings, a map of tags) cannot carry
// per-ID children and are not the shape this contract forbids. Unknown
// attribute types fail closed: a future nested shape must extend the walk,
// not pass it unexamined.
func collectInlineChildCollections(t *testing.T, prefix string, attrs map[string]schema.Attribute, found *[]string) {
	t.Helper()

	for name, a := range attrs {
		attrPath := name
		if prefix != "" {
			attrPath = prefix + "." + name
		}

		var children map[string]schema.Attribute
		switch nested := a.(type) {
		case schema.ListNestedAttribute:
			children = nested.NestedObject.Attributes
			*found = append(*found, attrPath)
		case schema.SetNestedAttribute:
			children = nested.NestedObject.Attributes
			*found = append(*found, attrPath)
		case schema.MapNestedAttribute:
			children = nested.NestedObject.Attributes
			*found = append(*found, attrPath)
		case schema.SingleNestedAttribute:
			// An object, not a collection — but its children are walked.
			children = nested.Attributes
		case schema.StringAttribute, schema.BoolAttribute, schema.Int64Attribute,
			schema.Float64Attribute, schema.ListAttribute, schema.SetAttribute,
			schema.MapAttribute, schema.DynamicAttribute:
			// Scalars and scalar collections: no object members, nothing to flag.
		default:
			t.Fatalf("%s: unsupported attribute type %T — extend the walk", attrPath, a)
		}
		collectInlineChildCollections(t, attrPath, children, found)
	}
}

// collectInlineChildCollectionBlocks is the block half of the walk. The
// provider declares no blocks today (planmodifier_order_test.go fails on the
// first one), but a block is exactly the shape this contract exists to catch,
// so the walk cannot afford to pass over them silently.
func collectInlineChildCollectionBlocks(t *testing.T, prefix string, blocks map[string]schema.Block, found *[]string) {
	t.Helper()

	for name, b := range blocks {
		attrPath := name
		if prefix != "" {
			attrPath = prefix + "." + name
		}

		var children map[string]schema.Attribute
		var childBlocks map[string]schema.Block
		switch nested := b.(type) {
		case schema.ListNestedBlock:
			children, childBlocks = nested.NestedObject.Attributes, nested.NestedObject.Blocks
			*found = append(*found, attrPath)
		case schema.SetNestedBlock:
			children, childBlocks = nested.NestedObject.Attributes, nested.NestedObject.Blocks
			*found = append(*found, attrPath)
		case schema.SingleNestedBlock:
			children, childBlocks = nested.Attributes, nested.Blocks
		default:
			t.Fatalf("%s: unsupported block type %T — extend the walk", attrPath, b)
		}

		collectInlineChildCollections(t, attrPath, children, found)
		collectInlineChildCollectionBlocks(t, attrPath, childBlocks, found)
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if p == want {
			return true
		}
	}
	return false
}
