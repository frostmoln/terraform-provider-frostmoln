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
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
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
	// different host than the API). Its 15m default timeout is a WIRE ceiling
	// on the one upload request, deliberately NOT one of the wait budgets:
	// the raised poll deadline bounds the in-guest poll AFTER the upload
	// completes, and the storage edge's presigned POST policy is the
	// server-side bound on how long an upload may legally take.
	uploadClient *http.Client
}

func (r *webserverDeploymentResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 5 * time.Second
}

// getPollTimeout is the DEFAULT wait budget — the timeouts block's fallback
// per verb. Create and update both poll the in-guest deploy to a terminal
// status; delete has no wait at all (see Delete). Raised from 15m to 2h
// (audit D3): the platform's own poll deadline for this workflow,
// deployPollDeadline (provisioning internal/workflow/deploy_webserver_content.go:24),
// is 2 hours — matched to the agent job's queue-side TTL so the workflow
// observes the deploy's TRUE terminal state instead of declaring a live
// deploy failed (the in-guest deploy itself is capped at 1h by the agent;
// 2h is the safety ceiling). A 15m ceiling gave up on a deploy the platform
// still considers live. The upload's HTTP client timeout is a separate wire
// timeout, not one of these budgets.
func (r *webserverDeploymentResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	return 2 * time.Hour
}

// resolveBudgets turns the configured timeouts block into effective budgets,
// falling back per verb to the same value this resource has always hardcoded.
// Routing the defaults through the accessor keeps the test-injection seam
// intact: a test that shrinks pollTimeout shrinks every wait that does not
// carry an explicit timeouts override, exactly as before. (The upload's
// HTTP client timeout is a separate wire timeout, not one of these budgets.)
func (r *webserverDeploymentResource) resolveBudgets(m *timeouts.Model) timeouts.Budgets {
	budgets, err := m.Resolve(timeouts.Uniform(r.getPollTimeout()))
	if err != nil {
		// Unreachable via HCL (the block validator rejects bad durations at
		// plan time); degrade to the defaults rather than fail a wait.
		return timeouts.Uniform(r.getPollTimeout())
	}
	return budgets
}

