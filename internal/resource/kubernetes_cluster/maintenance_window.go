package kubernetes_cluster

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// maintenanceWindowOperation is what the API demands whenever a request body carries
// the field, before it judges the content and whether or not the value changed.
const maintenanceWindowOperation = "kubernetes:kubernetes-clusters:update-maintenance-window"

// maintenanceWindowHolders says who can hold the operation. The same words go in every
// refusal, warning and description, so a credential's remedy is stated one way.
const maintenanceWindowHolders = "The Owner and Admin roles hold it, as does a group document that names it or a " +
	"custom role with kubernetes:*; an API key needs an access policy that names it."

// The remedy for each refusal. A create is not an update: there is no stored value to
// restore, and if it is the create half of a replacement the old cluster is already gone.
const (
	maintenanceWindowUpdateRefused = "The request was refused and nothing in it was applied. To apply your other changes, " +
		"remove maintenance_window from the configuration or set it to the stored value, which a refresh shows."
	maintenanceWindowCreateRefused = "The cluster was not created. If this create is a replacement, the old cluster is " +
		"already destroyed. Remove maintenance_window from the configuration to take the window the platform assigns, " +
		"or apply with a credential that holds the operation."
)

var maintenanceWindowAttrTypes = map[string]attr.Type{
	"days":           types.SetType{ElemType: types.StringType},
	"start_time":     types.StringType,
	"duration_hours": types.Int64Type,
	"time_zone":      types.StringType,
}

// apiMaintenanceWindow is the wire shape on create, PUT and GET.
type apiMaintenanceWindow struct {
	Days          []string `json:"days"`
	StartTime     string   `json:"startTime"`
	DurationHours int64    `json:"durationHours"`
	TimeZone      string   `json:"timeZone"`
}

func nullMaintenanceWindow() types.Object {
	return types.ObjectNull(maintenanceWindowAttrTypes)
}

func maintenanceWindowFromAPI(w *apiMaintenanceWindow) types.Object {
	return types.ObjectValueMust(maintenanceWindowAttrTypes, map[string]attr.Value{
		"days":           stringSliceToSet(w.Days),
		"start_time":     types.StringValue(w.StartTime),
		"duration_hours": types.Int64Value(w.DurationHours),
		"time_zone":      types.StringValue(w.TimeZone),
	})
}

// maintenanceWindowToAPI is the request form of a known value, and nil for null or
// unknown, which is how a create without the attribute omits the field.
func maintenanceWindowToAPI(o types.Object) *apiMaintenanceWindow {
	if o.IsNull() || o.IsUnknown() {
		return nil
	}
	a := o.Attributes()
	days, _ := a["days"].(types.Set)
	start, _ := a["start_time"].(types.String)
	hours, _ := a["duration_hours"].(types.Int64)
	zone, _ := a["time_zone"].(types.String)
	sorted := setToStringSlice(days)
	slices.Sort(sorted)
	return &apiMaintenanceWindow{
		Days:          sorted,
		StartTime:     start.ValueString(),
		DurationHours: hours.ValueInt64(),
		TimeZone:      zone.ValueString(),
	}
}

// maintenanceWindowToSend is the field of an update, nil unless the customer's plan
// moves the window. 🔴 The API refuses the field's PRESENCE from a caller without the
// operation, and the plan carries the stored value whether or not the configuration
// names it. Sending it from there would fail every apply by a Developer or Member,
// so a value equal to the state is never sent, days compared as a set.
func maintenanceWindowToSend(plan, state types.Object) *apiMaintenanceWindow {
	if plan.Equal(state) {
		return nil
	}
	return maintenanceWindowToAPI(plan)
}

// settledMaintenanceWindow is the state value after a create or update. A planned
// value stays as planned, because Terraform refuses a result that differs from a
// known plan; a change made outside Terraform in the meantime is the next refresh's
// drift. An unset attribute on a new cluster takes what the server chose.
func settledMaintenanceWindow(planned types.Object, w *apiMaintenanceWindow) types.Object {
	if !planned.IsNull() && !planned.IsUnknown() {
		return planned
	}
	if w == nil {
		return nullMaintenanceWindow()
	}
	return maintenanceWindowFromAPI(w)
}

// addMaintenanceWindowRefusal reports a refusal of the window's operation in the
// practitioner's terms, and whether err was one. Another 403 keeps its own message.
func addMaintenanceWindowRefusal(diags *diag.Diagnostics, err error, remedy string) bool {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 403 ||
		(apiErr.RequiredPermission != maintenanceWindowOperation && !strings.Contains(apiErr.Message, maintenanceWindowOperation)) {
		return false
	}
	diags.AddAttributeError(path.Root("maintenance_window"), "Not Allowed To Set The Maintenance Window",
		"Setting maintenance_window needs the "+maintenanceWindowOperation+" operation, which this credential does not hold. "+
			maintenanceWindowHolders+" "+remedy+"\n\n"+err.Error())
	return true
}

// warnReplaceWithMaintenanceWindow warns when this plan replaces the cluster and the
// configuration names the window. The destroy runs first and needs no such operation,
// so a credential without it loses the cluster and is then refused the create. It reads
// the CONFIGURATION: the plan always holds a window. A -replace or a taint is not seen:
// such a plan changes no attribute, and the provider is told nothing of either.
func warnReplaceWithMaintenanceWindow(ctx context.Context, req resource.ModifyPlanRequest, plan KubernetesClusterModel, diags *diag.Diagnostics) {
	if req.State.Raw.IsNull() {
		return
	}
	var state KubernetesClusterModel
	if d := req.State.Get(ctx, &state); d.HasError() {
		return
	}
	if !plannedReplacement(plan, state) {
		return
	}
	var configured types.Object
	if d := req.Config.GetAttribute(ctx, path.Root("maintenance_window"), &configured); d.HasError() || configured.IsNull() {
		return
	}
	diags.AddAttributeWarning(path.Root("maintenance_window"),
		"Replacing this cluster: creating it again needs the maintenance window operation",
		"This plan replaces the Kubernetes cluster and the configuration names maintenance_window. The old cluster is "+
			"destroyed first, and the create that follows is refused without the "+maintenanceWindowOperation+" operation. "+
			maintenanceWindowHolders+" Apply with a credential that holds it, or remove maintenance_window from the "+
			"configuration, so the new cluster takes the window the platform assigns.")
}
