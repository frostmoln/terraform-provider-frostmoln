package webserver_deployment

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource                = &webserverDeploymentResource{}
	_ resource.ResourceWithImportState = &webserverDeploymentResource{}
	_ resource.ResourceWithModifyPlan  = &webserverDeploymentResource{}
)

// NewResource returns a new webserver_deployment resource factory.
func NewResource() resource.Resource {
	return &webserverDeploymentResource{}
}

type webserverDeploymentResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
	// uploadClient sends the presigned multipart POST to the storage edge (a
	// different host than the API). nil uses a default with a generous timeout.
	uploadClient *http.Client
}

func (r *webserverDeploymentResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

func (r *webserverDeploymentResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 15 * time.Minute
}

func (r *webserverDeploymentResource) getUploadClient() *http.Client {
	if r.uploadClient != nil {
		return r.uploadClient
	}
	return &http.Client{Timeout: 15 * time.Minute}
}

func (r *webserverDeploymentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_webserver_deployment"
}

func (r *webserverDeploymentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Deploys static/PHP site content to a managed Apache or Nginx webserver instance. " +
			"The provider runs the full deploy flow: it requests a presigned upload from the " +
			"platform, uploads the local archive, starts the deploy with the archive checksum, and " +
			"waits for the in-guest agent to verify, extract, and publish the release. A changed archive " +
			"(detected via source_hash) triggers a new deploy. Destroying the resource only drops it from " +
			"Terraform state — content already published on the instance is left in place (there is no " +
			"\"undeploy\" API; destroy the instance to remove it).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "Synthetic resource identifier (equal to instance_id — one deployment per instance).",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"instance_id": schema.StringAttribute{
				Description: "The ID of the Apache or Nginx webserver instance to deploy content to.",
				Required:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source_archive": schema.StringAttribute{
				Description: "Local filesystem path to the .tar.gz archive to deploy (the archive's top level " +
					"becomes the site document root). The provider reads this file at plan and apply time; it is " +
					"never uploaded to or returned by the API as-is — only its SHA-256 (source_hash) is tracked in " +
					"state. Changing the file's contents changes source_hash and triggers a new deploy.",
				Required: true,
			},
			"source_hash": schema.StringAttribute{
				Description: "SHA-256 hex digest of source_archive, computed by the provider at plan time. It is " +
					"the change driver: when the archive's bytes change, this hash changes and a new deploy runs. " +
					"It is also sent to the deploy's start call so the in-guest agent can verify the upload.",
				Computed: true,
			},
			"deploy_id": schema.StringAttribute{
				Description: "The ID of the most recent content deploy for this instance.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "Terminal status of the most recent deploy (succeeded once applied).",
				Computed:    true,
			},
		},
	}
}

func (r *webserverDeploymentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// ModifyPlan computes source_hash from the archive file at plan time so a change
// to the archive's contents (even under an unchanged path) surfaces as a diff and
// drives a new deploy. When the hash changes on an existing resource, deploy_id
// and status are marked unknown so their post-apply values are accepted.
func (r *webserverDeploymentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// Destroy: no plan to modify.
	if req.Plan.Raw.IsNull() {
		return
	}

	var plan WebserverDeploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// The archive path itself may be interpolated from another resource and not
	// yet known; then the hash (and anything derived from it) is known only after
	// apply.
	if plan.SourceArchive.IsUnknown() || plan.SourceArchive.IsNull() {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("source_hash"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("deploy_id"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
		return
	}

	hash, err := hashArchiveFile(plan.SourceArchive.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Cannot read source_archive",
			fmt.Sprintf("Failed to read the deploy archive %q to compute its hash: %s", plan.SourceArchive.ValueString(), err),
		)
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("source_hash"), types.StringValue(hash))...)

	// On an update, when the archive changed the deploy_id and status will change
	// too — mark them unknown so Terraform accepts the values Update writes.
	if !req.State.Raw.IsNull() {
		var state WebserverDeploymentModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if state.SourceHash.ValueString() != hash {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("deploy_id"), types.StringUnknown())...)
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
		}
	}
}