func (r *webserverDeploymentResource) getUploadClient() *http.Client {
	if r.uploadClient != nil {
		return r.uploadClient
	}
	return &http.Client{Timeout: 15 * time.Minute} // separate wire timeout, not a wait budget (see the field comment)
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
			"\"undeploy\" API; destroy the instance to remove it)." +
			"\n\n" + scopedecl.Summary("frostmoln_webserver_deployment"),
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
		Blocks: map[string]schema.Block{
			// Customer-tunable wait budgets: default 2h per verb (15m before
			// 2026-09-16 — audit D3: the platform's own deployPollDeadline
			// for the in-guest deploy is 2h, agent capped at 1h + safety).
			// Create and update
			// both run the deploy flow, whose poll to a terminal deploy
			// status is bounded by the matching budget; delete is a
			// deliberate no-op — there is no "undeploy" API, so destroying
			// the resource only drops it from state and budgets.Delete
			// bounds nothing. The presigned upload's HTTP client timeout is
			// a separate wire timeout, not a wait budget. A timeouts change
			// is an in-place no-op on real infrastructure — verified by the
			// Gate 2 smoke test (project-docs/product/TF-CONVERGENCE-WALL-PLAN.md).
			"timeouts": timeouts.Schema(),
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

	// An unreadable archive is a WARNING here, not an error: an error raised while
	// planning also aborts `terraform destroy`, whose refresh phase runs ModifyPlan
	// against an ordinary non-null plan — so an archive that CI built and cleaned up
	// (or a path that moved) would trap the resource, undestroyable. Create/Update
	// hash the file themselves and fail hard there, which a destroy never reaches;
	// leaving the derived attributes unknown keeps the plan honest until then.
	hash, err := hashArchiveFile(plan.SourceArchive.ValueString())
	if err != nil {
		resp.Diagnostics.AddWarning(
			"Cannot read source_archive",
			fmt.Sprintf("Failed to read the deploy archive %q to compute its hash: %s. An apply that deploys this archive will fail; a destroy is unaffected.", plan.SourceArchive.ValueString(), err),
		)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("source_hash"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("deploy_id"), types.StringUnknown())...)
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
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

	// The customer's timeouts block, falling back to this resource's
	// defaults (see resolveBudgets above for what each default is now).
	budgets := r.resolveBudgets(plan.Timeouts)

	hash, err := hashArchiveFile(archivePath)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read source_archive", err.Error())
		return
	}

	outcome, err := r.runDeploy(ctx, instanceID, archivePath, hash, budgets.Create)
	if err == nil {
		plan.ID = types.StringValue(instanceID)
		plan.SourceHash = types.StringValue(hash)
		plan.DeployID = types.StringValue(outcome.DeployID)
		plan.Status = types.StringValue(outcome.Status)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	if outcome.DeployID == "" {
		// The create-deploy call itself failed: no deploy record exists,
		// nothing is recorded in state, and re-applying is safe.
		resp.Diagnostics.AddError("Content deploy failed", err.Error())
		return
	}

	if outcome.Unresolved {
		// The orphan contract's adopt-and-track arm (the provider stopped
		// waiting while the deploy may still land, and the start was already
		// accepted). Track the deploy BEFORE the error: the framework persists
		// state alongside Create errors, and a tracked row is what keeps the
		// deploy refreshable and the next plan honest. The state row carries
		// the observability facts this flow knows (deploy id, archive hash)
		// plus one honest read for the current status.
		plan.ID = types.StringValue(instanceID)
		plan.SourceHash = types.StringValue(hash)
		plan.DeployID = types.StringValue(outcome.DeployID)
		plan.Status = types.StringNull()
		if d, readErr := r.getDeploy(ctx, instanceID, outcome.DeployID); readErr == nil {
			plan.fromAPI(d)
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		// Whether or not the state write landed, the outcome error must
		// reach the practitioner — it carries the deploy id, the taint
		// semantics and the do-not-`state rm` warning (a failed Set is
		// itself diagnosed; losing the outcome text on top of it would
		// hide the observability handle).
		statusWord := "not observed (the record exists; refresh settles it)"
		if !plan.Status.IsNull() {
			statusWord = fmt.Sprintf("last read as %q", plan.Status.ValueString())
		}
		resp.Diagnostics.AddError(
			"Content Deploy Outcome Unknown — The Deploy Is Tracked In State",
			fmt.Sprintf("The deploy flow was accepted by the platform (deploy %s created, archive uploaded, start accepted), "+
				"but the provider could not observe a terminal status within its wait: the in-guest agent may still be "+
				"extracting and publishing the release, or the deploy may have failed after the provider stopped looking "+
				"(status %s).\n\n"+
				"The deploy has been adopted into Terraform state, and the failed create marks the resource TAINTED: "+
				"the next apply destroys (a state-only drop — there is no undeploy API) and recreates it, which runs a "+
				"fresh deploy of the same archive; refresh first if you would rather keep whatever the deploy landed. "+
				"Do NOT run `terraform state rm` — it is the one action that re-creates the forgetting this exists to "+
				"prevent. To force a fresh deploy without waiting for taint, `terraform apply -replace=EXAMPLE_ADDRESS`, "+
				"where EXAMPLE_ADDRESS is this resource's own address in your configuration (e.g. "+
				"frostmoln_webserver_deployment.mysite).\n\n"+
				"The wait gave up with: %s",
				outcome.DeployID, statusWord, err),
		)
		return
	}

	// A definite failure after the deploy record was created (upload
	// rejected, start refused, or the agent reported the deploy failed):
	// nothing was published, so nothing is tracked in state and re-applying
	// runs a fresh deploy once the reason below is dealt with. The deploy
	// record itself remains on the instance as inert platform metadata.
	resp.Diagnostics.AddError("Content deploy failed",
		fmt.Sprintf("%s\n\nThe deploy %s is recorded on the instance but not tracked in Terraform state — "+
			"re-applying runs a fresh deploy once the reason is dealt with.", err.Error(), outcome.DeployID))
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

	d, err := r.getDeploy(ctx, state.InstanceID.ValueString(), state.DeployID.ValueString())
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

	// Refresh deploy_id/status; preserve the write-only source_archive/source_hash
	// and the create-time id/instance_id the API never returns.
	state.fromAPI(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// getDeploy reads one deploy record of an instance and parses it — the one
// read everyone (refresh, the adoption's honest read, the status poll) shares.
func (r *webserverDeploymentResource) getDeploy(ctx context.Context, instanceID, deployID string) (*apiDeploy, error) {
	apiResp, err := r.client.Get(ctx, r.client.TenantPath("/webservers/"+instanceID+"/deploys/"+deployID), nil)
	if err != nil {
		return nil, err
	}
	return client.ParseResponse[apiDeploy](apiResp)
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

	// The customer's timeouts block, falling back to this resource's
	// defaults (see resolveBudgets above for what each default is now).
	budgets := r.resolveBudgets(plan.Timeouts)

	outcome, err := r.runDeploy(ctx, instanceID, archivePath, hash, budgets.Update)
	if err != nil {
		// The framework keeps the PRIOR state on an update error, so nothing
		// here needs surgery — but the deploy's observability handle must be
		// named either way. An unresolved outcome self-heals through the HASH,
		// not through refresh: state still carries the previous archive's
		// source_hash, so the next apply still sees a hash change and
		// redeploys (a possibly-landed deploy merely re-runs the same archive).
		// Refresh cannot see the new deploy — state holds the old deploy id.
		if outcome.DeployID != "" {
			resp.Diagnostics.AddError("Content deploy failed",
				fmt.Sprintf("%s\n\nThe deploy %s is recorded on the instance; the previous deploy's state is kept. "+
					"The next apply re-runs the deploy (state still carries the previous archive's hash); "+
					"a deploy that was still running merely re-runs the same archive.",
					err.Error(), outcome.DeployID))
			return
		}
		resp.Diagnostics.AddError("Content deploy failed", err.Error())
		return
	}

	plan.DeployID = types.StringValue(outcome.DeployID)
	plan.Status = types.StringValue(outcome.Status)
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

// deployOutcome carries what a deploy flow established when the provider
// stopped driving it. deployID is set as soon as the deploy record exists on
// the instance (the create-deploy call succeeded) — even when every later step
// failed — because that record is the observability handle the practitioner
// needs. unresolved is the orphan contract's UNKNOWN arm: the provider stopped
// waiting while the deploy may still land. A definite outcome (upload
// rejected, start refused, agent-reported failed) is never unresolved.
type deployOutcome struct {
	DeployID   string
	Status     string
	Unresolved bool
}

// runDeploy executes the ADR-0091 content-deploy flow against a webserver
// instance: create the deploy (presigned POST policy) -> upload the archive ->
// start with the checksum -> poll to terminal. Its error return means the
// deploy did not succeed; outcome.DeployID says how far it got, outcome.
// Unresolved says whether the deploy may still be running (the adopt-and-track
// arm) or definitively did not succeed (upload/start/agent failure — re-applying
// runs a fresh deploy). The deploy-status poll runs on the timeouts block's
// budget for the running operation (create or update); the upload's HTTP client
// timeout is a separate wire timeout, not a wait budget.
func (r *webserverDeploymentResource) runDeploy(ctx context.Context, instanceID, archivePath, sha256 string, budget time.Duration) (deployOutcome, error) {
	// 1. Create the deploy — the response carries the presigned POST policy.
	createResp, err := r.client.Post(ctx, r.client.TenantPath("/webservers/"+instanceID+"/deploys"), nil)
	if err != nil {
		return deployOutcome{}, fmt.Errorf("create deploy: %w", err)
	}
	created, err := client.ParseResponse[apiCreateDeployResponse](createResp)
	if err != nil {
		return deployOutcome{}, fmt.Errorf("parse create-deploy response: %w", err)
	}
	if created.DeployID == "" || created.UploadURL == "" {
		return deployOutcome{}, fmt.Errorf("create-deploy response missing deployId or uploadUrl")
	}
	outcome := deployOutcome{DeployID: created.DeployID}

	// 2. Upload the archive using the signed POST policy. A rejected upload
	// ends the flow definitively: the deploy record exists but was never
	// started, so nothing was published and a fresh deploy is safe.
	if err := r.uploadArchive(ctx, created.UploadURL, created.UploadFields, archivePath); err != nil {
		return outcome, err
	}

	// 3. Start the deploy with the archive checksum. Same definiteness.
	if _, err := r.client.Post(ctx, r.client.TenantPath("/webservers/"+instanceID+"/deploys/"+created.DeployID+"/start"), apiStartDeployRequest{SHA256: sha256}); err != nil {
		return outcome, fmt.Errorf("start deploy: %w", err)
	}

	// 4. Poll until the deploy reaches a terminal state.
	final, unresolved, err := r.waitForDeploy(ctx, instanceID, created.DeployID, budget)
	if err != nil {
		outcome.Unresolved = unresolved
		if final != nil {
			outcome.Status = final.Status
		}
		return outcome, err
	}
	outcome.Status = final.Status
	return outcome, nil
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

// waitForDeploy polls the deploy until it reaches a terminal state. Exactly two
// outcomes are definite: the agent reported the deploy FAILED (the platform's
// own decision — nothing was published, and a fresh deploy is safe), or it
// succeeded. Everything else (the wait budget elapsed, a poll error to the
// deadline) is UNRESOLVED: the deploy may still be running or may land after
// the provider stopped looking, which is the adopt-and-track arm of the orphan
// contract. The wait budget is the timeouts block's budget for the running
// operation (create or update).
func (r *webserverDeploymentResource) waitForDeploy(ctx context.Context, instanceID, deployID string, budget time.Duration) (*apiDeploy, bool, error) {
	var last *apiDeploy
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      budget,
		TargetStates: []string{deployStatusSucceeded},
		ErrorStates:  []string{deployStatusFailed},
		ResourceName: "webserver_deployment",
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.getDeploy(pollCtx, instanceID, deployID)
			if pollErr != nil {
				return "", pollErr
			}
			last = pollResp
			return pollResp.Status, nil
		},
	})
	if err != nil {
		if last != nil && last.Status == deployStatusFailed {
			message := last.ErrorMessage
			if message == "" {
				message = "the agent reported the deploy failed without an error message"
			}
			return last, false, fmt.Errorf("deploy %s failed: %s", deployID, message)
		}
		return last, true, err
	}
	return last, false, nil
}
