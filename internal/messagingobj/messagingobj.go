// Package messagingobj is the shared core of the three LavinMQ object resources
// (frostmoln_messaging_vhost, frostmoln_messaging_user and
// frostmoln_messaging_permission) and their data sources: the wire types, the
// paths, the per-instance mutation lock, the wait-for-terminal-state poll and
// the refusal diagnostics.
//
// The server contract is the messaging service's api/openapi/messaging-api.yaml
// (MESSAGING-VHOSTS-USERS-PLAN §2). The parts every caller here depends on:
//
//   - Every mutation answers 202 with the object (`pending`, or `deleting`), or a
//     delete answers 204 when the broker never received the object.
//   - The OBJECT is polled, never /v1/operations: `operationId` is for support.
//   - `ready` with no failureReason is success. `ready` WITH a failureReason is
//     a change that FAILED with the broker unchanged (a grant replace or a
//     delete). `error` is a failed create: the object is absent, or its state is
//     unknown.
package messagingobj

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/timeouts"
)

// Object statuses (the OpenAPI Status enum).
const (
	StatusPending  = "pending"
	StatusReady    = "ready"
	StatusDeleting = "deleting"
	StatusError    = "error"

	// statusGone is not a server status: the poll reports it when the object
	// answers a service 404, which is what a finished delete looks like.
	statusGone = "gone"
	// statusTerminal ends a wait on an error no retry can clear.
	statusTerminal = "terminal-error"
)

// FailureOutcomeUnknown is the one failure reason after which the broker may
// have changed; every other reason means it is unchanged.
const FailureOutcomeUnknown = "OUTCOME_UNKNOWN"

// Refusal codes of the object surface. The object codes are upper-case and the
// generic servicekit codes lower-case; HasCode matches either casing, so a
// later normalisation on either side does not silently disable a branch.
const (
	CodeCapabilityAbsent    = "CAPABILITY_ABSENT"
	CodeOperationInProgress = "OPERATION_IN_PROGRESS"
	CodeRotationInProgress  = "ROTATION_IN_PROGRESS"
	CodeObjectExists        = "OBJECT_EXISTS"
	CodeObjectInUse         = "OBJECT_IN_USE"
	CodeObjectNotReady      = "OBJECT_NOT_READY"
	CodeObjectLimitReached  = "OBJECT_LIMIT_REACHED"
	CodeInvalidState        = "invalid_state"
)

// NamePattern is the server's ObjectName rule: 1..64 of [A-Za-z0-9._-],
// starting with a letter or digit. Validated at plan time so a bad name fails
// before anything is sent.
const NamePattern = `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`

// NameRule is NamePattern in words, for attribute descriptions.
const NameRule = "1 to 64 characters of `A-Z`, `a-z`, `0-9`, `.`, `_` and `-`, starting with a letter or digit, " +
	"unique on the instance regardless of case (`Orders` cannot be created while `orders` exists); a change in " +
	"case is a different name"

// DefaultPollInterval is how often a wait re-reads the object.
const DefaultPollInterval = 5 * time.Second

// DefaultTimeout is every verb's default wait budget. Operations on one
// instance run one at a time in the guest and a queued one is retried for about
// 20 minutes before it fails IN_FLIGHT, so the budget sits above that: a wait
// that gives up first reports a timeout for an operation the platform is still
// legitimately working, instead of its real verdict.
const DefaultTimeout = 30 * time.Minute

