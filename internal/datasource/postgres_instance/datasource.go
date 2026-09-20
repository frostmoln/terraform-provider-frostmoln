// Package postgres_instance implements the frostmoln_postgres_instance
// Terraform data source: the name→id resolver for a PostgreSQL instance — the
// lookup a configuration needs to reach an instance that Terraform did not
// create (a portal or `fm`-provisioned database can today be referenced only
// by a hardcoded UUID or plumbed through terraform_remote_state).
//
// The shape is the house resolver shape (`frostmoln_vpc`): `id` or `name`,
// exactly one; an id goes straight to the instance read, a name lists the
// tenant's databases and matches client-side. A name resolver must do the
// match client-side: the database list service has no name filter (its list
// options are status/type/limit/offset).
//
// Ambiguity and absence both fail the read. A resolver that silently picked
// the first of several same-named instances would feed the wrong database's
// endpoint into a configuration — exactly the mistake this data source exists
// to prevent — so more than one match is an error that names the colliding
// ids, and zero matches is an error, never an empty row. A row whose `type`
// is `mysql` can never satisfy this data source, in a name lookup or in an id
// read: the type is checked on both paths, so a mysql instance is reported as
// absent rather than resolved. (The list pages: the repository applies a
// default limit of 50, so one unfiltered request can miss instances — Read
// walks the pages until the name is found or the list is exhausted.)
package postgres_instance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

var _ datasource.DataSource = &postgresInstanceDataSource{}

const offerType = "postgresql"

// NewDataSource returns a new frostmoln_postgres_instance data source factory.
func NewDataSource() datasource.DataSource {
	return &postgresInstanceDataSource{}
}

type postgresInstanceDataSource struct {
	client *client.Client
}

// postgresInstanceModel is the Terraform state model. Attribute names mirror
// the frostmoln_postgres_instance resource's (version from the wire's
// typeVersion, flavor_id, storage_gb, …), so a data source read can be fed
// straight into a reference without translating names.
type postgresInstanceModel struct {
	ID                  types.String `tfsdk:"id"`
	Name                types.String `tfsdk:"name"`
	Type                types.String `tfsdk:"type"`
	Version             types.String `tfsdk:"version"`
	FlavorID            types.String `tfsdk:"flavor_id"`
	StorageGB           types.Int64  `tfsdk:"storage_gb"`
	VPCID               types.String `tfsdk:"vpc_id"`
	SubnetID            types.String `tfsdk:"subnet_id"`
	PrivateIP           types.String `tfsdk:"private_ip"`
	Port                types.Int64  `tfsdk:"port"`
	PublicIP            types.String `tfsdk:"public_ip"`
	Status              types.String `tfsdk:"status"`
	HAEnabled           types.Bool   `tfsdk:"ha_enabled"`
	HAStatus            types.String `tfsdk:"ha_status"`
	BackupEnabled       types.Bool   `tfsdk:"backup_enabled"`
	BackupSchedule      types.String `tfsdk:"backup_schedule"`
	BackupRetentionDays types.Int64  `tfsdk:"backup_retention_days"`
	ParameterGroupID    types.String `tfsdk:"parameter_group_id"`
	SecurityGroupID     types.String `tfsdk:"security_group_id"`
	AdminUsername       types.String `tfsdk:"admin_username"`
	CreatedAt           types.String `tfsdk:"created_at"`
	UpdatedAt           types.String `tfsdk:"updated_at"`
	TenantID            types.String `tfsdk:"tenant_id"`
	// Point-in-time recovery. The same five attributes the resource exposes,
	// so a data-source read can be fed straight into a check block or an
	// output without translating names.
	PITREnabled             types.Bool   `tfsdk:"pitr_enabled"`
	PITRCapable             types.Bool   `tfsdk:"pitr_capable"`
	PITRArchivePausedReason types.String `tfsdk:"pitr_archive_paused_reason"`
	EarliestRestorableTime  types.String `tfsdk:"earliest_restorable_time"`
	LatestRestorableTime    types.String `tfsdk:"latest_restorable_time"`
}

