// Package volume implements the frostmoln_volume Terraform data source: the
// name→id resolver for a block storage volume — the lookup a configuration
// needs to reach a volume that Terraform did not create (a portal or
// `fm`-provisioned volume can today be referenced only by a hardcoded UUID or
// plumbed through terraform_remote_state).
//
// The shape is the house resolver shape (`frostmoln_postgres_instance`): `id`
// or `name`, exactly one; an id goes straight to the volume read, a name walks
// the volume list. A name resolver must do the match client-side even though
// the volume list HAS a name filter (VolumeListOptions carries `name` as an
// exact match, pushed down to Cinder before pagination): the filter is trusted
// to narrow, never to resolve — the request carries the name and Read still
// matches rows client-side, so an unexpected or unfiltered answer can never
// become a resolved volume.
//
// Ambiguity and absence both fail the read. A resolver that silently picked
// the first of several same-named volumes would feed the wrong disk's id into
// a configuration — exactly the mistake this data source exists to prevent —
// so more than one match is an error that names the colliding ids, and zero
// matches is an error, never an empty row.
//
// The `attachments` array shows what consumes the volume: which instance it
// is attached to and on which device. The attachments are rendered verbatim —
// a check block pinning "no consumer" or "attached to this instance" reads the
// same rows the portal's volume panel shows.
package volume

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/reservedmeta"
)

var _ datasource.DataSource = &volumeDataSource{}

// NewDataSource returns a new frostmoln_volume data source factory.
func NewDataSource() datasource.DataSource {
	return &volumeDataSource{}
}

type volumeDataSource struct {
	client *client.Client
}

// volumeModel is the Terraform state model. Attribute names mirror the wire
// (storage/internal/domain/volume.go), so a data source read can be fed
// straight into a reference without translating names.
type volumeModel struct {
	ID               types.String `tfsdk:"id"`
	Name             types.String `tfsdk:"name"`
	Description      types.String `tfsdk:"description"`
	Size             types.Int64  `tfsdk:"size"`
	Status           types.String `tfsdk:"status"`
	VolumeType       types.String `tfsdk:"volume_type"`
	AvailabilityZone types.String `tfsdk:"availability_zone"`
	Bootable         types.Bool   `tfsdk:"bootable"`
	Encrypted        types.Bool   `tfsdk:"encrypted"`
	Attachments      types.List   `tfsdk:"attachments"`
	SourceVolumeID   types.String `tfsdk:"source_volume_id"`
	SourceSnapshotID types.String `tfsdk:"source_snapshot_id"`
	SourceImageID    types.String `tfsdk:"source_image_id"`
	IOPS             types.Int64  `tfsdk:"iops"`
	Throughput       types.Int64  `tfsdk:"throughput"`
	Metadata         types.Map    `tfsdk:"metadata"`
	CreatedAt        types.String `tfsdk:"created_at"`
	UpdatedAt        types.String `tfsdk:"updated_at"`
	TenantID         types.String `tfsdk:"tenant_id"`
}

// volumeAttachmentRowModel is one row of the attachments list. Every field the
// wire carries is always present on an attachment row (none of the
// VolumeAttachment fields is omitempty), so rows render with values, never
// nulls.
type volumeAttachmentRowModel struct {
	ID         types.String `tfsdk:"id"`
	VolumeID   types.String `tfsdk:"volume_id"`
	InstanceID types.String `tfsdk:"instance_id"`
	Device     types.String `tfsdk:"device"`
	AttachedAt types.String `tfsdk:"attached_at"`
}