// Object is a vhost or an application user: both carry the same fields.
type Object struct {
	Name           string `json:"name"`
	Status         string `json:"status"`
	FailureReason  string `json:"failureReason,omitempty"`
	FailureMessage string `json:"failureMessage,omitempty"`
	OperationID    string `json:"operationId,omitempty"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// Permission is one user's grant on one vhost.
type Permission struct {
	User           string `json:"user"`
	Vhost          string `json:"vhost"`
	Configure      string `json:"configure"`
	Write          string `json:"write"`
	Read           string `json:"read"`
	Status         string `json:"status"`
	FailureReason  string `json:"failureReason,omitempty"`
	FailureMessage string `json:"failureMessage,omitempty"`
	OperationID    string `json:"operationId,omitempty"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// PermissionList is the body of both grant lists (per user and per vhost).
type PermissionList struct {
	Permissions []Permission `json:"permissions"`
}

// Credentials is GET .../users/{user}/credentials. Never logged.
type Credentials struct {
	Host      string `json:"host"`
	AMQPPort  int    `json:"amqpPort"`
	AMQPSPort int    `json:"amqpsPort"`
	Username  string `json:"username"`
	Password  string `json:"password"`
}

// CreateRequest is the vhost and user create body.
type CreateRequest struct {
	Name string `json:"name"`
}

// SetPermissionRequest is the grant PUT body; every regex is required.
type SetPermissionRequest struct {
	Configure string `json:"configure"`
	Write     string `json:"write"`
	Read      string `json:"read"`
}

// Paths, all under the tenant: /v1/tenants/{t}/messaging/{instanceId}/...

func instancePath(c *client.Client, instanceID string) string {
	return c.TenantPath("/messaging/" + url.PathEscape(instanceID))
}

// VhostsPath is the instance's vhost collection.
func VhostsPath(c *client.Client, instanceID string) string {
	return instancePath(c, instanceID) + "/vhosts"
}

// VhostPath is one vhost.
func VhostPath(c *client.Client, instanceID, vhost string) string {
	return VhostsPath(c, instanceID) + "/" + url.PathEscape(vhost)
}

// VhostPermissionsPath lists every grant on one vhost.
func VhostPermissionsPath(c *client.Client, instanceID, vhost string) string {
	return VhostPath(c, instanceID, vhost) + "/permissions"
}

// UsersPath is the instance's user collection.
func UsersPath(c *client.Client, instanceID string) string {
	return instancePath(c, instanceID) + "/users"
}

// UserPath is one user.
func UserPath(c *client.Client, instanceID, user string) string {
	return UsersPath(c, instanceID) + "/" + url.PathEscape(user)
}

// CredentialsPath is one user's password.
func CredentialsPath(c *client.Client, instanceID, user string) string {
	return UserPath(c, instanceID, user) + "/credentials"
}

// UserPermissionsPath lists every grant one user holds.
func UserPermissionsPath(c *client.Client, instanceID, user string) string {
	return UserPath(c, instanceID, user) + "/permissions"
}

// PermissionPath is one user's grant on one vhost.
func PermissionPath(c *client.Client, instanceID, user, vhost string) string {
	return UserPermissionsPath(c, instanceID, user) + "/" + url.PathEscape(vhost)
}

// ---------------------------------------------------------------------------
// The per-instance mutation lock.
//
// WHY: operations on ONE messaging instance run one at a time inside its guest.
// Many issued at once queue and are retried for about 18-20 minutes, after
// which the late ones fail IN_FLIGHT with nothing changed — so a configuration
// with a dozen users and grants, applied at Terraform's default
// -parallelism=10, would otherwise fail part of its objects for no reason but
// arrival order. Every mutation of a vhost, user or permission, AND the wait
// for its verdict, therefore holds this lock keyed by instance_id, and all
// three resources share it: the guest serialises across kinds, not per kind.
// Operations on different instances still run in parallel.
//
// It is process-wide, which is narrower than a Terraform run: Terraform starts
// a separate plugin process per provider CONFIGURATION, so resources under an
// aliased provider block do not share it, and neither do other clients (the
// portal, fm, a second Terraform run). Their overlap is absorbed by the
// server's own per-instance queue; the lock only stops this configuration from
// flooding that queue on its own.
// ---------------------------------------------------------------------------

var (
	locksMu sync.Mutex
	locks   = map[string]chan struct{}{}
)

// Lock takes the instance's mutation lock, waiting until it is free or ctx is
// done. The returned release must be called exactly once (defer it).
func Lock(ctx context.Context, instanceID string) (release func(), err error) {
	key := strings.ToLower(instanceID)
	locksMu.Lock()
	ch, ok := locks[key]
	if !ok {
		ch = make(chan struct{}, 1)
		locks[key] = ch
	}
	locksMu.Unlock()

	select {
	case ch <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-ch }) }, nil
	case <-ctx.Done():
		return func() {}, fmt.Errorf("waiting for another vhost, user or permission operation on messaging instance %s "+
			"to finish: %w", instanceID, ctx.Err())
	}
}

// AddLockError reports a lock wait that ended without the lock.
func AddLockError(diags *diag.Diagnostics, err error) {
	diags.AddError("Messaging object operation not started", err.Error()+
		". Operations on one instance run one at a time, so this one never reached the platform; apply again.")
}

// ---------------------------------------------------------------------------
// Waiting for a verdict.
// ---------------------------------------------------------------------------

