package image

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var (
	_ resource.Resource              = &imageResource{}
	_ resource.ResourceWithConfigure = &imageResource{}
)

// NewResource returns a new image resource factory.
func NewResource() resource.Resource {
	return &imageResource{}
}

type imageResource struct {
	client       *client.Client
	pollInterval time.Duration
	pollTimeout  time.Duration
	// uploadClient sends the presigned multipart POST to the storage edge (a
	// different host than the API). nil uses a default with a generous timeout.
	uploadClient *http.Client
}

func (r *imageResource) getPollInterval() time.Duration {
	if r.pollInterval > 0 {
		return r.pollInterval
	}
	return 10 * time.Second
}

func (r *imageResource) getPollTimeout() time.Duration {
	if r.pollTimeout > 0 {
		return r.pollTimeout
	}
	// Glance has to fetch the staged object and convert it to raw before the
	// image goes active; on a multi-gigabyte qcow2 that is minutes, not seconds.
	return 60 * time.Minute
}

func (r *imageResource) getUploadClient() *http.Client {
	if r.uploadClient != nil {
		return r.uploadClient
	}
	// One hour, because that is the ceiling RGW puts on the presigned form
	// itself: an upload still running past it is going to be rejected anyway,
	// and a shorter client timeout would kill a legitimate slow-link upload the
	// server would still have accepted.
	return &http.Client{
		Timeout: 60 * time.Minute,
		// Never follow a redirect. The body is an io.MultiReader wrapping an open
		// file, so GetBody is nil and net/http cannot replay it — under the
		// default policy a 302 from the storage edge silently downgrades the POST
		// to a bodyless GET, which "succeeds" and stages nothing. Returning the
		// 3xx instead lets the status check below report it as the failure it is.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (r *imageResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_image"
}

func (r *imageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a customer custom image (bring-your-own-image). The provider runs the full " +
			"flow: it creates the image record, uploads the local disk image straight to Frostmoln object " +
			"storage with the presigned form the platform returns, asks the platform to import it, and waits " +
			"for the image to reach \"active\". Custom images require the custom-images entitlement; without " +
			"it the API refuses the create. Only name, description, min_disk_gb and min_ram_mb can be changed " +
			"in place — every other attribute, including source_file, replaces the image. Create waits up to " +
			"60 minutes for the import to finish; that budget is not configurable.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the image.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the image. Between 1 and 255 characters.",
				Required:    true,
				Validators: []validator.String{
					// Mirrors compute's binding (min=1,max=255) so an over-long
					// name fails at plan time rather than as a 400 mid-apply.
					stringvalidator.LengthBetween(1, 255),
				},
			},
			"description": schema.StringAttribute{
				Description: "A human-readable description of the image.",
				Optional:    true,
			},
			"source_file": schema.StringAttribute{
				Description: "Local filesystem path to the disk image to upload, read by the machine running " +
					"Terraform — not a bucket key or a URL. Its size is checked against the platform's upload " +
					"limit before any bytes are sent. Terraform cannot see a change to the file's CONTENTS " +
					"under an unchanged path: pair it with source_file_hash if you want a rebuilt image to " +
					"trigger a replacement.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"source_file_hash": schema.StringAttribute{
				Description: "Optional change trigger for the CONTENTS of source_file, typically " +
					"filemd5(\"...\") or filesha256(\"...\") over the same path. The provider never computes it " +
					"itself — hashing a multi-gigabyte disk image on every plan would make every plan read the " +
					"whole file. Its value is never sent to the API; changing it replaces the image, which " +
					"re-uploads and re-imports it.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"disk_format": schema.StringAttribute{
				Description: "The disk format of source_file. Customer images must be \"qcow2\" or \"raw\".",
				Required:    true,
				Validators: []validator.String{
					stringvalidator.OneOf(customerDiskFormats...),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"container_format": schema.StringAttribute{
				Description: "The container format of source_file. Customer images must be \"bare\" (the default).",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(customerContainerFormat),
				Validators: []validator.String{
					stringvalidator.OneOf(customerContainerFormat),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplace(),
				},
			},
			"os_distro": schema.StringAttribute{
				Description: "The OS distribution of the image (e.g. \"ubuntu\", \"debian\"). Recorded as a Glance " +
					"image property at creation and cannot be changed afterwards.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"os_version": schema.StringAttribute{
				Description: "The OS version of the image (e.g. \"24.04\"). Recorded as a Glance image property " +
					"at creation and cannot be changed afterwards.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"architecture": schema.StringAttribute{
				Description: "The CPU architecture the image is built for (e.g. \"x86_64\"). Recorded as a Glance " +
					"image property at creation and cannot be changed afterwards.",
				Optional: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"min_disk_gb": schema.Int64Attribute{
				Description: "Minimum root disk size in GB an instance must have to boot this image. Can be " +
					"changed in place. The platform may RAISE this value after an import completes, to match " +
					"the image's actual expanded size — a configured value is kept in state as written, so a " +
					"later refresh can surface the platform's larger figure as a diff.",
				Optional: true,
				Computed: true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"min_ram_mb": schema.Int64Attribute{
				Description: "Minimum RAM in MB an instance must have to boot this image. Can be changed in place.",
				Optional:    true,
				Computed:    true,
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
				},
			},
			"status": schema.StringAttribute{
				Description: "The status of the image (\"active\" once the import has completed).",
				Computed:    true,
			},
			"size": schema.Int64Attribute{
				Description: "The stored size of the image in bytes.",
				Computed:    true,
			},
			"virtual_size": schema.Int64Attribute{
				Description: "The virtual disk size of the image in bytes once expanded.",
				Computed:    true,
			},
			"checksum": schema.StringAttribute{
				Description: "The MD5 checksum of the STORED image data, set by the platform once the image is " +
					"active. It is not the value to check a vendor download against: it is an MD5 while vendors " +
					"publish SHA-256, and for a qcow2 upload it describes the image AFTER the import has " +
					"converted it to raw, so it cannot equal a digest of source_file. (For a disk_format of " +
					"\"raw\" there is no conversion, so only the algorithm differs.) To verify a download, " +
					"compare filesha256 of the local path against the vendor's published value before you apply " +
					"— see the example. The provider also checks, at upload time, that the bytes the storage " +
					"edge received match the bytes it sent, and fails the apply if they do not.",
				Computed: true,
			},
			"visibility": schema.StringAttribute{
				Description: "The visibility of the image. Customer images are always \"private\" — the API " +
					"rejects anything else.",
				Computed: true,
			},
			"owner": schema.StringAttribute{
				Description: "The owning project of the image.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the image was created.",
				Computed:    true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

func (r *imageResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// Create runs the whole bring-your-own-image flow: create the image record ->
// persist its id -> check the local file against the platform's upload limit ->
// upload the bytes to object storage -> ask the platform to import them -> wait
// for the image to go active.
//
// The ordering of the first two steps is the load-bearing part. From the moment
// the create returns, a real image exists server-side and counts against the
// tenant's custom-image allowance. Every failure after that point writes the id
// to state FIRST, so `terraform destroy` can remove it. Returning an error
// without doing so would leave the practitioner owning an image Terraform has
// never heard of, which they can then neither see nor delete from their config.
func (r *imageResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sourcePath := plan.SourceFile.ValueString()

	// Captured BEFORE any read-back overwrites them — see ImageModel.keepPlanned
	// for why the server's version of these two must not win a create.
	plannedDescription, plannedMinDisk := plan.Description, plan.MinDiskGB

	// Stat BEFORE the create. A typo'd path is a configuration error, and
	// creating the image record first would charge the tenant's allowance for an
	// empty queued image nobody can fill.
	info, err := os.Stat(sourcePath)
	if err != nil {
		resp.Diagnostics.AddError(
			"Cannot read source_file",
			fmt.Sprintf("Failed to stat the disk image %q: %s", sourcePath, err),
		)
		return
	}
	if info.IsDir() {
		resp.Diagnostics.AddError(
			"Cannot read source_file",
			fmt.Sprintf("source_file %q is a directory; it must be a single disk image file.", sourcePath),
		)
		return
	}

	createResp, err := r.client.Post(ctx, "/v1/images", plan.toCreateRequest())
	if err != nil {
		detail := imageErrorDetail(err)
		// A transport-level failure — timeout, connection reset, EOF — is not an
		// *APIError, and it does NOT mean the create did not happen: the request
		// may have reached compute and created the image before the response was
		// lost. The create is genuinely expensive (an owned-image listing, a
		// staging reservation, a Glance create and a presign) against a 60s
		// client timeout, so this is a real outcome, not a theoretical one. There
		// is no idempotency key on this endpoint, so the provider cannot dedupe
		// it — the honest move is to say so rather than imply nothing happened.
		var apiErr *client.APIError
		if !errors.As(err, &apiErr) {
			detail += "\n\n" + mayExistHint
		}
		resp.Diagnostics.AddError("Failed to create image", detail)
		return
	}
	created, err := client.ParseResponse[apiCreateImageResponse](createResp)
	if err != nil {
		// The create SUCCEEDED — this is a response we could not read. Same
		// orphan as the no-id branch below, so it gets the same warning.
		resp.Diagnostics.AddError(
			"Failed to parse image create response",
			err.Error()+"\n\n"+mayExistHint,
		)
		return
	}
	if created.ID == "" {
		resp.Diagnostics.AddError(
			"Image Create Returned No ID",
			"The image was created but the response carried no id, so it cannot be tracked in Terraform state.\n\n"+
				mayExistHint,
		)
		return
	}

	// The image EXISTS from here on — persist it before anything that can fail.
	plan.fromAPI(&created.apiImage)
	plan.keepPlanned(plannedDescription, plannedMinDisk)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upload, err := r.resolveUploadForm(ctx, created)
	if err != nil {
		resp.Diagnostics.AddError("Image created but its bytes cannot be uploaded",
			imageErrorDetail(err)+"\n\n"+orphanHint(created.ID))
		return
	}

	// Check the file against the form's limit before spending the upload: the
	// alternative is discovering it after pushing gigabytes the edge then
	// rejects, and re-minting a form costs one of the tenant's few hourly mints.
	if upload.MaxBytes > 0 && info.Size() > upload.MaxBytes {
		resp.Diagnostics.AddError(
			"source_file is larger than the platform accepts",
			fmt.Sprintf("%q is %d bytes; the upload limit is %d bytes. Nothing was uploaded.\n\n"+
				"Note that a compressed qcow2 is bounded twice: this limit is on the UPLOADED bytes, and a "+
				"separate limit of %d bytes applies to the size the image expands to once converted, which "+
				"cannot be checked from the file on disk.\n\n%s",
				sourcePath, info.Size(), upload.MaxBytes, upload.MaxVirtualBytes, orphanHint(created.ID)),
		)
		return
	}

	// An expired form cannot succeed, so spending the upload on it only wastes
	// the transfer and hides the cause behind a signature error from RGW.
	if err := checkFormNotExpired(upload); err != nil {
		resp.Diagnostics.AddError("The upload form has already expired",
			err.Error()+"\n\n"+orphanHint(created.ID))
		return
	}

	if err := r.uploadImage(ctx, upload, sourcePath, info.Size()); err != nil {
		resp.Diagnostics.AddError("Failed to upload the image", err.Error()+"\n\n"+orphanHint(created.ID))
		return
	}

	if _, err := r.client.Post(ctx, "/v1/images/"+created.ID+"/import", nil); err != nil {
		resp.Diagnostics.AddError("Failed to start the image import",
			imageErrorDetail(err)+"\n\n"+orphanHint(created.ID))
		return
	}

	final, err := r.waitForImport(ctx, created.ID)
	if err != nil {
		resp.Diagnostics.AddError("Image import failed", err.Error())
		return
	}

	plan.fromAPI(final)
	plan.keepPlanned(plannedDescription, plannedMinDisk)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *imageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, "/v1/images/"+state.ID.ValueString(), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to read image", imageErrorDetail(err))
		return
	}

	img, err := client.ParseResponse[apiImage](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse image response", err.Error())
		return
	}

	state.fromAPI(img)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is metadata-only. Everything that describes the image DATA —
// source_file, its hash, the formats, the OS properties — is RequiresReplace,
// because Glance image data is immutable once imported: there is no API that
// swaps the bytes under a live image, and a tenant may already be booting
// instances from it.
func (r *imageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan ImageModel
	var state ImageModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	imageID := state.ID.ValueString()

	var updateReq apiUpdateImageRequest
	needsUpdate := false
	if !plan.Name.Equal(state.Name) {
		name := plan.Name.ValueString()
		updateReq.Name = &name
		needsUpdate = true
	}
	if !plan.Description.Equal(state.Description) {
		desc := plan.Description.ValueString()
		updateReq.Description = &desc
		needsUpdate = true
	}
	if !plan.MinDiskGB.Equal(state.MinDiskGB) {
		minDisk := plan.MinDiskGB.ValueInt64()
		updateReq.MinDisk = &minDisk
		needsUpdate = true
	}
	if !plan.MinRAMMB.Equal(state.MinRAMMB) {
		minRAM := plan.MinRAMMB.ValueInt64()
		updateReq.MinRAM = &minRAM
		needsUpdate = true
	}

	// Nothing the API owns changed — keep the state values rather than spend a
	// request that would echo them straight back.
	if !needsUpdate {
		plan.ID = state.ID
		plan.Status = state.Status
		plan.Size = state.Size
		plan.VirtualSize = state.VirtualSize
		plan.Checksum = state.Checksum
		plan.Visibility = state.Visibility
		plan.Owner = state.Owner
		plan.CreatedAt = state.CreatedAt
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
		return
	}

	apiResp, err := r.client.Put(ctx, "/v1/images/"+imageID, updateReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update image", imageErrorDetail(err))
		return
	}

	img, err := client.ParseResponse[apiImage](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to parse image response", err.Error())
		return
	}

	// Same read-back guard as Create, and for the same reason: an update that
	// sets description against a compute predating PR #582 would otherwise adopt
	// an empty echo and fail the apply outright, rather than merely not
	// persisting. Captured before fromAPI overwrites them.
	plannedDescription, plannedMinDisk := plan.Description, plan.MinDiskGB
	plan.fromAPI(img)
	plan.keepPlanned(plannedDescription, plannedMinDisk)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *imageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ImageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if _, err := r.client.Delete(ctx, "/v1/images/"+state.ID.ValueString()); err != nil {
		// Already gone — including the case a failed create left a queued image
		// the practitioner deleted by hand. The end state is what was asked for.
		if client.IsNotFound(err) {
			return
		}
		resp.Diagnostics.AddError("Failed to delete image", imageErrorDetail(err))
	}
}

// resolveUploadForm returns the presigned upload form for a freshly created
// image, minting one if the create did not carry it.
//
// The create's `upload` is absent whenever staging is unconfigured (production
// today) or the presign failed, and compute deliberately still answers 201 in
// both cases so the id reaches the caller. POST /images/{id}/upload is the
// documented recovery for exactly that. This code path issues it at most ONCE: a
// mint is charged against a per-tenant hourly budget of six, so a retry loop here
// would burn a customer's whole hour on one bad apply.
//
// (The shared client does retry underneath this call, but only a 429 — the
// gateway's rate limit, rejected before the request reaches the mint reservation,
// so those retries cannot cost a mint. The refusal that does cost one arrives as
// a 403 and is returned on the first attempt.)
//
// When staging is off it answers 503 with its own message, which is the honest
// thing to put in front of the practitioner.
func (r *imageResource) resolveUploadForm(ctx context.Context, created *apiCreateImageResponse) (*apiImageUpload, error) {
	if created.Upload != nil {
		return created.Upload, nil
	}

	mintResp, err := r.client.Post(ctx, "/v1/images/"+created.ID+"/upload", nil)
	if err != nil {
		return nil, fmt.Errorf("image %s was created but no upload form could be minted for it: %w", created.ID, err)
	}
	upload, err := client.ParseResponse[apiImageUpload](mintResp)
	if err != nil {
		return nil, fmt.Errorf("parse upload form for image %s: %w", created.ID, err)
	}
	if upload.URL == "" {
		return nil, fmt.Errorf("image %s was created but the minted upload form carried no url", created.ID)
	}
	return upload, nil
}

// uploadImage POSTs the disk image to the presigned storage endpoint as
// multipart/form-data: every signed policy field verbatim, then the image bytes
// as the trailing "file" field (the file MUST be last for an S3 POST policy).
// No Frostmoln auth header rides along — the signature in the form IS the
// authorization, and the endpoint is object storage, not the API.
//
// This is the streaming counterpart of webserver_deployment's uploadArchive and
// keeps its shape deliberately. The one difference is why it is not simply that
// function: a site archive is megabytes and can be assembled in a bytes.Buffer,
// a disk image is tens of gigabytes and cannot. The multipart preamble and
// trailer are still produced by mime/multipart (never hand-written boundaries);
// only the file bytes are spliced in between, which also yields an exact
// Content-Length — RGW rejects a chunked POST-object form.
func (r *imageResource) uploadImage(ctx context.Context, upload *apiImageUpload, sourcePath string, size int64) error {
	f, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open source_file: %w", err)
	}
	defer func() { _ = f.Close() }()

	var frame bytes.Buffer
	w := multipart.NewWriter(&frame)
	for k, v := range upload.Fields {
		if err := w.WriteField(k, v); err != nil {
			return fmt.Errorf("write upload field %q: %w", k, err)
		}
	}
	if _, err := w.CreateFormFile("file", filepath.Base(sourcePath)); err != nil {
		return fmt.Errorf("create file part: %w", err)
	}
	// Everything written so far is the preamble; Close appends the closing
	// boundary, so the bytes it adds are the trailer. Splitting the buffer at
	// that offset gives both halves without a hand-rolled boundary string.
	headLen := frame.Len()
	if err := w.Close(); err != nil {
		return fmt.Errorf("finalize multipart body: %w", err)
	}
	head, tail := frame.Bytes()[:headLen], frame.Bytes()[headLen:]

	// Hash the image bytes in the pass that uploads them, so the storage edge's
	// ETag can be checked against what we actually sent. A tens-of-gigabytes file
	// must not be read twice, and MD5 is not a security primitive here — it is
	// simply the only digest an S3/RGW ETag can be compared with.
	sent := md5.New() // #nosec G401
	body := io.MultiReader(bytes.NewReader(head), io.TeeReader(f, sent), bytes.NewReader(tail))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, upload.URL, body)
	if err != nil {
		return fmt.Errorf("build upload request: %w", err)
	}
	httpReq.Header.Set("Content-Type", w.FormDataContentType())
	httpReq.ContentLength = int64(len(head)) + size + int64(len(tail))

	httpResp, err := r.getUploadClient().Do(httpReq)
	if err != nil {
		return fmt.Errorf("upload image: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(httpResp.Body, 4096))
		return fmt.Errorf("image upload rejected: HTTP %d: %s", httpResp.StatusCode, string(msg))
	}

	// Refuse to go on to the import when the edge reports a checksum that is not
	// the one we sent: the staged object is not source_file, and converting
	// corrupt bytes yields an image that boots wrong rather than one that
	// visibly failed. `fm compute image create` refuses on the same evidence, and
	// the two clients must not disagree about the same upload.
	//
	// Only an ETag that IS a plain MD5 is comparable — server-side encryption and
	// multipart both return something else, and treating those as "different"
	// would refuse every healthy upload.
	etag := strings.Trim(httpResp.Header.Get("ETag"), `"`)
	if isMD5ETag(etag) && !strings.EqualFold(etag, hex.EncodeToString(sent.Sum(nil))) {
		return fmt.Errorf("image upload arrived corrupted: sent MD5 %s, storage edge stored %s "+
			"(the image record exists and holds staging quota until it is destroyed)",
			hex.EncodeToString(sent.Sum(nil)), etag)
	}
	return nil
}

// isMD5ETag reports whether an ETag is a plain MD5 and can therefore be compared
// with one. Deliberately an allow-list: an unrecognised shape means UNCHECKED,
// never corrupt.
func isMD5ETag(etag string) bool {
	if len(etag) != 2*md5.Size {
		return false
	}
	_, err := hex.DecodeString(etag)
	return err == nil
}

// waitForImport polls the image until Glance has finished importing it,
// returning the final image.
//
// The termination conditions are `active`, `killed`, importFailed, or the
// timeout — importFailed being the one that is easy to miss. A failed
// interoperable import does not move the image to a failed status; it reverts it
// to `queued`, so a loop keyed on `active` (and even one that also watches
// `killed`) polls a permanently-failed image until it times out.
func (r *imageResource) waitForImport(ctx context.Context, imageID string) (*apiImage, error) {
	var last *apiImage
	gone := false
	_, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     r.getPollInterval(),
		Timeout:      r.getPollTimeout(),
		TargetStates: []string{imageStatusActive},
		ErrorStates: []string{
			imageStatusKilled, imageStatusDeleted, imageStatusPendingDelete, imageStatusDeactivated,
			importStateFailed, importStateGone,
		},
		ResourceName: "image " + imageID,
		PollFunc: func(pollCtx context.Context) (string, error) {
			pollResp, pollErr := r.client.Get(pollCtx, "/v1/images/"+imageID, nil)
			if pollErr != nil {
				// An unambiguous 404 is terminal, not transient: the image was
				// deleted out of band and no amount of further polling brings it
				// back. It must be reported as a STATE, because WaitForState
				// retries every PollFunc error until the deadline — returning it
				// as an error would hold the apply open for the full timeout.
				// Every other error (a 500, a dropped connection) stays a
				// transient error and is retried, which is what that retry is for.
				if client.IsNotFound(pollErr) {
					gone = true
					return importStateGone, nil
				}
				return "", pollErr
			}
			img, parseErr := client.ParseResponse[apiImage](pollResp)
			if parseErr != nil {
				return "", parseErr
			}
			last = img
			if img.ImportFailed {
				return importStateFailed, nil
			}
			return img.Status, nil
		},
	})
	if err == nil {
		return last, nil
	}

	switch {
	case gone:
		// No orphan to warn about — the image is the thing that vanished.
		return nil, fmt.Errorf("image %s disappeared while it was importing: the platform reports it no "+
			"longer exists. It was most likely deleted outside Terraform; re-apply to create it again", imageID)
	case last != nil && last.ImportFailed:
		detail := "the platform could not import the uploaded data"
		if last.ImportFailedStores != "" {
			detail += " (stores: " + last.ImportFailedStores + ")"
		}
		return nil, fmt.Errorf("image %s: %s. A rejected disk image is the usual cause — check that "+
			"disk_format matches the file.\n\n%s", imageID, detail, orphanHint(imageID))
	case last != nil && (last.Status == imageStatusDeleted || last.Status == imageStatusPendingDelete):
		return nil, fmt.Errorf("image %s was deleted while it was importing (status %q); re-apply to "+
			"create it again", imageID, last.Status)
	default:
		// A timeout, or a terminal status the image is still sitting in
		// (`killed`, `deactivated`) — the record exists either way.
		return nil, fmt.Errorf("%w\n\n%s", err, orphanHint(imageID))
	}
}

// orphanHint is the closing sentence on every Create failure that happens AFTER
// the image record exists. Consistency matters more than brevity here: the
// practitioner's next action is the same in all of these cases, and a failure
// that omits it reads as though nothing was created.
func orphanHint(imageID string) string {
	return fmt.Sprintf("The image %s exists and holds part of this tenant's custom-image allowance. "+
		"Terraform tracks it, so `terraform destroy` removes it.", imageID)
}

// mayExistHint is the counterpart for the failures where the provider cannot
// tell whether the image was created — a lost response, an unreadable body, or a
// response with no id. The endpoint takes no idempotency key, so there is
// nothing to dedupe against and nothing to put in state; saying so is all the
// provider honestly can do.
const mayExistHint = "The image MAY have been created. Check `fm compute image list` and delete the orphan if one exists."

// checkFormNotExpired refuses an upload form whose expiry has already passed.
// An unparseable or absent expiry is deliberately NOT an error: the field is
// advisory, and refusing an upload the edge would have accepted because the
// provider could not read a timestamp would be the worse failure.
func checkFormNotExpired(upload *apiImageUpload) error {
	expiresAt, err := time.Parse(time.RFC3339, upload.ExpiresAt)
	if err != nil || expiresAt.IsZero() {
		return nil
	}
	if time.Now().After(expiresAt) {
		return fmt.Errorf("the presigned upload form expired at %s; nothing was uploaded. Apply again to "+
			"mint a fresh one (each mint is charged against this tenant's hourly upload budget)",
			expiresAt.Format(time.RFC3339))
	}
	return nil
}

// imageErrorDetail renders an API error for a diagnostic, appending the one
// thing the server's own message cannot say: which of the two quota_exceeded
// refusals this is. They share a code and differ only by status, and they are
// bounded by different things — one by uploads in flight, the other by images
// owned — so a diagnostic that flattens them names the wrong thing to remove.
//
// NEITHER arm may promise that waiting clears it. The 403's first check counts
// the tenant's `queued` images, and a FAILED Glance import leaves the image in
// `queued`, so abandoned attempts hold their slot until they are deleted. An
// earlier revision of this text said the 403 "clears as uploads finish or are
// abandoned", which is exactly backwards for that arm and sent practitioners
// into an apply loop that could never succeed.
//
// The 403's OTHER two arms (staged bytes, staged objects) genuinely do drain —
// compute reclaims a staged object once its import reaches a terminal state —
// which is why the copy says "does NOT necessarily" rather than "does NOT".
// Nothing on the wire distinguishes the three, so the hedge is the honest form.
func imageErrorDetail(err error) string {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != errCodeQuotaExceeded {
		return err.Error()
	}
	switch apiErr.StatusCode {
	case http.StatusForbidden:
		return err.Error() + "\n\nThis is the staging limit on uploads in flight, not this tenant's image " +
			"allowance. It does NOT necessarily clear on its own: a failed import leaves the image queued and " +
			"still holding a slot, so finish or delete the images in progress before applying again."
	case http.StatusConflict:
		return err.Error() + "\n\nThis is this tenant's custom-image allowance, and it is full. It does NOT " +
			"clear on its own: delete an existing custom image (or ask for a higher limit) before applying again. " +
			"The allowance bounds both the number of images and their total size, so freeing one slot may not be " +
			"enough. An image the storage backend still holds clones of — in practice, one with instances built " +
			"from it — refuses the delete with 409 resource_in_use; destroy those first."
	}
	return err.Error()
}

// errCodeQuotaExceeded is servicekit's wire code for both image quota refusals.
const errCodeQuotaExceeded = "quota_exceeded"