// apiVolume is the API representation of one block volume — the same wire
// shape storage serializes (see storage/internal/domain/volume.go). Fields
// that are omitempty on the wire decode into optionals so an absent field can
// render as null, never as "" or 0; iops/throughput decode as POINTERS (the
// service drops a zero via omitempty, so an absent key and a zero are the same
// verdict: no provisioned performance floor).
type apiVolume struct {
	ID               string                `json:"id"`
	Name             string                `json:"name"`
	Description      string                `json:"description,omitempty"`
	TenantID         string                `json:"tenantId"`
	Size             int64                 `json:"size"`
	Status           string                `json:"status"`
	VolumeType       string                `json:"volumeType"`
	AvailabilityZone string                `json:"availabilityZone,omitempty"`
	Bootable         bool                  `json:"bootable"`
	Encrypted        bool                  `json:"encrypted"`
	Attachments      []apiVolumeAttachment `json:"attachments,omitempty"`
	SourceVolumeID   string                `json:"sourceVolumeId,omitempty"`
	SourceSnapshotID string                `json:"sourceSnapshotId,omitempty"`
	SourceImageID    string                `json:"sourceImageId,omitempty"`
	IOPS             *int                  `json:"iops,omitempty"`
	Throughput       *int                  `json:"throughput,omitempty"`
	Metadata         map[string]string     `json:"metadata,omitempty"`
	CreatedAt        string                `json:"createdAt"`
	UpdatedAt        string                `json:"updatedAt"`
}

type apiVolumeAttachment struct {
	ID         string `json:"id"`
	VolumeID   string `json:"volumeId"`
	InstanceID string `json:"instanceId"`
	Device     string `json:"device"`
	AttachedAt string `json:"attachedAt"`
}

// apiVolumeList is the wire shape of the volume list — the same paginated
// envelope the service answers, read here with the exact-match name filter.
type apiVolumeList struct {
	Volumes []apiVolume `json:"volumes"`
}

func (d *volumeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_volume"
}

func (d *volumeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a block storage volume by ID or name. Exactly one of `id` or " +
			"`name` must be specified.\n\n" +

			"This is how a configuration reaches a volume that Terraform did not create: a " +
			"volume provisioned in the portal or with `fm` can today be referenced only by a " +
			"hardcoded UUID or plumbed through `terraform_remote_state` — both of which tie the " +
			"configuration to one deployment. Look it up by name instead and the reference " +
			"survives re-provisioning elsewhere.\n\n" +

			"A name lookup that matches NOTHING fails the read, and so does one that matches " +
			"MORE THAN ONE volume: the data source refuses to pick for you (the diagnostic " +
			"names the colliding ids). The list request carries the name as the service's " +
			"exact-match filter, but the match is re-verified client-side over the answer — " +
			"a filter narrows the search, it never earns the resolution.\n\n" +

			"`attachments` shows what consumes the volume: one row per attachment, carrying " +
			"the consuming instance's id and the device path it occupies — the same rows the " +
			"portal's volume panel shows. A volume nothing consumes renders an empty list, " +
			"never null.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the volume. Exactly one of id or name " +
					"must be specified.",
				Optional: true,
				Validators: []validator.String{
					validVolumeIDValidator{},
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the volume. Exactly one of id or name must be " +
					"specified.",
				Optional: true,
			},
			"description": schema.StringAttribute{
				Description: "The description of the volume, null when the volume was created " +
					"without one.",
				Computed: true,
			},
			"size": schema.Int64Attribute{
				Description: "The size of the volume in GiB.",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the volume (creating, available, in-use, " +
					"attaching, detaching, deleting, error, error_deleting, resizing, " +
					"migrating).",
				Computed: true,
			},
			"volume_type": schema.StringAttribute{
				Description: "The type of storage backing this volume (ssd, hdd, nvme, or " +
					"default).",
				Computed: true,
			},
			"availability_zone": schema.StringAttribute{
				Description: "The availability zone where the volume is located, null when the " +
					"platform has not recorded one.",
				Computed: true,
			},
			"bootable": schema.BoolAttribute{
				Description: "Whether the volume can be used as a boot volume.",
				Computed:    true,
			},
			"encrypted": schema.BoolAttribute{
				Description: "Whether the volume is encrypted.",
				Computed:    true,
			},
			"attachments": schema.ListNestedAttribute{
				Description: "What consumes the volume: one row per attachment, with the " +
					"consuming instance and the device path it occupies. A volume nothing " +
					"consumes renders as an empty list, never null.",
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "The unique identifier of the attachment.",
							Computed:    true,
						},
						"volume_id": schema.StringAttribute{
							Description: "The ID of the attached volume — the same volume this " +
								"data source resolved.",
							Computed: true,
						},
						"instance_id": schema.StringAttribute{
							Description: "The ID of the instance the volume is attached to — " +
								"the consumer.",
							Computed: true,
						},
						"device": schema.StringAttribute{
							Description: "The device path on the instance (e.g. /dev/vdb).",
							Computed:    true,
						},
						"attached_at": schema.StringAttribute{
							Description: "The timestamp when the volume was attached.",
							Computed:    true,
						},
					},
				},
			},
			"source_volume_id": schema.StringAttribute{
				Description: "The ID of the source volume when this volume was cloned from one, " +
					"null otherwise.",
				Computed: true,
			},
			"source_snapshot_id": schema.StringAttribute{
				Description: "The ID of the snapshot this volume was created from, null when it " +
					"was not.",
				Computed: true,
			},
			"source_image_id": schema.StringAttribute{
				Description: "The ID of the image this volume was created from, null when it was " +
					"not.",
				Computed: true,
			},
			"iops": schema.Int64Attribute{
				Description: "The provisioned IOPS, null when the volume has no provisioned " +
					"IOPS floor.",
				Computed: true,
			},
			"throughput": schema.Int64Attribute{
				Description: "The provisioned throughput in MiB/s, null when the volume has no " +
					"provisioned throughput floor.",
				Computed: true,
			},
			"metadata": schema.MapAttribute{
				Description: "The customer key-value metadata stored on the volume — the " +
					"platform's reserved keys (the frostmoln_ namespace) are filtered out, " +
					"matching the resource's read-back, so `metadata` means the same thing on " +
					"both surfaces. Null when the volume carries none.",
				Computed:    true,
				ElementType: types.StringType,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the volume was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the volume was last updated.",
				Computed:    true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this volume.",
				Computed:    true,
			},
		},
	}
}

