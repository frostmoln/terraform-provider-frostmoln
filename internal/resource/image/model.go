// Package image implements the frostmoln_image resource: a customer custom
// image (BYOI) whose bytes Terraform uploads from the local filesystem.
//
// It shares the frostmoln_image type name with the image DATA SOURCE
// (internal/datasource/image). That is deliberate and legal — resources and data
// sources live in separate Terraform namespaces — and it is the right pairing:
// the data source looks up the platform catalogue an instance boots from, this
// resource declares an image the tenant owns.
package image

import (
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// ImageModel is the Terraform state model for a customer custom image.
//
// source_file / source_file_hash are write-only from the API's perspective: the
// image bytes go straight to object storage and compute never echoes the local
// path back, so they are only ever preserved from plan/state, never populated
// from a read (mirrors the webserver_deployment source_archive pattern).
//
// The create-only metadata (disk_format, container_format, os_distro,
// os_version, architecture) is likewise preserved rather than refreshed — see
// fromAPI for why.
type ImageModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	SourceFile      types.String `tfsdk:"source_file"`
	SourceFileHash  types.String `tfsdk:"source_file_hash"`
	DiskFormat      types.String `tfsdk:"disk_format"`
	ContainerFormat types.String `tfsdk:"container_format"`
	OSDistro        types.String `tfsdk:"os_distro"`
	OSVersion       types.String `tfsdk:"os_version"`
	Architecture    types.String `tfsdk:"architecture"`
	DefaultUser     types.String `tfsdk:"default_user"`
	MinDiskGB       types.Int64  `tfsdk:"min_disk_gb"`
	MinRAMMB        types.Int64  `tfsdk:"min_ram_mb"`
	Status          types.String `tfsdk:"status"`
	Size            types.Int64  `tfsdk:"size"`
	VirtualSize     types.Int64  `tfsdk:"virtual_size"`
	Checksum        types.String `tfsdk:"checksum"`
	Visibility      types.String `tfsdk:"visibility"`
	Owner           types.String `tfsdk:"owner"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

// Image status values (mirror compute domain.ImageStatus). These are the
// TERMINAL ones — the same set compute's own isTerminalImport treats as "stop
// waiting" (internal/service/impl/imageimport.go). The rest (`saving`,
// `uploading`, `importing`, …) are intermediate and keep the wait loop polling.
//
// Only `active` is a success. The other four all mean the image will never
// become bootable, and three of them (`deleted`, `pending_delete`,
// `deactivated`) are reachable by an operator or another client acting on the
// image mid-import — so a wait that watched only `active` and `killed` would sit
// out its whole timeout on any of them.
const (
	imageStatusActive        = "active"
	imageStatusKilled        = "killed"
	imageStatusDeleted       = "deleted"
	imageStatusPendingDelete = "pending_delete"
	imageStatusDeactivated   = "deactivated"
)

// importStateFailed is a SYNTHETIC poll state, never a value compute can
// return. A failed Glance interoperable import does NOT leave the image in a
// failed status — it reverts it to `queued`, which is also exactly what an image
// nobody has uploaded to looks like. The boolean `importFailed` is the only
// signal that the import was attempted and lost, so the wait loop maps it onto
// this state and lists it as an error state; keyed on `active` alone the loop
// would sit out the full timeout on every failure instead of failing in seconds.
const importStateFailed = "import_failed"

// importStateGone is the second SYNTHETIC poll state: the image 404s mid-import
// because it was deleted out of band. It has to be a state rather than an error
// return, because client.WaitForState treats EVERY PollFunc error as transient
// and keeps polling (poller.go) — so a 404 returned as an error would hold the
// apply open for the full timeout and then report a generic deadline, instead of
// the one thing that actually happened.
const importStateGone = "gone"

// customerDiskFormats are the only disk formats compute accepts on a customer
// image (its customerDiskFormats allowlist). Anything else is a 400, so the
// provider refuses it at plan time where the practitioner can still fix it.
var customerDiskFormats = []string{"qcow2", "raw"}

// customerContainerFormat is the only container format compute accepts. Both
// formats must be declared truthfully on the queued image: Glance 409s an import
// against an image missing either, and its conversion plugin hard-fails when the
// detected format disagrees with the declared one.
const customerContainerFormat = "bare"

// customerArchitectures are the only architectures compute accepts on an image.
// It validates them because Nova's legacy property map turns `architecture` into
// `hw_architecture`, which ImagePropertiesFilter matches against the hypervisor —
// so a value no host reports is not merely wrong, it schedules NOWHERE, and the
// failure surfaces at INSTANCE create with nothing pointing back at the image.
//
// Declaring it here, rather than leaving the 400 to the server, is what keeps a
// bad value from costing the image itself: `architecture` is RequiresReplace, so
// without a plan-time validator Terraform DESTROYS the existing image, then fails
// the recreate on the server's 400 — leaving the apply dead, the image gone and
// every instance that referenced it broken. Same reasoning as customerDiskFormats
// above; the difference is only that this one became a closed set later.
// It holds x86_64 ALONE, matching compute: prod-fbg has no ARM host class, so an
// image declaring aarch64 would be accepted and then unlaunchable for ever —
// the exact failure this validator exists to prevent. Widen it in lockstep with
// compute's imageArchitectures the day an ARM host exists.
var customerArchitectures = []string{"x86_64"}

// apiImage mirrors the subset of compute domain.Image this resource tracks.
type apiImage struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Status          string `json:"status"`
	Visibility      string `json:"visibility"`
	DiskFormat      string `json:"diskFormat"`
	ContainerFormat string `json:"containerFormat"`
	Size            int64  `json:"size"`
	VirtualSize     int64  `json:"virtualSize"`
	MinDisk         int64  `json:"minDisk"`
	MinRAM          int64  `json:"minRam"`
	Checksum        string `json:"checksum,omitempty"`
	OSDistro        string `json:"osDistro,omitempty"`
	OSVersion       string `json:"osVersion,omitempty"`
	Architecture    string `json:"architecture,omitempty"`
	// Metadata carries the image's RAW Glance string properties. It is how
	// default_user is refreshed, and the reason the response's own `defaultUser`
	// field is deliberately NOT declared here: that one is DERIVED by compute
	// (explicit property, else os_admin_user, else its os_distro map), so an
	// image whose user was never set still reads back e.g. "debian". Refreshing
	// an Optional attribute from it would write a value the practitioner never
	// configured into state — a permanent diff, or "provider produced
	// inconsistent result after apply" on the create that adopted it.
	Metadata map[string]string `json:"metadata,omitempty"`
	// ImportFailed is the ONLY way to tell a failed import from an image that
	// was never uploaded: both sit at status `queued`. See importStateFailed.
	ImportFailed bool `json:"importFailed,omitempty"`
	// ImportFailedStores names the stores the import could not write, verbatim
	// from Glance — the one piece of detail a failure diagnostic can offer.
	ImportFailedStores string `json:"importFailedStores,omitempty"`
	// ImportFailureReason is WHY the import failed, as a stable code compute
	// defines. It is the only part of a failure a practitioner can act on: the
	// stores above name what the platform could not write, which says nothing
	// about the file. Empty when compute could not read the reason.
	ImportFailureReason string `json:"importFailureReason,omitempty"`
	Owner               string `json:"owner"`
	CreatedAt           string `json:"createdAt"`
}

// apiCreateImageRequest mirrors compute domain.CreateImageRequest, narrowed to
// what a customer image may set. `visibility` is deliberately never sent:
// compute rejects anything but "private" and forces that value itself, so
// sending it would only add a way to get a 400.
type apiCreateImageRequest struct {
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	DiskFormat      string `json:"diskFormat"`
	ContainerFormat string `json:"containerFormat"`
	MinDisk         int64  `json:"minDisk,omitempty"`
	MinRAM          int64  `json:"minRam,omitempty"`
	OSDistro        string `json:"osDistro,omitempty"`
	OSVersion       string `json:"osVersion,omitempty"`
	Architecture    string `json:"architecture,omitempty"`
	DefaultUser     string `json:"defaultUser,omitempty"`
}

// apiImageUpload mirrors compute domain.ImageUpload: a presigned POST form, not
// a URL to PUT to (only a POST policy's content-length-range binds the upload
// size at the RGW edge). Every field is sent verbatim as multipart/form-data
// with the image as the trailing "file" part; the bytes go straight to object
// storage and never transit compute or the gateway (ADR-0024/0030).
type apiImageUpload struct {
	URL       string            `json:"url"`
	Fields    map[string]string `json:"fields"`
	ExpiresAt string            `json:"expiresAt"`
	// MaxBytes bounds the UPLOADED bytes — checkable locally before spending
	// the upload.
	MaxBytes int64 `json:"maxBytes"`
	// MaxVirtualBytes bounds the POST-CONVERSION size, which a compressed qcow2
	// declares for itself. It cannot be checked from the file's size on disk, so
	// it is only ever quoted in an error message.
	MaxVirtualBytes int64 `json:"maxVirtualBytes"`
}

// apiCreateImageResponse mirrors compute domain.CreateImageResponse: the image
// at the top level, plus the upload form when direct upload is available.
//
// Upload is ABSENT when image staging is unconfigured (production today) or the
// presign failed — and in BOTH cases the image has still been created. Anything
// that treats a missing form as "the create failed" orphans a real,
// allowance-consuming server object.
type apiCreateImageResponse struct {
	apiImage
	Upload *apiImageUpload `json:"upload,omitempty"`
}

// apiUpdateImageRequest mirrors compute domain.UpdateImageRequest, narrowed to
// the five fields this resource can change in place. Pointers so an unchanged
// field is omitted rather than sent as a zero value that would clear it.
//
// default_user is one of them: compute updates the property in place
// (domain.UpdateImageRequest.DefaultUser), so it must NOT be modelled as
// create-only alongside os_distro — forcing a whole image replacement for a
// one-word metadata fix is the opposite of the self-service this attribute is
// for. An empty string IS a meaningful value here: it clears the property.
type apiUpdateImageRequest struct {
	Name        *string `json:"name,omitempty"`
	Description *string `json:"description,omitempty"`
	MinDisk     *int64  `json:"minDisk,omitempty"`
	MinRAM      *int64  `json:"minRam,omitempty"`
	DefaultUser *string `json:"defaultUser,omitempty"`
}

// toCreateRequest builds the create body from the plan.
func (m *ImageModel) toCreateRequest() apiCreateImageRequest {
	return apiCreateImageRequest{
		Name:            m.Name.ValueString(),
		Description:     m.Description.ValueString(),
		DiskFormat:      m.DiskFormat.ValueString(),
		ContainerFormat: m.ContainerFormat.ValueString(),
		MinDisk:         m.MinDiskGB.ValueInt64(),
		MinRAM:          m.MinRAMMB.ValueInt64(),
		OSDistro:        m.OSDistro.ValueString(),
		OSVersion:       m.OSVersion.ValueString(),
		Architecture:    m.Architecture.ValueString(),
		DefaultUser:     m.DefaultUser.ValueString(),
	}
}

// fromAPI refreshes the server-owned attributes plus the three the API can
// change in place (name, min_disk_gb, min_ram_mb) and description.
//
// It deliberately never touches source_file, source_file_hash, disk_format,
// container_format, os_distro, os_version or architecture. Those are write-only
// or create-only: the API either never returns them or returns them only as the
// properties Glance happened to persist, and overwriting a configured value with
// a slightly different read-back is how a create earns Terraform's "provider
// produced inconsistent result after apply". They are all RequiresReplace, so a
// real change is a new image anyway — there is nothing a refresh could usefully
// tell the practitioner that a replacement would not.
func (m *ImageModel) fromAPI(img *apiImage) {
	m.ID = types.StringValue(img.ID)
	m.Name = types.StringValue(img.Name)
	m.Status = types.StringValue(img.Status)
	m.Size = types.Int64Value(img.Size)
	m.VirtualSize = types.Int64Value(img.VirtualSize)
	m.MinDiskGB = types.Int64Value(img.MinDisk)
	m.MinRAMMB = types.Int64Value(img.MinRAM)
	m.Visibility = types.StringValue(img.Visibility)
	m.Owner = types.StringValue(img.Owner)
	m.CreatedAt = types.StringValue(img.CreatedAt)

	// description is Optional and NOT Computed: an omitted description must stay
	// null, or the value Terraform applies stops matching the configuration.
	if img.Description != "" {
		m.Description = types.StringValue(img.Description)
	} else {
		m.Description = types.StringNull()
	}
	// checksum is Computed and set UNCONDITIONALLY, empty string included. Glance
	// only fills it once the image is active, but a null Computed attribute is
	// re-marked unknown on the next plan (MarkComputedNilsAsUnknown) and
	// UseStateForUnknown bails on a null state value — so leaving it null would
	// render `checksum = (known after apply)` on every subsequent plan and call
	// Update for a value nothing had changed.
	m.Checksum = types.StringValue(img.Checksum)

	// default_user is refreshed from the RAW Glance property, never from the
	// response's derived `defaultUser` — see apiImage.Metadata. Like description
	// it is Optional and NOT Computed, so an unset one must stay null.
	if raw := img.Metadata["default_user"]; raw != "" {
		m.DefaultUser = types.StringValue(raw)
	} else {
		m.DefaultUser = types.StringNull()
	}
}

// keepPlanned re-asserts the plan's own values for the two attributes a create
// must NOT adopt from the server's read-back.
//
//   - description: compute persists it as a Glance image property and reads it
//     back only since compute PR #582. Against an older compute it round-trips
//     as empty, and adopting that would make the value Terraform applies differ
//     from the configuration — a hard "provider produced inconsistent result
//     after apply", re-raised on every retry because Update could not persist it
//     either. A provider that shows a diff against an old server is a nuisance;
//     one that hard-errors is unusable, so the plan value wins.
//   - min_disk_gb: compute's reclaim watcher calls reassertMinDisk and raises
//     min_disk to match the stored size once the image goes active, in a
//     goroutine that races this resource's poll. Polling before the reassert
//     lands gives a diff on the next plan; polling after gives the same hard
//     inconsistent-result error — a coin flip, i.e. a flaky apply. Only a value
//     the practitioner actually set is restored: when the attribute was omitted
//     the plan value is unknown and the server's number is the right answer.
func (m *ImageModel) keepPlanned(description types.String, minDiskGB types.Int64) {
	if !description.IsUnknown() {
		m.Description = description
	}
	if !minDiskGB.IsUnknown() && !minDiskGB.IsNull() {
		m.MinDiskGB = minDiskGB
	}
}

// Import failure reason codes, as compute defines them, with the copy the
// practitioner sees. An UNRECOGNISED code renders as nothing rather than raw: a
// code from a compute newer than this provider is not text to put in a
// diagnostic, and the generic line is a correct fallback.
const (
	importFailureNestedFormat       = "nestedFormat"
	importFailureUnsupportedFeature = "unsupportedFeature"
	importFailureDeclaredFormat     = "declaredFormat"
	importFailureConversionFailed   = "conversionFailed"
	importFailureUnknown            = "unknown"
)

var importFailureReasonText = map[string]string{
	importFailureNestedFormat: "the file is a disk image whose contents are themselves a disk image " +
		"(a qcow2 exported from inside another qcow2). Re-applying will fail the same way — export the " +
		"guest disk from the hypervisor as a plain qcow2 or raw image and point `source_file` at that",
	importFailureUnsupportedFeature: "the file uses a feature the platform cannot convert, such as a " +
		"backing file or encryption. Flatten it into a single unencrypted qcow2 or raw image " +
		"(qemu-img convert) and point `source_file` at that",
	importFailureDeclaredFormat: "the file is not the disk format `disk_format` declares. Check what " +
		"it really is (qemu-img info will tell you) and set `disk_format` to match",
	importFailureConversionFailed: "the file could not be converted to the platform's storage format, " +
		"which usually means the upload was incomplete or the image is corrupt. Check the file opens " +
		"locally, then re-apply",
	importFailureUnknown: "the platform could not determine why the import failed. Contact support and " +
		"quote the image ID — re-applying with the same file is unlikely to help",
}

// importFailureReason renders an import failure reason code, or "" when there is
// no code or the code is not one this provider build knows.
func importFailureReason(code string) string {
	return importFailureReasonText[code]
}