// apiDatabaseInstance is the API representation of one database instance —
// the same wire shape the postgres/mysql resources map (the /databases
// surface carries both types; the `type` field is `postgresql` or `mysql`).
type apiDatabaseInstance struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Type                string `json:"type"`
	TypeVersion         string `json:"typeVersion"`
	FlavorID            string `json:"flavorId"`
	StorageGB           int    `json:"storageGb"`
	VPCID               string `json:"vpcId"`
	SubnetID            string `json:"subnetId"`
	PrivateIP           string `json:"privateIp,omitempty"`
	Port                int    `json:"port"`
	PublicIP            string `json:"publicIp,omitempty"`
	Status              string `json:"status"`
	HAEnabled           bool   `json:"haEnabled"`
	HAStatus            string `json:"haStatus,omitempty"`
	BackupEnabled       bool   `json:"backupEnabled"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int   `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    string `json:"parameterGroupId,omitempty"`
	SecurityGroupID     string `json:"securityGroupId,omitempty"`
	AdminUsername       string `json:"adminUsername,omitempty"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
	TenantID            string `json:"tenantId,omitempty"`
	// Point-in-time recovery. The booleans are POINTERS because absent is not
	// false: a database service older than the release that added the feature
	// omits them, and reporting that as "recovery: off" would be an assertion
	// no service made.
	PITREnabled *bool `json:"pitrEnabled,omitempty"`
	PITRCapable *bool `json:"pitrCapable,omitempty"`
	// The window and the pause reason are served on the single-instance GET
	// and on nothing else — which is why resolveByName re-reads the instance
	// it found by id instead of rendering the list row it matched.
	PITRArchivePausedReason string `json:"pitrArchivePausedReason,omitempty"`
	EarliestRestorableTime  string `json:"earliestRestorableTime,omitempty"`
	LatestRestorableTime    string `json:"latestRestorableTime,omitempty"`
}

type apiDatabaseInstanceList struct {
	Instances  []apiDatabaseInstance `json:"instances"`
	TotalCount int                   `json:"totalCount"`
}

func (d *postgresInstanceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_postgres_instance"
}

func (d *postgresInstanceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Look up a PostgreSQL instance by ID or name. Exactly one of `id` or " +
			"`name` must be specified.\n\n" +

			"This is how a configuration reaches a database that Terraform did not create: an " +
			"instance provisioned in the portal or with `fm` can today be referenced only by a " +
			"hardcoded UUID or plumbed through `terraform_remote_state` — both of which tie the " +
			"configuration to one deployment. Look it up by name instead and the reference " +
			"survives re-provisioning elsewhere.\n\n" +

			"A name lookup that matches NOTHING fails the read, and so does one that matches " +
			"MORE THAN ONE instance: the data source refuses to pick for you (the diagnostic " +
			"names the colliding ids). A `mysql` instance never satisfies this data source, by " +
			"id or by name — use `frostmoln_mysql_instance`. There is no composed `endpoint` " +
			"attribute: the platform serves the instance's address as `private_ip`/`public_ip` " +
			"plus `port`, which is the wire shape every client composes from.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description: "The unique identifier of the PostgreSQL instance. Exactly one of " +
					"id or name must be specified.",
				Optional: true,
				Validators: []validator.String{
					validInstanceIDValidator{},
				},
			},
			"name": schema.StringAttribute{
				Description: "The name of the PostgreSQL instance. Exactly one of id or name " +
					"must be specified.",
				Optional: true,
			},
			"type": schema.StringAttribute{
				Description: "The database type. Always `postgresql` on this data source — the " +
					"type is checked on every path, so the value is platform-verified, not a " +
					"restatement of the configuration.",
				Computed: true,
			},
			"version": schema.StringAttribute{
				Description: "The PostgreSQL version (the wire's typeVersion).",
				Computed:    true,
			},
			"flavor_id": schema.StringAttribute{
				Description: "The flavor ID/size of the instance.",
				Computed:    true,
			},
			"storage_gb": schema.Int64Attribute{
				Description: "The storage size in gigabytes.",
				Computed:    true,
			},
			"vpc_id": schema.StringAttribute{
				Description: "The VPC ID where the instance is deployed.",
				Computed:    true,
			},
			"subnet_id": schema.StringAttribute{
				Description: "The subnet ID where the instance is deployed.",
				Computed:    true,
			},
			"private_ip": schema.StringAttribute{
				Description: "The private IP address of the instance. Compose the connection " +
					"address with `port` — the platform serves no composed endpoint attribute.",
				Computed: true,
			},
			"port": schema.Int64Attribute{
				Description: "The port the instance listens on.",
				Computed:    true,
			},
			"public_ip": schema.StringAttribute{
				Description: "The public IP address of the instance, null when the instance is " +
					"not publicly reachable.",
				Computed: true,
			},
			"status": schema.StringAttribute{
				Description: "The current status of the instance.",
				Computed:    true,
			},
			"ha_enabled": schema.BoolAttribute{
				Description: "Whether high availability is enabled.",
				Computed:    true,
			},
			"ha_status": schema.StringAttribute{
				Description: "The high-availability status, null when HA is not enabled.",
				Computed:    true,
			},
			"backup_enabled": schema.BoolAttribute{
				Description: "Whether backups are enabled.",
				Computed:    true,
			},
			"backup_schedule": schema.StringAttribute{
				Description: "The backup schedule, null when backups are not enabled.",
				Computed:    true,
			},
			"backup_retention_days": schema.Int64Attribute{
				Description: "How many days backups are retained, null when unset.",
				Computed:    true,
			},
			"parameter_group_id": schema.StringAttribute{
				Description: "The parameter group attached to the instance, null when the " +
					"instance runs on server defaults.",
				Computed: true,
			},
			"security_group_id": schema.StringAttribute{
				Description: "The platform-managed security group attached to the instance. " +
					"The platform creates and owns it: rule writes on it are refused (read it " +
					"with `frostmoln_security_group_rules`); import it only to understand it, " +
					"never to manage it.",
				Computed: true,
			},
			"admin_username": schema.StringAttribute{
				Description: "The admin username for the instance. The admin password is never " +
					"served by the read surface.",
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				Description: "The timestamp when the instance was created.",
				Computed:    true,
			},
			"updated_at": schema.StringAttribute{
				Description: "The timestamp when the instance was last updated, null when the " +
					"platform has not recorded one.",
				Computed: true,
			},
			"tenant_id": schema.StringAttribute{
				Description: "The tenant ID that owns this instance.",
				Computed:    true,
			},
			"pitr_enabled": schema.BoolAttribute{
				Description: "Whether point-in-time recovery is on for this instance, null when the " +
					"platform does not report it. Archiving actually runs only when this AND " +
					"`backup_enabled` are true.",
				Computed: true,
			},
			"pitr_capable": schema.BoolAttribute{
				Description: "Whether the instance CAN do point-in-time recovery. Fixed when it was " +
					"created and never changes, so it is what tells \"turned off\" apart from " +
					"\"cannot be turned on\".",
				Computed: true,
			},
			"pitr_archive_paused_reason": schema.StringAttribute{
				Description: "Why the platform has paused write-ahead log archiving, null when it is " +
					"not paused. `tenant_cap` is the tenant's retained-storage cap; " +
					"`platform_disabled` is a fleet-wide pause. Any other value means a reason this " +
					"provider release does not know about.",
				Computed: true,
			},
			"earliest_restorable_time": schema.StringAttribute{
				Description: "The earliest instant the instance can be restored to (RFC 3339, UTC), " +
					"null when it has no restorable window right now.",
				Computed: true,
			},
			"latest_restorable_time": schema.StringAttribute{
				Description: "The latest instant the instance can be restored to (RFC 3339, UTC), " +
					"null when it has no restorable window right now. It advances continuously " +
					"while archiving runs, so it is a reading taken at refresh time, not a value " +
					"that stays true — the platform checks a restore target against the window it " +
					"has at that moment.",
				Computed: true,
			},
		},
	}
}

func (d *postgresInstanceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *postgresInstanceDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg postgresInstanceModel
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

	var inst *apiDatabaseInstance
	if idSet {
		found, diags := d.readByID(ctx, cfg.ID.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		inst = found
	} else {
		found, diags := d.resolveByName(ctx, cfg.Name.ValueString())
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		// The LIST row is how the name was resolved, but it is not what this
		// data source renders: the restorable window and the archive pause
		// reason are served on the single-instance GET and on no other
		// response, so a name lookup that stopped at the list row would report
		// every instance as having no window at all. Re-read the instance the
		// name found, so an id lookup and a name lookup answer identically.
		reread, rereadDiags := d.readByID(ctx, found.ID)
		resp.Diagnostics.Append(rereadDiags...)
		if resp.Diagnostics.HasError() {
			return
		}
		inst = reread
	}

	setInstanceState(&cfg, inst)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// readByID walks the id path: one instance read, then the type check. The
// identity guard refuses a 200 that does not carry the requested id — whatever
// answered is not the instance read this provider builds its contract on.
func (d *postgresInstanceDataSource) readByID(ctx context.Context, id string) (*apiDatabaseInstance, diag.Diagnostics) {
	var diags diag.Diagnostics

	pathStr, pathErr := databasePath(d.client, id)
	if pathErr != nil {
		diags.AddError("Invalid Instance ID", pathErr.Error())
		return nil, diags
	}

	apiResp, err := d.client.Get(ctx, pathStr, nil)
	if err != nil {
		if client.IsNotFound(err) {
			diags.AddError(
				"The PostgreSQL instance does not exist",
				fmt.Sprintf("The id %q answers 404 — there is no such database instance (or "+
					"none this caller can see), so there is nothing to resolve. Correct `id`, "+
					"or look the instance up by `name` instead.\n\n%s", id, err.Error()),
			)
			return nil, diags
		}
		diags.AddError("Failed to Read PostgreSQL Instance", err.Error())
		return nil, diags
	}

	var inst apiDatabaseInstance
	if err := json.Unmarshal(apiResp.Body, &inst); err != nil {
		diags.AddError("Failed to Parse PostgreSQL Instance Response", err.Error())
		return nil, diags
	}
	if inst.ID != id {
		diags.AddError(
			"This instance read did not identify the requested instance",
			fmt.Sprintf("The response does not carry the id this path asks for (%q). Whatever "+
				"answered is not the instance read this provider builds its contract on, so the "+
				"lookup refuses rather than render a row no service promised. The path was %q.",
				id, pathStr),
		)
		return nil, diags
	}
	if inst.Type != offerType {
		diags.AddError(
			fmt.Sprintf("The instance is a %s instance, not a PostgreSQL one", inst.Type),
			fmt.Sprintf("The /databases surface carries both database types, and instance %q "+
				"is a %s instance. `frostmoln_postgres_instance` never resolves a %s row — "+
				"for a `mysql` instance use `frostmoln_mysql_instance`.", inst.ID, inst.Type, inst.Type),
		)
		return nil, diags
	}
	return &inst, diags
}

// resolveByName walks the list path. The list has no name filter and pages at
// a repository default limit of 50, so Read walks the pages until the name is
// found or the list is exhausted; more than one match is an error that names
// the colliding ids. A `mysql` row can never match.
func (d *postgresInstanceDataSource) resolveByName(ctx context.Context, name string) (*apiDatabaseInstance, diag.Diagnostics) {
	var diags diag.Diagnostics

	var matches []apiDatabaseInstance
	const pageSize = 100
	const maxPages = 20
	// searchExhausted records the loop hitting the page cap with a FULL final
	// page: the tenant may carry instances beyond the search window, so a
	// zero-match verdict then is "not in the window we searched", never the
	// plain absence the not-found arm asserts.
	searchExhausted := false
	for offset, page := 0, 0; page < maxPages; offset, page = offset+pageSize, page+1 {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))

		apiResp, err := d.client.Get(ctx, d.client.TenantPath("/databases"), q)
		if err != nil {
			diags.AddError("Failed to List Database Instances", err.Error())
			return nil, diags
		}

		var list apiDatabaseInstanceList
		if err := json.Unmarshal(apiResp.Body, &list); err != nil {
			diags.AddError("Failed to Parse Database Instances Response", err.Error())
			return nil, diags
		}

		for i := range list.Instances {
			// A `mysql` row can never count as a match here — the same rule
			// the id path enforces: this data source resolves PostgreSQL
			// instances or it fails, it never resolves the wrong type.
			if list.Instances[i].Name == name && list.Instances[i].Type == offerType {
				matches = append(matches, list.Instances[i])
			}
		}
		if len(list.Instances) < pageSize {
			break
		}
		if page == maxPages-1 {
			searchExhausted = true
		}
	}

	switch len(matches) {
	case 0:
		if searchExhausted {
			diags.AddError(
				fmt.Sprintf("No PostgreSQL instance named %q in the searched window", name),
				fmt.Sprintf("The tenant's database list did not terminate within the search "+
					"window (%d pages of %d), so the lookup cannot claim the name is absent — "+
					"there may be instances beyond it. Narrow with `id`, or ask the tenant to "+
					"rename instances to a unique name.", maxPages, pageSize),
			)
			return nil, diags
		}
		diags.AddError(
			"No PostgreSQL instance with this name",
			fmt.Sprintf("No database instance named %q resolves to a PostgreSQL instance in "+
				"this tenant. Check the spelling of `name`; if the instance is a MySQL one, "+
				"look it up with `frostmoln_mysql_instance`.", name),
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
			fmt.Sprintf("%d PostgreSQL instances share the name %q", len(matches), name),
			fmt.Sprintf("The tenant's databases carry more than one PostgreSQL instance with "+
				"this name (%s). The data source refuses to pick one for you: every match "+
				"resolves to a DIFFERENT instance, and a silent pick would feed the wrong "+
				"database's address into your configuration. Disambiguate with `id`, or rename "+
				"the instances.", strings.Join(ids, ", ")),
		)
		return nil, diags
	}
}

// databasePath builds one instance read path behind the same guard the id
// carries at plan time — Read must never trust that a validated configuration
// is the only thing that reaches it.
func databasePath(c *client.Client, id string) (string, error) {
	if err := validInstanceID(id); err != nil {
		return "", err
	}
	return c.TenantPath(fmt.Sprintf("/databases/%s", id)), nil
}

// validInstanceID refuses an id that cannot safely be one path segment. The
// same guard shape the vpc_routes and security_group_rules listings carry:
// a "." or ".." id does not stay one path segment (the client joins with
// path.Join, which CLEANS), and the cleaned URL addresses a DIFFERENT
// resource.
func validInstanceID(id string) error {
	if id == "" {
		return fmt.Errorf("an instance ID is required")
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\?#%`) {
		return fmt.Errorf("invalid instance ID %q", id)
	}
	return nil
}