func (r *webserverDeploymentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan WebserverDeploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := plan.InstanceID.ValueString()
	archivePath := plan.SourceArchive.ValueString()

	hash, err := hashArchiveFile(archivePath)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read source_archive", err.Error())
		return
	}

	deployID, status, err := r.runDeploy(ctx, instanceID, archivePath, hash)
	if err != nil {
		resp.Diagnostics.AddError("Content deploy failed", err.Error())
		return
	}

	plan.ID = types.StringValue(instanceID)
	plan.SourceHash = types.StringValue(hash)
	plan.DeployID = types.StringValue(deployID)
	plan.Status = types.StringValue(status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webserverDeploymentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state WebserverDeploymentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Nothing to refresh until a deploy has been recorded.
	if state.DeployID.IsNull() || state.DeployID.ValueString() == "" {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+state.InstanceID.ValueString()+"/deploys/"+state.DeployID.ValueString()), nil)
	if err != nil {
		if client.IsNotFound(err) {
			// The deploy record (or the instance) is gone; drop it from state so a
			// re-apply redeploys.
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read webserver deploy", err.Error())
		return
	}

	d, err := client.ParseResponse[apiDeploy](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse webserver deploy response", err.Error())
		return
	}

	// Refresh deploy_id/status; preserve the write-only source_archive/source_hash
	// and the create-time id/instance_id the API never returns.
	state.fromAPI(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *webserverDeploymentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan WebserverDeploymentModel
	var state WebserverDeploymentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	instanceID := plan.InstanceID.ValueString() // == state (instance_id is RequiresReplace)
	archivePath := plan.SourceArchive.ValueString()

	hash, err := hashArchiveFile(archivePath)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read source_archive", err.Error())
		return
	}

	plan.ID = state.ID
	plan.SourceHash = types.StringValue(hash)

	// Redeploy only when the archive's bytes actually changed. A pure path rename
	// to identical content just refreshes state without touching the instance.
	if hash == state.SourceHash.ValueString() {
		plan.DeployID = state.DeployID
		plan.Status = state.Status
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	deployID, status, err := r.runDeploy(ctx, instanceID, archivePath, hash)
	if err != nil {
		resp.Diagnostics.AddError("Content deploy failed", err.Error())
		return
	}

	plan.DeployID = types.StringValue(deployID)
	plan.Status = types.StringValue(status)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *webserverDeploymentResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
	// A deployment represents content already published in-guest; there is no
	// "undeploy" API. Removing the resource only drops it from Terraform state (the
	// framework does that automatically when Delete returns) — the served content on
	// the instance is intentionally left intact. Destroy the instance to remove it.
}

// ImportState seeds both id and instance_id from the import ID (the instance id).
// source_archive/source_hash cannot be recovered (the API never returns them), so
// the archive must be re-declared in configuration after import.
func (r *webserverDeploymentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("instance_id"), req.ID)...)
}

// runDeploy executes the ADR-0091 content-deploy flow against a webserver
// instance: create the deploy (presigned POST policy) -> upload the archive ->
// start with the checksum -> poll to terminal. It returns the deploy id and its
// terminal status. On a failed deploy it returns the agent's error message.
func (r *webserverDeploymentResource) runDeploy(ctx context.Context, instanceID, archivePath, sha256 string) (string, string, error) {
	// 1. Create the deploy — the response carries the presigned POST policy.
	createResp, err := r.client.Post(ctx, r.client.TenantPath("/webservers/"+instanceID+"/deploys"), nil)
	if err != nil {
		return "", "", fmt.Errorf("create deploy: %w", err)
	}
	created, err := client.ParseResponse[apiCreateDeployResponse](createResp)
	if err != nil {
		return "", "", fmt.Errorf("parse create-deploy response: %w", err)
	}
	if created.DeployID == "" || created.UploadURL == "" {
		return "", "", fmt.Errorf("create-deploy response missing deployId or uploadUrl")
	}

	// 2. Upload the archive using the signed POST policy.
	if err := r.uploadArchive(ctx, created.UploadURL, created.UploadFields, archivePath); err != nil {
		return created.DeployID, "", err
	}

	// 3. Start the deploy with the archive checksum.
	if _, err := r.client.Post(ctx, r.client.TenantPath("/webservers/"+instanceID+"/deploys/"+created.DeployID+"/start"), apiStartDeployRequest{SHA256: sha256}); err != nil {
		return created.DeployID, "", fmt.Errorf("start deploy: %w", err)
	}

	// 4. Poll until the deploy reaches a terminal state.
	final, err := r.waitForDeploy(ctx, instanceID, created.DeployID)
	if err != nil {
		return created.DeployID, "", err
	}
	return final.ID, final.Status, nil
}

// uploadArchive POSTs the archive to the presigned storage endpoint as
// multipart/form-data: every signed policy field verbatim, then the archive bytes
// as the trailing "file" field (the file MUST be last for an S3 POST policy).
func (r *webserverDeploymentResource) uploadArchive(ctx context.Context, uploadURL string, fields map[string]string, archivePath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = f.Close() }()

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return fmt.Errorf("write upload field %q: %w", k, err)
		}
	}
	part, err := w.CreateFormFile("file", filepath.Base(archivePath))
	if err != nil {
		return fmt.Errorf("create file part: %w", err)
	}
	if _, err := io.Copy(part, f); err != nil {
		return fmt.Errorf("copy archive into request: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finalize multipart body: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	httpReq.Header.Set("Content-Type", w.FormDataContentType())

	httpResp, err := r.getUploadClient().Do(httpReq)
	if err != nil {
		return fmt.Errorf("upload archive: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		return fmt.Errorf("archive upload rejected: HTTP %d: %s", httpResp.StatusCode, string(msg))
	}
	return nil
}

// waitForDeploy polls the deploy until it reaches a terminal state, returning the
// final deploy (with the agent's error message on failure).
func (r *webserverDeploymentResource) waitForDeploy(ctx context.Context, instanceID, deployID string) (*apiDeploy, error) {
	var last *apiDeploy
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{deployStatusSucceeded},
		ErrorStates:  []string{deployStatusFailed},
		ResourceName: "webserver_deployment",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, r.client.TenantPath("/webservers/"+instanceID+"/deploys/"+deployID), nil)
			if pollErr != nil {
				return "", pollErr
			}
			d, parseErr := client.ParseResponse[apiDeploy](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			last = d
			return d.Status, nil
		},
	})
	if err != nil {
		if last != nil && last.ErrorMessage != "" {
			return nil, fmt.Errorf("deploy %s failed: %s", deployID, last.ErrorMessage)
		}
		return nil, err
	}
	return last, nil
}
