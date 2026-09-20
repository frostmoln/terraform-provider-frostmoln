// Package postgres_instance implements the frostmoln_postgres_instance Terraform resource.
package postgres_instance

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// defaultBackupRetentionDays is the retention floor ADR-0085 tells every client to
// reflect: the 35-day COMPLIANCE object-lock window. It is a FLOOR, not a ladder, so
// copying it here is what the ADR asks for rather than a duplicated platform rule.
//
// There is deliberately no schedule counterpart. The platform's schedule default is
// SIZE-AWARE for a point-in-time-recovery instance (daily, weekly or twice-monthly by
// volume), so any literal this provider held would be a copy of one rung of a ladder
// the platform owns — the failure ADR-0015 forbids for prices. An unwritten schedule
// is left unknown and the platform answers with the one it picked.
const defaultBackupRetentionDays = 35

// offerTypePostgreSQL is the /databases surface's discriminator: it carries
// both managed database offers, and this resource resolves PostgreSQL or it
// refuses.
const offerTypePostgreSQL = "postgresql"

// restoreTargetNameMaxLen is the platform's cap on a restore target's name
// (database domain/backup.go's RestoreRequest.Validate).
const restoreTargetNameMaxLen = 63

// apiPostgresInstanceList is one page of the tenant's databases. Only the
// restore path's confirm-by-name lookup reads it.
type apiPostgresInstanceList struct {
	Instances  []apiPostgresInstance `json:"instances"`
	TotalCount int                   `json:"totalCount"`
}