func (d *volumeDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *client.Client, got: %T", req.ProviderData),
		)
		return
	}
	d.client = c
}

func (d *volumeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg volumeModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	idSet := !cfg.ID.IsNull() && !cfg.ID.IsUnknown()
	nameSet := !cfg.Name.IsNull() && !cfg.Name.IsUnknown()

	if !idSet && !nameSet {
		resp.Diagnostics.AddError(
			"One of id or name must be specified",
			"Neither `id` nor `name` carries a value, so there is nothing to look up. "+
				"Specify exactly one of them.",
		)
		return
	}
	if idSet && nameSet {
		resp.Diagnostics.AddError(
			"Only one of id or name may be specified",
			fmt.Sprintf("Both `id` (%q) and `name` (%q) carry a value. Specify exactly one of "+
				"them.", cfg.ID.ValueString(), cfg.Name.ValueString()),
		)
		return
	}

	var vol *apiVolume
	if idSet {
		found, diags := d.readByID(ctx, cfg.ID.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		vol = found
	} else {
		found, diags := d.resolveByName(ctx, cfg.Name.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		vol = found
	}

	setInstanceState(ctx, &cfg, vol, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// readByID walks the id path: one volume read, then the identity guard. A 200
// that does not carry the requested id is not the volume read this provider
// builds its contract on.
func (d *volumeDataSource) readByID(ctx context.Context, id string) (*apiVolume, diag.Diagnostics) {
	var diags diag.Diagnostics

	pathStr, pathErr := volumePath(d.client, id)
	if pathErr != nil {
		diags.AddError("Invalid Volume ID", pathErr.Error())
		return nil, diags
	}

	apiResp, err := d.client.Get(ctx, pathStr, nil)
	if err != nil {
		if client.IsNotFound(err) {
			diags.AddError(
				"The volume does not exist",
				fmt.Sprintf("The id %q answers 404 — there is no such block volume (or none "+
					"this caller can see), so there is nothing to resolve. Correct `id`, "+
					"or look the volume up by `name` instead.\n\n%s", id, err.Error()),
			)
			return nil, diags
		}
		diags.AddError("Failed to Read Volume", err.Error())
		return nil, diags
	}

	var vol apiVolume
	if err := json.Unmarshal(apiResp.Body, &vol); err != nil {
		diags.AddError("Failed to Parse Volume Response", err.Error())
		return nil, diags
	}
	if vol.ID != id {
		diags.AddError(
			"This volume read did not identify the requested volume",
			fmt.Sprintf("The response does not carry the id this path asks for (%q). Whatever "+
				"answered is not the volume read this provider builds its contract on, so the "+
				"lookup refuses rather than render a row no service promised. The path was %q.",
				id, pathStr),
		)
		return nil, diags
	}
	return &vol, diags
}

// resolveByName walks the list path: one list request, the name riding the
// service's exact-match filter. The match is re-verified client-side —
// never trust the filter alone — and more than one surviving match is an
// error that names the colliding ids.
func (d *volumeDataSource) resolveByName(ctx context.Context, name string) (*apiVolume, diag.Diagnostics) {
	var diags diag.Diagnostics

	q := url.Values{}
	q.Set("name", name)

	apiResp, err := d.client.Get(ctx, d.client.TenantPath("/volumes"), q)
	if err != nil {
		diags.AddError("Failed to List Volumes", err.Error())
		return nil, diags
	}

	var list apiVolumeList
	if err := json.Unmarshal(apiResp.Body, &list); err != nil {
		diags.AddError("Failed to Parse Volumes Response", err.Error())
		return nil, diags
	}

	// Client-side match over whatever the filter returned. The filter is
	// exact-match server-side, so a well-behaved service can only narrow — but
	// the resolution rides the ROWS, never the filter's word.
	var matches []apiVolume
	for i := range list.Volumes {
		if list.Volumes[i].Name == name {
			matches = append(matches, list.Volumes[i])
		}
	}

	switch len(matches) {
	case 0:
		diags.AddError(
			"No volume with this name",
			fmt.Sprintf("No block volume named %q answers in this tenant. Check the spelling "+
				"of `name`; if you have the volume's id (for example from the portal or from "+
				"`fm`), look it up with `id` instead.", name),
		)
		return nil, diags
	case 1:
		return &matches[0], diags
	default:
		ids := make([]string, 0, len(matches))
		for i := range matches {
			ids = append(ids, matches[i].ID)
		}
		diags.AddError(
			fmt.Sprintf("%d volumes share the name %q", len(matches), name),
			fmt.Sprintf("This tenant carries more than one block volume named %q (%s). The "+
				"data source refuses to pick one for you: every match resolves to a DIFFERENT "+
				"disk, and a silent pick would feed the wrong volume's id into your "+
				"configuration. Disambiguate with `id`, or rename the volumes.",
				name, strings.Join(ids, ", ")),
		)
		return nil, diags
	}
}

// volumePath builds one volume read path behind the same guard the id carries
// at plan time — Read must never trust that a validated configuration is the
// only thing that reaches it.
func volumePath(c *client.Client, id string) (string, error) {
	if err := validVolumeID(id); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/volumes/%s", id)), nil
}

// validVolumeID refuses an id that cannot safely be one path segment. The
// same guard shape the postgres_instance and security_group_rules data
// sources carry: a "." or ".." id does not stay one path segment (the client
// joins with path.Join, which CLEANS), and the cleaned URL addresses a
// DIFFERENT resource.
func validVolumeID(id string) error {
	if id == "" {
		return fmt.Errorf("a volume ID is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\?#%`) {
		return fmt.Errorf("invalid volume ID %q", id)
	}
	return nil
}

// validVolumeIDValidator carries validVolumeID into plan time, so a
// configuration with an unusable id fails before any request is built.
type validVolumeIDValidator struct{}

func (v validVolumeIDValidator) Description(_ context.Context) string {
	return "value must be a usable volume ID: non-empty, and a single URL path segment"
}

func (v validVolumeIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v validVolumeIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	if err := validVolumeID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Volume ID",
			fmt.Sprintf("%s: %s", err.Error(), "a volume ID must be non-empty and "+
				"must not contain a '/', a backslash, '?', '#' or '%', so it can only ever "+
				"address the one volume named."),
		)
	}
}

// setInstanceState maps one verified volume onto the model. Absent-optionals
// are null, never "" or 0 — a check block must be able to tell "no value"
// apart from "empty value". The attachments list renders [], never null,
// because "nothing consumes this volume" is an answer, not an absence.
func setInstanceState(ctx context.Context, state *volumeModel, vol *apiVolume, diags *diag.Diagnostics) {
	state.ID = types.StringValue(vol.ID)
	state.Name = types.StringValue(vol.Name)
	state.Description = stringFromWire(vol.Description)
	state.Size = types.Int64Value(vol.Size)
	state.Status = types.StringValue(vol.Status)
	state.VolumeType = types.StringValue(vol.VolumeType)
	state.AvailabilityZone = stringFromWire(vol.AvailabilityZone)
	state.Bootable = types.BoolValue(vol.Bootable)
	state.Encrypted = types.BoolValue(vol.Encrypted)

	rows := make([]volumeAttachmentRowModel, 0, len(vol.Attachments))
	for i := range vol.Attachments {
		att := &vol.Attachments[i]
		rows = append(rows, volumeAttachmentRowModel{
			ID:         types.StringValue(att.ID),
			VolumeID:   types.StringValue(att.VolumeID),
			InstanceID: types.StringValue(att.InstanceID),
			Device:     types.StringValue(att.Device),
			AttachedAt: types.StringValue(att.AttachedAt),
		})
	}
	attachments, listDiags := types.ListValueFrom(ctx, attachmentRowObjectType(), rows)
	diags.Append(listDiags...)
	if diags.HasError() {
		return
	}
	state.Attachments = attachments

	state.SourceVolumeID = stringFromWire(vol.SourceVolumeID)
	state.SourceSnapshotID = stringFromWire(vol.SourceSnapshotID)
	state.SourceImageID = stringFromWire(vol.SourceImageID)
	state.IOPS = int64FromWire(vol.IOPS)
	state.Throughput = int64FromWire(vol.Throughput)
	// Metadata rides the wire with omitempty: an absent map is "no metadata",
	// rendered as null, never {}. The RESERVED KEYS are filtered exactly as
	// the volume resource's read-back filters them (reservedmeta.FilterVolume):
	// the platform's own keys are not tags, and a data source that rendered
	// them would make every check block comparing this listing against the
	// resource's state count drift that is not there.
	customerMetadata := reservedmeta.FilterVolume(vol.Metadata)
	if len(customerMetadata) > 0 {
		metadataMap, mapDiags := types.MapValueFrom(ctx, types.StringType, customerMetadata)
		diags.Append(mapDiags...)
		if diags.HasError() {
			return
		}
		state.Metadata = metadataMap
	} else {
		state.Metadata = types.MapNull(types.StringType)
	}
	state.CreatedAt = types.StringValue(vol.CreatedAt)
	state.UpdatedAt = types.StringValue(vol.UpdatedAt)
	state.TenantID = types.StringValue(vol.TenantID)
}

// attachmentRowObjectType is the framework type of one `attachments` row.
func attachmentRowObjectType() attr.Type {
	return types.ObjectType{
		AttrTypes: map[string]attr.Type{
			"id":          types.StringType,
			"volume_id":   types.StringType,
			"instance_id": types.StringType,
			"device":      types.StringType,
			"attached_at": types.StringType,
		},
	}
}

// int64FromWire maps an absent optional count to null, not zero — an omitted
// iops/throughput key and a provisioned floor of zero are the same verdict on
// the wire (the service drops a zero via omitempty), and neither is a "0".
func int64FromWire(v *int) types.Int64 {
	if v == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*v))
}

// stringFromWire maps an empty optional string to null, not "".
func stringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}
