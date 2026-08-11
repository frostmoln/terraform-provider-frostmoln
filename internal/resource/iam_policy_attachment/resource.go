package iam_policy_attachment

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &iamPolicyAttachmentResource{}
	_ resource.ResourceWithImportState = &iamPolicyAttachmentResource{}
)

// listPageLimit bounds each attachment-list page fetched while looking up an
// attachment; the loop follows nextCursor until the target is found or the pages
// are exhausted.
const listPageLimit = 100

// maxAttachmentPages caps the attachment-list scan (listPageLimit * this ≈ 100k
// rows) so a buggy or hostile gateway that never terminates the cursor can't hang
// a plan/apply. ponytail: fixed cap + a non-advancing-cursor guard; no real
// tenant has this many attachments on one policy.
const maxAttachmentPages = 1000

// NewResource returns a new IAM policy attachment resource factory.
func NewResource() resource.Resource {
	return &iamPolicyAttachmentResource{}
}

type iamPolicyAttachmentResource struct {
	client *client.Client
}

// attachmentsPath is the (non-tenant-scoped) collection path for a policy's
// attachments; the tenant is resolved from the caller's auth context.
func attachmentsPath(policyID string) string {
	return "/v1/iam/policies/" + url.PathEscape(policyID) + "/attachments"
}

// composeID / parseID map between the synthetic Terraform id and its parts. The
// three segments (policy id, attachee type, attachee id) never themselves contain
// a "/", so a plain 3-way split is unambiguous.
func composeID(policyID, attacheeType, attacheeID string) string {
	return policyID + "/" + attacheeType + "/" + attacheeID
}

func (r *iamPolicyAttachmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_iam_policy_attachment"
}