// PostgresInstanceModel is the Terraform state model for a managed PostgreSQL instance.
type PostgresInstanceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Version   types.String `tfsdk:"version"`
	FlavorID  types.String `tfsdk:"flavor_id"`
	StorageGB types.Int64  `tfsdk:"storage_gb"`
	VPCID     types.String `tfsdk:"vpc_id"`
	SubnetID  types.String `tfsdk:"subnet_id"`
	HAEnabled types.Bool   `tfsdk:"ha_enabled"`
	// HAStatus is read-only. ha_enabled records what was REQUESTED and is RequiresReplace;
	// ha_status records what the platform actually has, and the two disagree for instances
	// created before high availability was built (ha_enabled true, ha_status no_standby).
	HAStatus            types.String `tfsdk:"ha_status"`
	BackupEnabled       types.Bool   `tfsdk:"backup_enabled"`
	BackupSchedule      types.String `tfsdk:"backup_schedule"`
	BackupRetentionDays types.Int64  `tfsdk:"backup_retention_days"`
	ParameterGroupID    types.String `tfsdk:"parameter_group_id"`
	// Extensions is the DECLARATIVE set of enabled extension catalog names.
	// fromAPI never reads it from the instance body (the wire carries the
	// extension ledger on a separate GET); every refresh path calls
	// readExtensionsIntoModel, which projects the ledger's enabled entries
	// into this set — removed/failed entries are intent history, not state.
	Extensions types.Set `tfsdk:"extensions"`
	// PITREnabled is the only WRITEABLE PITR attribute: what the customer asks
	// for. It is sent only when the configuration carries it (see
	// toCreateRequest/toUpdateRequest) — pre-GA an OMITTED pitrEnabled resolves
	// OFF even for an entitled tenant, and pitr_capable is fixed at create, so
	// a provider that helpfully filled in a value the practitioner did not
	// write would decide their data-protection posture for them, permanently.
	PITREnabled types.Bool `tfsdk:"pitr_enabled"`
	// The four read-only PITR attributes. PITRCapable is fixed at create; the
	// other three move under the customer between one plan and the next
	// (latest_restorable_time advances with every archived segment), which is
	// why none of them carries UseStateForUnknown and why ModifyPlan re-plans
	// them as unknown on any apply. See the schema.
	PITRCapable             types.Bool   `tfsdk:"pitr_capable"`
	PITRArchivePausedReason types.String `tfsdk:"pitr_archive_paused_reason"`
	EarliestRestorableTime  types.String `tfsdk:"earliest_restorable_time"`
	LatestRestorableTime    types.String `tfsdk:"latest_restorable_time"`
	// RestoreFrom is create-only input, carried as types.Object rather than a
	// pointer-to-struct because an Optional+Computed object is UNKNOWN in the
	// plan of every create that omits it, and the framework's reflection
	// refuses to decode an unknown into a Go pointer.
	RestoreFrom   types.Object `tfsdk:"restore_from"`
	Status        types.String `tfsdk:"status"`
	PrivateIP     types.String `tfsdk:"private_ip"`
	Port          types.Int64  `tfsdk:"port"`
	PublicIP      types.String `tfsdk:"public_ip"`
	AdminUsername types.String `tfsdk:"admin_username"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
	TenantID      types.String `tfsdk:"tenant_id"`
	// Timeouts is the customer-tunable `timeouts` block. fromAPI never touches
	// it: the pointer the practitioner configured rides through every
	// model copy (plan -> early -> state) unchanged, which is what keeps the
	// block stable in state across reads and refreshes.
	Timeouts *timeouts.Model `tfsdk:"timeouts"`
}

// apiPostgresInstance is the API representation of a managed PostgreSQL instance.
type apiPostgresInstance struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type is `postgresql` or `mysql`: the /databases surface carries both, and
	// a restore SOURCE is looked up on it by an id the practitioner supplied,
	// so this resource has to be able to see that the id names the wrong offer.
	Type                string `json:"type"`
	PostgresVersion     string `json:"typeVersion"`
	FlavorID            string `json:"flavorId"`
	StorageGB           int    `json:"storageGb"`
	VPCID               string `json:"vpcId"`
	SubnetID            string `json:"subnetId"`
	HAEnabled           bool   `json:"haEnabled"`
	HAStatus            string `json:"haStatus"`
	BackupEnabled       bool   `json:"backupEnabled"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays int    `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    string `json:"parameterGroupId,omitempty"`
	Status              string `json:"status"`
	PrivateIP           string `json:"privateIp,omitempty"`
	Port                int    `json:"port,omitempty"`
	PublicIP            string `json:"publicIp,omitempty"`
	AdminUsername       string `json:"adminUsername,omitempty"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt,omitempty"`
	TenantID            string `json:"tenantId,omitempty"`

	// PITR. The two booleans are POINTERS because absent and false are
	// different answers here, and reading one as the other is the whole bug
	// class this feature can produce: a database below the P8 floor omits both
	// fields entirely, and a plain `bool` would report that instance as
	// "point-in-time recovery: off" — an assertion no service made — instead of
	// "this deployment does not say".
	PITREnabled *bool `json:"pitrEnabled,omitempty"`
	PITRCapable *bool `json:"pitrCapable,omitempty"`
	// The window and the paused reason are served on the single-instance GET
	// and NOWHERE else (plan §4.15 item 3): on a create, update or list
	// response their absence means nothing at all, which is why every path
	// that fills them re-reads by GET. On a GET their absence does mean
	// something — there is no restorable window right now.
	PITRArchivePausedReason string `json:"pitrArchivePausedReason,omitempty"`
	EarliestRestorableTime  string `json:"earliestRestorableTime,omitempty"`
	LatestRestorableTime    string `json:"latestRestorableTime,omitempty"`
}