// Wait polls get until the object leaves pending/deleting: it returns the last
// object read and gone=true when the object answered a service 404 (a finished
// delete). err is a timeout or a cancelled context; obj then holds the last
// object read, if any, so the caller can still record it.
func Wait[T any](ctx context.Context, interval, timeout time.Duration, name string,
	get func(ctx context.Context) (*T, error), status func(*T) string,
) (obj *T, gone bool, err error) {
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	var last *T
	var terminal error
	state, err := client.WaitForState(ctx, client.PollConfig{
		Interval:     interval,
		Timeout:      timeout,
		TargetStates: []string{StatusReady, StatusError, statusGone},
		ErrorStates:  []string{statusTerminal},
		ResourceName: name,
		PollFunc: func(pollCtx context.Context) (string, error) {
			got, err := get(pollCtx)
			if err != nil {
				// Only a SERVICE 404 is a verdict (client.IsNotFound requires
				// the flat envelope); a gateway misroute is retried like any
				// other transient error and never read as "deleted".
				if client.IsNotFound(err) {
					return statusGone, nil
				}
				// An error no retry can clear ends the wait at once: it runs
				// under the per-instance lock, so retrying a bad key to the
				// deadline would stall every object on the instance.
				if IsTerminal(err) {
					terminal = err
					return statusTerminal, nil
				}
				return "", err
			}
			last = got
			return status(got), nil
		},
	})
	if terminal != nil {
		return last, false, fmt.Errorf("reading the %s failed: %w", name, terminal)
	}
	return last, state == statusGone, err
}

// MismatchError is a response that names a different object than the one
// requested. It is never retried: the next read answers the same way.
type MismatchError struct{ Msg string }

func (e *MismatchError) Error() string { return e.Msg }

// Mismatch builds a MismatchError.
func Mismatch(format string, args ...any) error {
	return &MismatchError{Msg: fmt.Sprintf(format, args...)}
}

// IsTerminal reports whether a poll error cannot clear by retrying: an
// identity mismatch, or a 4xx refusal other than 404 (a gateway misroute is
// retried; a service 404 is handled before this), 408 and 429.
func IsTerminal(err error) bool {
	var mm *MismatchError
	if errors.As(err, &mm) {
		return true
	}
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.StatusCode {
	case http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	default:
		return apiErr.StatusCode >= 400 && apiErr.StatusCode < 500
	}
}

// Outcome is the verdict of a finished mutation, read off the object.
type Outcome int

const (
	// Succeeded: `ready` with no failureReason.
	Succeeded Outcome = iota
	// ChangeFailed: `ready` WITH a failureReason — the change failed and the
	// broker kept the object's previous state.
	ChangeFailed
	// Errored: `error` — a failed create; the object is absent or its state is
	// unknown.
	Errored
)

// Judge classifies a terminal object.
func Judge(status, failureReason string) Outcome {
	switch {
	case status == StatusError:
		return Errored
	case failureReason != "":
		return ChangeFailed
	default:
		return Succeeded
	}
}

// FailureText renders why an operation failed, stripping non-printable bytes
// from the server's text. The localised failureMessage leads; the stable code
// follows in parentheses.
func FailureText(reason, message string) string {
	reason, message = printable(reason), printable(message)
	switch {
	case message != "" && reason != "":
		return message + " (" + reason + ")"
	case message != "":
		return message
	case reason != "":
		return reason
	default:
		return "no reason was given"
	}
}

// BrokerSentence says what a failure did to the broker.
func BrokerSentence(reason string) string {
	if reason == FailureOutcomeUnknown {
		return "The platform could not confirm whether the broker applied the change."
	}
	return "The broker is unchanged."
}

func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, s)
}

// ---------------------------------------------------------------------------
// Refusals.
// ---------------------------------------------------------------------------

// HasCode reports whether err is an API refusal carrying code, in either case.
func HasCode(err error, code string) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && strings.EqualFold(apiErr.Code, code)
}

// AddRefusal turns a refused mutation into a diagnostic that names the
// Terraform-level remedy. summary is e.g. "Failed to create the messaging vhost".
func AddRefusal(diags *diag.Diagnostics, summary, subject string, err error) {
	var detail string
	switch {
	case HasCode(err, CodeCapabilityAbsent):
		detail = "This messaging instance cannot manage vhosts, users and permissions yet: it was built before " +
			"in-place credential rotation, or its agent has not been granted object management. Retrying will " +
			"not help — after a refusal the platform re-checks at most once an hour. Recreate the instance on a " +
			"current build, or contact support."
	case HasCode(err, CodeOperationInProgress):
		detail = subject + " already has an operation in flight (another client, or an earlier apply that was " +
			"interrupted). Wait for it to finish, then apply again."
	case HasCode(err, CodeRotationInProgress):
		detail = "A credential rotation is in flight on the messaging instance; vhost, user and permission " +
			"operations wait for it. Apply again once the rotation has finished."
	case HasCode(err, CodeObjectExists):
		detail = subject + " already exists on the instance (names are unique regardless of case). Import the " +
			"existing object under its exact spelling with `terraform import`, or choose another name. If it is left over from a failed create (status " +
			"`error`), delete it first — that delete is local when it never reached the broker."
	case HasCode(err, CodeObjectLimitReached):
		detail = "The instance holds the maximum number of objects of this kind (100 vhosts and 100 users per " +
			"instance). Delete one you no longer use, then apply again."
	case HasCode(err, CodeObjectNotReady):
		detail = "A permission needs both its user and its vhost `ready`. Reference the `frostmoln_messaging_user` " +
			"and `frostmoln_messaging_vhost` resources from the permission so Terraform creates them first, " +
			"or wait for them to finish and apply again."
	case HasCode(err, CodeInvalidState):
		detail = "The messaging instance is not running, so its objects cannot change. Apply again once it is running."
	}
	if detail == "" {
		diags.AddError(summary, err.Error())
		return
	}
	diags.AddError(summary, detail+"\n\nError: "+err.Error())
}