func (r *iamPolicyAttachmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Attaches an IAM access policy (`frostmoln_iam_policy`) to a machine principal " +
			"(an API key or workload identity) or a group. The attachment is immutable — changing " +
			"any attribute detaches and re-attaches. Use one attachment resource per policy/principal pair.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Synthetic identifier: `policy_id/attachee_type/attachee_id`.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"policy_id": schema.StringAttribute{
				Description: "The policy to attach.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"attachee_type": schema.StringAttribute{
				Description: "The kind of principal to attach to: `api_key`, `workload_identity`, or `group`.",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf(attacheeAPIKey, attacheeWorkloadIdentity, attacheeGroup),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"attachee_id": schema.StringAttribute{
				Description: "The identifier of the principal or group to attach the policy to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"attached_at": schema.StringAttribute{
				Description: "The timestamp when the policy was attached.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *iamPolicyAttachmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *iamPolicyAttachmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan IAMPolicyAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	policyID := plan.PolicyID.ValueString()
	attacheeType := plan.AttacheeType.ValueString()
	attacheeID := plan.AttacheeID.ValueString()

	_, err := r.client.Post(ctx, attachmentsPath(policyID), apiAttachRequest{
		AttacheeType: attacheeType,
		AttacheeID:   attacheeID,
	})
	if err != nil {
		resp.Diagnostics.AddError("Failed to attach IAM policy", err.Error())
		return
	}

	plan.ID = types.StringValue(composeID(policyID, attacheeType, attacheeID))

	// Attach returns 204 with no body; read the attachment back to record
	// attached_at. If it can't be found (e.g. the list is briefly consistent),
	// leave attached_at null — the attachment definitely exists (create succeeded).
	att, found, err := r.findAttachment(ctx, policyID, attacheeType, attacheeID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read IAM policy attachment", err.Error())
		return
	}
	if found {
		plan.AttachedAt = types.StringValue(att.AttachedAt)
	} else {
		plan.AttachedAt = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *iamPolicyAttachmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state IAMPolicyAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	policyID := state.PolicyID.ValueString()
	attacheeType := state.AttacheeType.ValueString()
	attacheeID := state.AttacheeID.ValueString()

	att, found, err := r.findAttachment(ctx, policyID, attacheeType, attacheeID)
	if err != nil {
		// A deleted policy makes the collection 404 — treat as the attachment
		// being gone rather than a hard error.
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read IAM policy attachment", err.Error())
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	state.ID = types.StringValue(composeID(policyID, attacheeType, attacheeID))
	state.AttachedAt = types.StringValue(att.AttachedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update can never be reached — every attribute forces replacement — but the
// interface requires it.
func (r *iamPolicyAttachmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan IAMPolicyAttachmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *iamPolicyAttachmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state IAMPolicyAttachmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	q := url.Values{}
	q.Set("attacheeType", state.AttacheeType.ValueString())
	q.Set("attacheeId", state.AttacheeID.ValueString())

	_, err := r.client.DeleteWithQuery(ctx, attachmentsPath(state.PolicyID.ValueString()), q)
	if err != nil {
		if client.IsNotFound(err) {
			return
		}
		// A workload-identity binding must never be left with zero grant sources
		// (ADR-0102), so detaching its LAST policy is refused server-side. That
		// lands here on a plain `terraform destroy` of a policy-granted binding:
		// the attachment references the binding, so Terraform destroys the
		// attachment FIRST and the whole run stops. The raw code says nothing
		// about the way out, which is to destroy the binding itself — deleting a
		// binding is deliberately not guarded and reaps its attachment rows.
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "BINDING_WOULD_HAVE_NO_GRANT" {
			resp.Diagnostics.AddError(
				"Cannot detach the workload identity binding's last grant",
				fmt.Sprintf(
					"%s\n\nThis attachment is the only thing granting workload identity %s, and a "+
						"granted binding is never left with nothing (ADR-0102).\n\n"+
						"To tear the pair down, destroy the BINDING first — that removes its "+
						"attachments with it:\n"+
						"  terraform destroy -target=frostmoln_workload_identity_binding.<name>\n\n"+
						"To keep the binding, give it another grant (scopes, or a second policy "+
						"attachment) before removing this one.",
					err.Error(), state.AttacheeID.ValueString(),
				),
			)
			return
		}
		resp.Diagnostics.AddError("Failed to detach IAM policy", err.Error())
	}
}

// ImportState parses the synthetic "policy_id/attachee_type/attachee_id" id.
func (r *iamPolicyAttachmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(req.ID, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("expected \"policy_id/attachee_type/attachee_id\", got %q", req.ID),
		)
		return
	}
	// Re-validate the attachee type here: the schema's OneOf validator runs on
	// config, not on import-set state, so without this a bogus type would only
	// self-heal on the next plan.
	switch parts[1] {
	case attacheeAPIKey, attacheeWorkloadIdentity, attacheeGroup:
	default:
		resp.Diagnostics.AddError(
			"Invalid import ID",
			fmt.Sprintf("attachee_type must be one of %q, %q, %q; got %q", attacheeAPIKey, attacheeWorkloadIdentity, attacheeGroup, parts[1]),
		)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("policy_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("attachee_type"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("attachee_id"), parts[2])...)
}

// findAttachment looks up the one attachment binding the policy to the given
// principal. It returns (attachment, true, nil) on a match, (nil, false, nil)
// when there is none, or a non-nil error on a transport failure (a 404 from a
// deleted policy surfaces via client.IsNotFound).
//
// It passes the attacheeType/attacheeId filter so a filter-aware identity
// (single-attachment lookup) answers in ONE request (a page of at most the one
// match, empty nextCursor) — the O(1) fast path. The bounded paging loop is kept
// as a version-compat fallback: an older identity ignores the unknown filter and
// returns the full list, which the loop still pages+matches correctly. Either way
// the per-row match check below is authoritative, so the filter is a pure
// optimisation and never changes the result.
func (r *iamPolicyAttachmentResource) findAttachment(ctx context.Context, policyID, attacheeType, attacheeID string) (*apiAttachment, bool, error) {
	cursor := ""
	for page := 0; page < maxAttachmentPages; page++ {
		q := url.Values{}
		q.Set("attacheeType", attacheeType)
		q.Set("attacheeId", attacheeID)
		if cursor != "" {
			q.Set("cursor", cursor)
		}
		q.Set("limit", strconv.Itoa(listPageLimit))

		apiResp, err := r.client.Get(ctx, attachmentsPath(policyID), q)
		if err != nil {
			return nil, false, err
		}
		list, err := client.ParseResponse[apiAttachmentList](apiResp)
		if err != nil {
			return nil, false, err
		}
		for i := range list.Attachments {
			a := list.Attachments[i]
			if a.AttacheeType == attacheeType && a.AttacheeID == attacheeID {
				return &a, true, nil
			}
		}
		if list.NextCursor == "" {
			return nil, false, nil
		}
		if list.NextCursor == cursor {
			return nil, false, fmt.Errorf("attachment list cursor did not advance (%q); aborting to avoid an infinite loop", cursor)
		}
		cursor = list.NextCursor
	}
	return nil, false, fmt.Errorf("attachment list exceeded %d pages; aborting", maxAttachmentPages)
}