// validInstanceIDValidator carries validInstanceID into plan time, so a
// configuration with an unusable id fails before any request is built.
type validInstanceIDValidator struct{}

func (v validInstanceIDValidator) Description(_ context.Context) string {
	return "value must be a usable database instance ID: non-empty, and a single URL path segment"
}

func (v validInstanceIDValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (v validInstanceIDValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsUnknown() || req.ConfigValue.IsNull() {
		return
	}
	if err := validInstanceID(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid Instance ID",
			fmt.Sprintf("%s: %s", err.Error(), "a database instance ID must be non-empty and "+
				"must not contain a '/', a backslash, '?', '#' or '%', so it can only ever "+
				"address the one instance named."),
		)
	}
}

// setInstanceState maps one verified instance onto the model. Absent-optionals
// are null, never "" or 0 — a check block must be able to tell "no value"
// apart from "empty value".
func setInstanceState(state *postgresInstanceModel, inst *apiDatabaseInstance) {
	state.ID = types.StringValue(inst.ID)
	state.Name = types.StringValue(inst.Name)
	state.Type = types.StringValue(inst.Type)
	state.Version = types.StringValue(inst.TypeVersion)
	state.FlavorID = types.StringValue(inst.FlavorID)
	state.StorageGB = types.Int64Value(int64(inst.StorageGB))
	state.VPCID = types.StringValue(inst.VPCID)
	state.SubnetID = types.StringValue(inst.SubnetID)
	state.PrivateIP = stringFromWire(inst.PrivateIP)
	state.Port = types.Int64Value(int64(inst.Port))
	state.PublicIP = stringFromWire(inst.PublicIP)
	state.Status = types.StringValue(inst.Status)
	state.HAEnabled = types.BoolValue(inst.HAEnabled)
	state.HAStatus = stringFromWire(inst.HAStatus)
	state.BackupEnabled = types.BoolValue(inst.BackupEnabled)
	state.BackupSchedule = stringFromWire(inst.BackupSchedule)
	state.BackupRetentionDays = int64FromWire(inst.BackupRetentionDays)
	state.ParameterGroupID = stringFromWire(inst.ParameterGroupID)
	state.SecurityGroupID = stringFromWire(inst.SecurityGroupID)
	state.AdminUsername = stringFromWire(inst.AdminUsername)
	state.CreatedAt = stringFromWire(inst.CreatedAt)
	state.UpdatedAt = stringFromWire(inst.UpdatedAt)
	state.TenantID = stringFromWire(inst.TenantID)
	state.PITREnabled = boolFromWire(inst.PITREnabled)
	state.PITRCapable = boolFromWire(inst.PITRCapable)
	state.PITRArchivePausedReason = stringFromWire(inst.PITRArchivePausedReason)
	state.EarliestRestorableTime = stringFromWire(inst.EarliestRestorableTime)
	state.LatestRestorableTime = stringFromWire(inst.LatestRestorableTime)
}

// boolFromWire maps an absent optional boolean to null, not false.
func boolFromWire(v *bool) types.Bool {
	if v == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*v)
}

// int64FromWire maps an absent optional count to null, not zero.
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