// apiCreatePostgresInstanceRequest is the API request to create a managed PostgreSQL instance.
type apiCreatePostgresInstanceRequest struct {
	Name                string `json:"name"`
	PostgresVersion     string `json:"typeVersion"`
	FlavorID            string `json:"flavorId"`
	StorageGB           int    `json:"storageGb"`
	VPCID               string `json:"vpcId"`
	SubnetID            string `json:"subnetId"`
	HAEnabled           *bool  `json:"haEnabled,omitempty"`
	BackupEnabled       *bool  `json:"backupEnabled,omitempty"`
	BackupSchedule      string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int   `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    string `json:"parameterGroupId,omitempty"`
	PITREnabled         *bool  `json:"pitrEnabled,omitempty"`
}

// apiRestorePostgresInstanceRequest is the body for
// POST /databases/{sourceId}/restore. Exactly one of BackupID and
// PITRTimestamp is set: the two together are refused by the service (400
// invalid_input) precisely because an older service silently dropped the
// timestamp and restored the backup instead.
type apiRestorePostgresInstanceRequest struct {
	TargetName    string `json:"targetName"`
	BackupID      string `json:"backupId,omitempty"`
	PITRTimestamp string `json:"pitrTimestamp,omitempty"`
}

// apiUpdatePostgresInstanceRequest is the API request to update a managed
// PostgreSQL instance via PUT. It carries only in-place-updatable fields:
// storage_gb goes through POST /resize (grow-only) and flavor_id changes are
// refused in Update, so neither is sent here (the backend PUT handler has
// no storage field and drops flavor changes silently).
type apiUpdatePostgresInstanceRequest struct {
	Name                *string `json:"name,omitempty"`
	BackupEnabled       *bool   `json:"backupEnabled,omitempty"`
	BackupSchedule      *string `json:"backupSchedule,omitempty"`
	BackupRetentionDays *int    `json:"backupRetentionDays,omitempty"`
	ParameterGroupID    *string `json:"parameterGroupId,omitempty"`
	PITREnabled         *bool   `json:"pitrEnabled,omitempty"`
}

// hasChanges reports whether the update request carries any field to PUT.
func (r apiUpdatePostgresInstanceRequest) hasChanges() bool {
	return r.Name != nil || r.BackupEnabled != nil || r.BackupSchedule != nil ||
		r.BackupRetentionDays != nil || r.ParameterGroupID != nil || r.PITREnabled != nil
}

// apiResizePostgresInstanceRequest is the body for POST /databases/{id}/resize.
// Storage grows online and cannot be shrunk (backend rejects a decrease).
//
// BackupSchedule is not decoration. On a point-in-time-recovery instance the
// platform re-validates the schedule against the NEW size's base-backup floor:
// a schedule the platform chose it re-picks itself, but one the CUSTOMER wrote
// it never rewrites — it refuses the resize with a 400 unless the resize
// request itself carries a schedule that passes. Growing past a threshold and
// sparsening the schedule is therefore a single operation, and sending the two
// separately cannot work: the resize runs first and fails with the fix still
// unsent.
type apiResizePostgresInstanceRequest struct {
	StorageGB      int     `json:"storageGb"`
	BackupSchedule *string `json:"backupSchedule,omitempty"`
}

// toCreateRequest converts the Terraform model to an API create request.
func (m *PostgresInstanceModel) toCreateRequest(_ context.Context, _ *diag.Diagnostics) apiCreatePostgresInstanceRequest {
	req := apiCreatePostgresInstanceRequest{
		Name:            m.Name.ValueString(),
		PostgresVersion: m.Version.ValueString(),
		FlavorID:        m.FlavorID.ValueString(),
		StorageGB:       int(m.StorageGB.ValueInt64()),
		VPCID:           m.VPCID.ValueString(),
		SubnetID:        m.SubnetID.ValueString(),
	}

	if !m.HAEnabled.IsNull() && !m.HAEnabled.IsUnknown() {
		v := m.HAEnabled.ValueBool()
		req.HAEnabled = &v
	}
	if !m.BackupEnabled.IsNull() && !m.BackupEnabled.IsUnknown() {
		v := m.BackupEnabled.ValueBool()
		req.BackupEnabled = &v
	}
	if !m.BackupSchedule.IsNull() && !m.BackupSchedule.IsUnknown() {
		req.BackupSchedule = m.BackupSchedule.ValueString()
	}
	if !m.BackupRetentionDays.IsNull() && !m.BackupRetentionDays.IsUnknown() {
		v := int(m.BackupRetentionDays.ValueInt64())
		req.BackupRetentionDays = &v
	}
	if !m.ParameterGroupID.IsNull() && !m.ParameterGroupID.IsUnknown() {
		req.ParameterGroupID = m.ParameterGroupID.ValueString()
	}
	// Sent ONLY when the configuration carries it. On an omitted attribute the
	// plan value is unknown at create, so the guard below already withholds it
	// — but the rule matters enough to state: pre-GA the service resolves an
	// omitted pitrEnabled to OFF even for an entitled tenant (ADR-0038 clause
	// 8), and pitr_capable is stamped once, at create. A value invented here
	// would be the difference between a database that can be restored to a
	// point in time and one that can never be made to, with destroy-and-
	// recreate as the only remedy.
	if !m.PITREnabled.IsNull() && !m.PITREnabled.IsUnknown() {
		v := m.PITREnabled.ValueBool()
		req.PITREnabled = &v
	}

	return req
}

// toUpdateRequest converts the Terraform model to an API update request, comparing with current state.
func (m *PostgresInstanceModel) toUpdateRequest(state *PostgresInstanceModel) apiUpdatePostgresInstanceRequest {
	req := apiUpdatePostgresInstanceRequest{}

	// An UNKNOWN plan value is never sent, on any field below. Unknown means
	// "whatever the platform decides", and ValueBool()/ValueInt64() of an
	// unknown are the zero values — so sending one would PUT `false` or `0`
	// under the guise of a diff. That is reachable: a restore's create planned
	// its backup fields unknown so the target could inherit the source's, and
	// a resize plans the schedule unknown so the platform can re-pick it.
	if !m.Name.IsUnknown() && !m.Name.Equal(state.Name) {
		v := m.Name.ValueString()
		req.Name = &v
	}
	// The null guard matches pitr_enabled's below, and for the same reason:
	// ValueBool() of a null is `false`, so a null plan value against a `true`
	// state would PUT backups OFF on an instance nobody asked to change.
	if !m.BackupEnabled.IsUnknown() && !m.BackupEnabled.IsNull() && !m.BackupEnabled.Equal(state.BackupEnabled) {
		v := m.BackupEnabled.ValueBool()
		req.BackupEnabled = &v
	}
	// An enable carries the backup policy explicitly even when neither value changed. Both are
	// pinned to state by their plan modifiers, so on an omitted attribute plan == state and the
	// diff-only rule would send nothing -- leaving the server to fill the NULL columns itself.
	// Sending the recorded values keeps the outcome ours to guarantee rather than the server's
	// to choose.
	//
	// It sends a RECORDED value, never a fabricated one. An instance with no schedule on record
	// plans unknown (fromAPI writes null, UseStateForUnknown bails on it), the guard below skips
	// it, and the platform picks the schedule for the size -- which is what should happen, since
	// there is no client-side ladder to pick from.
	enabling := !m.BackupEnabled.IsUnknown() && m.BackupEnabled.ValueBool() && !state.BackupEnabled.ValueBool()
	if !m.BackupSchedule.IsUnknown() && (enabling || !m.BackupSchedule.Equal(state.BackupSchedule)) {
		v := m.BackupSchedule.ValueString()
		if v != "" {
			req.BackupSchedule = &v
		}
	}
	if !m.BackupRetentionDays.IsUnknown() && (enabling || !m.BackupRetentionDays.Equal(state.BackupRetentionDays)) {
		v := int(m.BackupRetentionDays.ValueInt64())
		if v > 0 {
			req.BackupRetentionDays = &v
		}
	}
	if !m.ParameterGroupID.IsUnknown() && !m.ParameterGroupID.Equal(state.ParameterGroupID) {
		v := m.ParameterGroupID.ValueString()
		req.ParameterGroupID = &v
	}
	// O12: the one PITR field that updates in place. Diff-only, and never sent
	// unknown — an omitted attribute is pinned to state by UseStateForUnknown,
	// so plan equals state and nothing is sent, which is what keeps Terraform
	// from deciding a data-protection setting the practitioner left alone.
	// The null guard is not belt-and-braces: ValueBool() of a null is `false`,
	// so a null plan value against a `true` state would PUT a silent disable.
	if !m.PITREnabled.IsUnknown() && !m.PITREnabled.IsNull() && !m.PITREnabled.Equal(state.PITREnabled) {
		v := m.PITREnabled.ValueBool()
		req.PITREnabled = &v
	}

	return req
}

// fromAPI populates the Terraform model from an API response.
func (m *PostgresInstanceModel) fromAPI(_ context.Context, inst *apiPostgresInstance, _ *diag.Diagnostics) {
	m.ID = types.StringValue(inst.ID)
	m.Name = types.StringValue(inst.Name)
	m.Version = types.StringValue(inst.PostgresVersion)
	m.FlavorID = types.StringValue(inst.FlavorID)
	m.StorageGB = types.Int64Value(int64(inst.StorageGB))
	m.VPCID = types.StringValue(inst.VPCID)
	m.SubnetID = types.StringValue(inst.SubnetID)
	m.HAEnabled = types.BoolValue(inst.HAEnabled)
	m.HAStatus = types.StringValue(inst.HAStatus)
	m.BackupEnabled = types.BoolValue(inst.BackupEnabled)
	m.Status = types.StringValue(inst.Status)
	m.CreatedAt = types.StringValue(inst.CreatedAt)

	// Both backup fields are absent from the response whenever the backend column is NULL --
	// every instance created with backups off, since the server applies its defaults only when
	// backup_enabled is true. The two are read back DIFFERENTLY, and the difference is the
	// point:
	//
	//   retention -- SUBSTITUTED with the floor, unconditionally. The reaper reads the column as
	//     GREATEST(COALESCE(backup_retention_days, 35), 35) and provisioning's sweep floors a
	//     zero the same way, so NULL already MEANS 35 and the substitution states what is
	//     already true. Sub-floor legacy values (written before the floor existed; no migration
	//     backfilled them) are floored here too -- the server clamps them on the next update,
	//     and reporting the pre-clamp number would mismatch.
	//
	//   schedule -- NEVER substituted; absent reads back as NULL. This arm used to write
	//     "0 2 * * *" whenever backups were off, so that state held a value to pin. Two things
	//     make that wrong now. The platform's schedule default became SIZE-AWARE for a
	//     point-in-time-recovery instance (daily, weekly or twice-monthly by volume), so there
	//     is no single literal to substitute -- and worse, toUpdateRequest's enable arm then
	//     sent that pinned literal EXPLICITLY, which the platform refuses above ~1,260 GB for
	//     failing the base-backup floor: the exact refusal removing the client-side default was
	//     meant to avoid, moved from create to update. Null is safe because backup_schedule's
	//     UseStateForUnknown bails on a null prior value and leaves the plan unknown, which is
	//     precisely "the platform chooses one for this size".
	//
	//     The old text also justified the substitution as healing a legacy
	//     (backup_enabled = true, schedule NULL) row -- a row that takes no backups and appears
	//     in no overdue metric. That population is gone: ADR-0085 gave every offer a CHECK
	//     forbidding it plus a one-time backfill (database migration 000024), so the database,
	//     not this provider, is what keeps it empty.
	m.BackupSchedule = stringFromWire(inst.BackupSchedule)

	if inst.BackupRetentionDays > defaultBackupRetentionDays {
		m.BackupRetentionDays = types.Int64Value(int64(inst.BackupRetentionDays))
	} else {
		m.BackupRetentionDays = types.Int64Value(defaultBackupRetentionDays)
	}

	if inst.ParameterGroupID != "" {
		m.ParameterGroupID = types.StringValue(inst.ParameterGroupID)
	} else {
		m.ParameterGroupID = types.StringNull()
	}

	if inst.PrivateIP != "" {
		m.PrivateIP = types.StringValue(inst.PrivateIP)
	} else {
		m.PrivateIP = types.StringNull()
	}

	if inst.Port > 0 {
		m.Port = types.Int64Value(int64(inst.Port))
	} else {
		m.Port = types.Int64Null()
	}

	if inst.PublicIP != "" {
		m.PublicIP = types.StringValue(inst.PublicIP)
	} else {
		m.PublicIP = types.StringNull()
	}

	if inst.AdminUsername != "" {
		m.AdminUsername = types.StringValue(inst.AdminUsername)
	} else {
		m.AdminUsername = types.StringNull()
	}

	if inst.UpdatedAt != "" {
		m.UpdatedAt = types.StringValue(inst.UpdatedAt)
	} else {
		m.UpdatedAt = types.StringNull()
	}

	if inst.TenantID != "" {
		m.TenantID = types.StringValue(inst.TenantID)
	} else {
		m.TenantID = types.StringNull()
	}

	// PITR. Absent stays NULL on every one of these — never substituted the way
	// the backup fields above are. There is no literal a client may stand in
	// for them: the window and the paused reason are served on the
	// single-instance GET alone, and the two booleans are absent from a
	// database below the P8 release. "The platform did not say" is the honest
	// value, and it is what a `check` block needs in order to tell it from
	// "the platform said no".
	m.PITREnabled = boolFromWire(inst.PITREnabled)
	m.PITRCapable = boolFromWire(inst.PITRCapable)
	m.PITRArchivePausedReason = stringFromWire(inst.PITRArchivePausedReason)
	m.EarliestRestorableTime = stringFromWire(inst.EarliestRestorableTime)
	m.LatestRestorableTime = stringFromWire(inst.LatestRestorableTime)
}

// boolFromWire maps an absent optional boolean to null, not false.
func boolFromWire(v *bool) types.Bool {
	if v == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*v)
}

// stringFromWire maps an absent optional string to null, not "".
func stringFromWire(v string) types.String {
	if v == "" {
		return types.StringNull()
	}
	return types.StringValue(v)
}

// restoreFromAttrTypes is the object type of the `restore_from` attribute. It
// is declared once and shared by the schema, the model and every null/unknown
// literal, so the three cannot drift into a type mismatch at runtime.
var restoreFromAttrTypes = map[string]attr.Type{
	"source_instance_id": types.StringType,
	"point_in_time":      types.StringType,
	"backup_id":          types.StringType,
}

// restoreFrom is the decoded `restore_from` block.
type restoreFrom struct {
	SourceInstanceID string
	PointInTime      string
	BackupID         string
}

// decodeRestoreFrom reads the block out of a types.Object, returning nil when
// the practitioner did not write one. An UNKNOWN object is also nil: that is
// the shape of every create that omits the block (Optional+Computed with no
// prior state), and it is never a restore.
func decodeRestoreFrom(o types.Object) *restoreFrom {
	if o.IsNull() || o.IsUnknown() {
		return nil
	}
	attrs := o.Attributes()
	str := func(name string) string {
		v, ok := attrs[name].(types.String)
		if !ok || v.IsNull() || v.IsUnknown() {
			return ""
		}
		return v.ValueString()
	}
	return &restoreFrom{
		SourceInstanceID: str("source_instance_id"),
		PointInTime:      str("point_in_time"),
		BackupID:         str("backup_id"),
	}
}

// toRestoreRequest builds the POST body. The caller has already established
// that exactly one of the two selectors is set (schema validators, re-checked
// in Create).
func (rf *restoreFrom) toRestoreRequest(targetName string) apiRestorePostgresInstanceRequest {
	return apiRestorePostgresInstanceRequest{
		TargetName:    targetName,
		BackupID:      rf.BackupID,
		PITRTimestamp: rf.PointInTime,
	}
}