// Budgets resolves a resource's timeouts block against DefaultTimeout.
func Budgets(m *timeouts.Model) timeouts.Budgets {
	b, err := m.Resolve(timeouts.Uniform(DefaultTimeout))
	if err != nil {
		// Unreachable via HCL: the block's validator rejects a bad duration at
		// plan time.
		return timeouts.Uniform(DefaultTimeout)
	}
	return b
}

// InUseDetail names the grants that block a vhost's or a user's delete
// (409 OBJECT_IN_USE). list is the matching grant list
// (VhostPermissionsPath or UserPermissionsPath); other picks the far end of
// each grant (the user for a vhost, the vhost for a user). A failed list read
// still yields an actionable message, without the names.
func InUseDetail(ctx context.Context, c *client.Client, list, subject, otherNoun string, other func(Permission) string) string {
	base := subject + " still has permissions, so it cannot be deleted. Delete those " +
		"`frostmoln_messaging_permission` resources (or the grants, if Terraform does not manage them) first; " +
		"a permission that references this resource is destroyed before it automatically."
	resp, err := c.Get(ctx, list, nil)
	if err != nil {
		return base + "\n\nThe blocking grants could not be listed: " + err.Error()
	}
	grants, err := client.ParseResponse[PermissionList](resp)
	if err != nil {
		return base + "\n\nThe blocking grants could not be listed: " + err.Error()
	}
	names := make([]string, 0, len(grants.Permissions))
	for _, p := range grants.Permissions {
		names = append(names, fmt.Sprintf("%q", printable(other(p))))
	}
	if len(names) == 0 {
		return base + "\n\nNo grant is listed now: one was probably deleted meanwhile. Apply again."
	}
	return base + fmt.Sprintf("\n\nBlocking grants (%s): %s.", otherNoun, strings.Join(names, ", "))
}

// Optional renders an optional API string: null when absent, so an absent
// failure reason reads as "none" rather than as an empty code.
func Optional(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

// English marks ctx so every object call sends Accept-Language: en. The
// service localises failureMessage from that header (en/sv/no/fi); Terraform
// diagnostics are written in English, so the provider asks for English
// explicitly instead of inheriting whatever default the service has.
func English(ctx context.Context) context.Context {
	return client.WithAcceptLanguage(ctx, "en")
}

// InstanceIDValidators guard instance_id, which becomes a path segment: a "."
// or ".." would collapse the request path onto a different resource, and a
// "/" would split it.
func InstanceIDValidators() []validator.String {
	return []validator.String{
		stringvalidator.LengthAtLeast(1),
		stringvalidator.NoneOf(".", ".."),
		stringvalidator.RegexMatches(regexp.MustCompile(`^[^/]+$`), "must not contain \"/\""),
	}
}

// NameValidators enforce the server's ObjectName rule at plan time.
func NameValidators() []validator.String {
	return []validator.String{
		stringvalidator.RegexMatches(regexp.MustCompile(NamePattern), "must be "+NameRule),
	}
}

// ReplaceWhenErrored plans a replacement of an object whose state says
// `error` — a failed create: the object is absent or its state unknown. A
// tainted resource is replaced anyway; this covers one that is not (an import
// of an errored object, or `terraform untaint`), which would otherwise plan
// clean on top of an object that does not work. Terraform core keeps a
// requires-replace path only if its value changes, so `status` is planned
// unknown and named as the reason.
func ReplaceWhenErrored(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var status types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("status"), &status)...)
	if resp.Diagnostics.HasError() || status.ValueString() != StatusError {
		return
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("status"), types.StringUnknown())...)
	resp.RequiresReplace = append(resp.RequiresReplace, path.Root("status"))
}

// WarnIfErrored is Read's notice for an object whose create failed.
func WarnIfErrored(diags *diag.Diagnostics, subject, status, reason, message string) {
	if status != StatusError {
		return
	}
	diags.AddWarning("The messaging object is in error",
		subject+" is in `error`: its create failed ("+FailureText(reason, message)+"), so it is absent on the "+
			"broker or its state is unknown. The next apply deletes and re-creates it.")
}
