package appgw_listener

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/scopedecl"
)

var (
	_ resource.Resource                   = &listenerResource{}
	_ resource.ResourceWithImportState    = &listenerResource{}
	_ resource.ResourceWithConfigure      = &listenerResource{}
	_ resource.ResourceWithValidateConfig = &listenerResource{}
)

// maxPortRangeSpan is the most ports one `tcp` listener may bind, and it
// mirrors appgw's domain.MaxPortRangeSpan.
//
// It is not an arbitrary round number and it is not a policy the provider could
// pick: `bind :A-B` is one directive and N SOCKETS, so the span is a
// tenant-settable multiplier on the appliance's file-descriptor budget. Mirrored
// here only so the refusal arrives at plan time instead of as a 400 partway
// through an apply that has already built the gateway; the server refuses it
// again either way.
const maxPortRangeSpan = 512

// maxConnectionCeiling mirrors appgw's domain.MaxConnectionCeiling. Unlike
// rateLimitRps and rateLimitBurst, which the server bounds at 1_000_000, this
// one has a platform ceiling of its own.
const maxConnectionCeiling = 200000

// inspectorPort mirrors appgw's domain.InspectorPort — the appliance's own
// loopback port, the sole member of its reserved set.
//
// Mirrored DELIBERATELY, against the general rule that this provider does not
// duplicate server-side lists. The reserved set is not a policy list that grows
// with the catalog; it is one port with a measured, fatal consequence. A listener
// span covering it renders `bind :9000`, HAProxy refuses to start, and EVERY
// OTHER LISTENER ON THAT GATEWAY goes down with it — so the cost of learning
// this at apply instead of at plan is an outage, not a retry. If the appliance
// ever reserves a second port this mirror goes stale in the SAFE direction:
// the server still refuses, and the practitioner gets the server's message
// instead of ours.
const inspectorPort = 9000

type listenerResource struct {
	client *client.Client
}

// NewResource returns a new Application Gateway listener resource factory.
func NewResource() resource.Resource {
	return &listenerResource{}
}

func (r *listenerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_appgw_listener"
}

// replaceOnChange is every listener attribute's plan modifier set.
//
// 🔴 THE LISTENER API HAS NO UPDATE. The server registers POST, GET and DELETE
// on listeners and nothing else, so there is no in-place change to make and
// every attribute forces a replacement. Marking one of these mutable would
// produce a plan Terraform cannot execute.
func (r *listenerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replaceStr := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	replaceInt := []planmodifier.Int64{int64planmodifier.RequiresReplace()}
	replaceList := []planmodifier.List{listplanmodifier.RequiresReplace()}
	replaceBool := []planmodifier.Bool{boolplanmodifier.RequiresReplace()}

	resp.Schema = schema.Schema{
		Description: "Manages a listener on a Frostmoln Application Gateway: one bound port — or, on " +
			"a `tcp` listener, a range of them — with its TLS settings and its network firewall.\n\n" +
			"There are two shapes, and they are not interchangeable:\n\n" +
			"* **`http` / `https`** are inspected, routed and (for `https`) TLS-terminated. Routes " +
			"hang off the listener and each route names the backend pool it forwards to.\n" +
			"* **`tcp`** forwards bytes at layer 4. It has **no routes**, so it names its one " +
			"`backend_pool_id` directly; it terminates no TLS and is not inspected by the WAF. The " +
			"source-CIDR, geo, rate-limit and connection controls all still apply — that is what " +
			"makes it worth having over a plain load balancer.\n\n" +
			"The listener API has no update operation, so **every** attribute forces a new resource.\n\n" +
			"A listener is authored, not live: its ROUTING starts serving on the gateway's next " +
			"configuration apply. Its **port** is the one exception on this whole API: it is opened " +
			"on the gateway's public ingress when the listener is created and closed when it is " +
			"destroyed, without an apply. So replacing a listener on the same port leaves that port " +
			"closed between the destroy and the create, and traffic to it is dropped for that " +
			"window — `create_before_destroy` where you can.\n\n" +
			"Two listeners on one gateway may not overlap in port space; a second one claiming a " +
			"port an existing listener binds is refused with `LISTENER_PORT_IN_USE`. A few ports " +
			"belong to the gateway appliance itself and are refused for every protocol." +
			"\n\n" + scopedecl.Summary("frostmoln_appgw_listener"),
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "The unique identifier of the listener.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"gateway_id": schema.StringAttribute{
				Description:   "The Application Gateway this listener belongs to.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"name": schema.StringAttribute{
				Description:   "The name of the listener.",
				Required:      true,
				PlanModifiers: replaceStr,
			},
			"protocol": schema.StringAttribute{
				Description: "The listener protocol: `http`, `https` or `tcp`.\n\n" +
					"A `tcp` listener requires `backend_pool_id` and refuses certificates, " +
					"`tls_min_version`, `tls_cipher_profile`, `redirect_to_https`, a WAF policy and " +
					"any `frostmoln_appgw_route` beneath it — none of them exist at layer 4.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.OneOf("http", "https", "tcp")},
				PlanModifiers: replaceStr,
			},
			"port": schema.Int64Attribute{
				Description: "The port to bind, or the FIRST port of the range when " +
					"`port_range_end` is set.",
				Required:      true,
				Validators:    []validator.Int64{int64validator.Between(1, 65535)},
				PlanModifiers: replaceInt,
			},
			"port_range_end": schema.Int64Attribute{
				Description: "The last port of a range, **inclusive**. `tcp` listeners only; on an " +
					"`http` or `https` listener it is refused rather than ignored.\n\n" +
					"Omit it for a single port. It must be strictly greater than `port` and the span " +
					"may cover at most " + fmt.Sprint(maxPortRangeSpan) + " ports — the appliance " +
					"opens one socket per port in the range.\n\n" +
					"A ranged listener forwards each connection to the **same** port on the backend " +
					"that the client connected to, so `8000-8100` reaches `8000-8100` on your " +
					"servers. Those backends therefore have no fixed port to probe, and the pool's " +
					"`frostmoln_appgw_health_check` must set its own `port`.",
				// Optional and deliberately NOT Computed. The server reports
				// `portRangeEnd: null` for a single-port listener, which is the
				// same absence the configuration states by omitting it -- there
				// is nothing for the platform to choose, so there is nothing to
				// compute.
				//
				// 🔴 MARKED Computed, DELETING THIS LINE FROM A CONFIGURATION IS
				// A NO-OP. Terraform carries a Computed attribute's prior value
				// into the proposed new state when the configuration is null,
				// and the framework marks such an attribute unknown only when
				// the proposed state DIFFERS from the prior one -- so a config
				// that drops `port_range_end` plans EMPTY and the listener goes
				// on binding the whole range. Measured against this schema with
				// Computed added: the planned value came back as the old number,
				// not null (TestAccListenerDroppingThePortRangeIsLegibleInThePlan).
				Optional:      true,
				Validators:    []validator.Int64{int64validator.Between(2, 65535)},
				PlanModifiers: replaceInt,
			},
			"backend_pool_id": schema.StringAttribute{
				Description: "The pool a `tcp` listener forwards to. **Required** on `tcp` and " +
					"refused on `http`/`https`, where each `frostmoln_appgw_route` names its own " +
					"pool.\n\n" +
					"The pool must be on this gateway, must have `protocol = \"http\"`, must not use " +
					"`session_affinity = \"cookie\"` (a cookie is an HTTP header the gateway never " +
					"writes at layer 4) and must not already be the target of an http route: the " +
					"gateway serves a pool in one protocol mode.",
				// Optional and deliberately NOT Computed, for the same reason as
				// port_range_end: an http/https listener has no pool of its own
				// and the server omits the field entirely. Nothing is chosen
				// platform-side, so there is nothing to compute -- and Computed
				// would carry a stale pool id forward through a config that
				// removed it, exactly as above.
				Optional:      true,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: replaceStr,
			},
			"default_certificate_id": schema.StringAttribute{
				Description:   "The certificate served when no SNI matches. `https` listeners only.",
				Optional:      true,
				Validators:    []validator.String{stringvalidator.LengthAtLeast(1)},
				PlanModifiers: replaceStr,
			},
			"sni_certificate_ids": schema.ListAttribute{
				Description: "Additional certificates selected by SNI. `https` listeners only.",
				Optional:    true,
				ElementType: types.StringType,
				// SizeAtLeast(1): the server omits an empty collection entirely, so
				// `= []` plans as empty and applies as null — a hard "inconsistent
				// result after apply". Omit the attribute instead of writing an
				// empty one; they mean the same thing here.
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: replaceList,
			},
			"tls_min_version": schema.StringAttribute{
				Description: "The minimum TLS version accepted: `1.2` or `1.3`. `https` listeners " +
					"only — a `tcp` listener terminates no TLS and reports none, so this reads null " +
					"on one.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("1.2", "1.3")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"tls_cipher_profile": schema.StringAttribute{
				Description: "The cipher profile: `modern` or `intermediate`. `https` listeners " +
					"only — a `tcp` listener terminates no TLS and reports none, so this reads null " +
					"on one.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("modern", "intermediate")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"redirect_to_https": schema.BoolAttribute{
				Description:   "Redirect requests to the https listener instead of serving them.",
				Optional:      true,
				Computed:      true,
				Default:       booldefault.StaticBool(false),
				PlanModifiers: replaceBool,
			},
			"allowed_cidrs": schema.ListAttribute{
				Description: "Source CIDRs allowed to reach this listener. Omit to allow all sources.",
				Optional:    true,
				ElementType: types.StringType,
				// SizeAtLeast(1): the server omits an empty collection entirely, so
				// `= []` plans as empty and applies as null — a hard "inconsistent
				// result after apply". Omit the attribute instead of writing an
				// empty one; they mean the same thing here.
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: replaceList,
			},
			"denied_cidrs": schema.ListAttribute{
				Description: "Source CIDRs refused by this listener.",
				Optional:    true,
				ElementType: types.StringType,
				// SizeAtLeast(1): the server omits an empty collection entirely, so
				// `= []` plans as empty and applies as null — a hard "inconsistent
				// result after apply". Omit the attribute instead of writing an
				// empty one; they mean the same thing here.
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: replaceList,
			},
			"geo_block_mode": schema.StringAttribute{
				Description: "Country filtering: `off`, `allow` (only `geo_countries`) or `deny` " +
					"(everything except `geo_countries`).",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("off", "allow", "deny")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplace()},
			},
			"geo_countries": schema.ListAttribute{
				Description:   "ISO 3166-1 alpha-2 country codes. Required when `geo_block_mode` is `allow` or `deny`.",
				Optional:      true,
				ElementType:   types.StringType,
				Validators:    []validator.List{listvalidator.SizeAtLeast(1)},
				PlanModifiers: replaceList,
			},
			"rate_limit_rps": schema.Int64Attribute{
				Description: "Sustained rate permitted per source address.\n\n" +
					"~> **The unit depends on `protocol`.** On `http`/`https` it counts **requests** " +
					"per second; on `tcp` it counts **connections** per second, because layer 4 has " +
					"no request to count. For a protocol where one connection carries a whole " +
					"session — SMTP, IMAP, MQTT — the same number is a far tighter limit than it is " +
					"for HTTP, so set it against the connections you expect rather than reusing an " +
					"HTTP figure.",
				Optional: true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.Between(1, 1000000)},
				PlanModifiers: replaceInt,
			},
			"rate_limit_burst": schema.Int64Attribute{
				Description: "Ceiling inside any one second, above `rate_limit_rps`. Same unit as " +
					"`rate_limit_rps`: requests on `http`/`https`, connections on `tcp`.",
				Optional: true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				Validators:    []validator.Int64{int64validator.Between(1, 1000000)},
				PlanModifiers: replaceInt,
			},
			"max_connections": schema.Int64Attribute{
				// Names the server-side ceiling, because this is the field a
				// practitioner sets and the flavor's is the one that refuses it.
				// The bound below is a SHAPE check, not that ceiling: the real
				// limit is per-flavor and only the server knows it.
				Description: "Maximum concurrent connections on this listener. Must not exceed the " +
					"flavor's `max_concurrent_connections`, which is enforced server-side — a " +
					"larger value is refused at create with `FLAVOR_LIMIT_EXCEEDED` naming the " +
					"flavor.",
				Optional: true,
				// AtLeast(1), not 0: the server treats 0 as absent — either by
				// `omitempty` on the wire or by coercing it to a default — so a
				// configured 0 comes back as something else and the apply fails
				// with "inconsistent result after apply". Refusing it at plan
				// time is both cheaper and truthful.
				// The ceiling is maxConnectionCeiling, NOT the 1_000_000 its two
				// rate-limit neighbours use. The server bounds this field
				// against its own platform constant, and above it the renderer
				// clamps anyway — so a larger stored value could only ever be
				// an inert number the customer believes is in force.
				Validators:    []validator.Int64{int64validator.Between(1, maxConnectionCeiling)},
				PlanModifiers: replaceInt,
			},
			"waf_policy_id": schema.StringAttribute{
				Description: "The WAF policy applied to this listener, if any — an `overlay`-scoped " +
					"policy. Read-only here: attach one with " +
					"`frostmoln_appgw_waf_policy_attachment`, which is where the attachment's " +
					"lifecycle lives.",
				Computed: true,
			},
			"enabled": schema.BoolAttribute{
				Description: "Whether the listener is serving.",
				Computed:    true,
			},
			"created_at": schema.StringAttribute{
				Description:   "The creation timestamp.",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Description: "The last update timestamp.",
				Computed:    true,
			},
		},
	}
}

// ValidateConfig mirrors the server's cross-field rules at plan time, so a
// mistake is caught before anything is created rather than as a 400 halfway
// through an apply that has already built the gateway.
//
// The listener is where that matters most on this whole API: creating one OPENS
// A PUBLIC PORT immediately, so an apply that gets several listeners in and
// then fails on the last has already changed what the internet can reach.
func (r *listenerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg ListenerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if !cfg.Protocol.IsUnknown() && cfg.Protocol.ValueString() != "https" {
		hasDefault := !cfg.DefaultCertificateID.IsNull() && !cfg.DefaultCertificateID.IsUnknown()
		// Element COUNT, not null-ness: an empty list is not "certificates are
		// set", and the server checks len() > 0 too.
		hasSNI := !cfg.SNICertificateIDs.IsNull() && !cfg.SNICertificateIDs.IsUnknown() &&
			len(cfg.SNICertificateIDs.Elements()) > 0
		if hasDefault || hasSNI {
			resp.Diagnostics.AddAttributeError(path.Root("default_certificate_id"),
				"Certificates Require protocol = \"https\"",
				"An http listener terminates no TLS, so a certificate on it would never be served. "+
					"Set protocol = \"https\", or move the certificate to the https listener.")
		}
	}

	validateProtocolShape(&cfg, resp)

	if !cfg.GeoBlockMode.IsUnknown() {
		mode := cfg.GeoBlockMode.ValueString()
		if mode == "allow" || mode == "deny" {
			if cfg.GeoCountries.IsNull() {
				resp.Diagnostics.AddAttributeError(path.Root("geo_countries"),
					fmt.Sprintf("geo_countries Is Required With geo_block_mode = %q", mode),
					fmt.Sprintf("geo_block_mode = %q needs the list of countries it applies to. "+
						"With an empty list the filter would %s every request.", mode,
						map[string]string{"allow": "refuse", "deny": "allow"}[mode]))
			}
		}
	}
}

// validateProtocolShape is the http/https-versus-tcp split, stated once.
//
// 🔴 THE TWO SHAPES ARE MUTUALLY EXCLUSIVE, NOT A SUPERSET AND A SUBSET. A tcp
// listener REQUIRES backend_pool_id and REFUSES the TLS settings; an http or
// https listener is the exact opposite on both. So each rule below is checked
// in both directions -- "missing on tcp" and "present on L7" -- because a guard
// that only refuses the surplus lets the missing case through to a 400.
func validateProtocolShape(cfg *ListenerModel, resp *resource.ValidateConfigResponse) {
	if cfg.Protocol.IsUnknown() || cfg.Protocol.IsNull() {
		return
	}
	isTCP := cfg.Protocol.ValueString() == "tcp"

	// backend_pool_id: required on tcp, refused on http/https.
	hasPool := !cfg.BackendPoolID.IsNull()
	switch {
	case isTCP && !hasPool:
		resp.Diagnostics.AddAttributeError(path.Root("backend_pool_id"),
			"backend_pool_id Is Required With protocol = \"tcp\"",
			"A tcp listener has no routes -- routing needs host, path and header matching, which "+
				"are bytes the gateway does not parse at layer 4 -- so the listener names the one "+
				"backend pool it forwards to. Set backend_pool_id, or use protocol = \"http\" and "+
				"give the listener a frostmoln_appgw_route.")
	case !isTCP && hasPool:
		resp.Diagnostics.AddAttributeError(path.Root("backend_pool_id"),
			"backend_pool_id Requires protocol = \"tcp\"",
			"On an http or https listener each frostmoln_appgw_route names the backend pool it "+
				"forwards to, so the listener itself has none. Remove backend_pool_id and add a "+
				"route, or set protocol = \"tcp\".")
	}

	// port_range_end: tcp only, strictly above port, span-capped.
	if !cfg.PortRangeEnd.IsNull() && !cfg.PortRangeEnd.IsUnknown() {
		end := cfg.PortRangeEnd.ValueInt64()
		at := path.Root("port_range_end")
		switch {
		case !isTCP:
			resp.Diagnostics.AddAttributeError(at,
				"port_range_end Requires protocol = \"tcp\"",
				"An http or https listener binds exactly one port. Remove port_range_end, or set "+
					"protocol = \"tcp\" to forward a range at layer 4.")
		case cfg.Port.IsNull() || cfg.Port.IsUnknown():
			// The range cannot be judged without its start; the server will.
		case end <= cfg.Port.ValueInt64():
			resp.Diagnostics.AddAttributeError(at,
				"port_range_end Must Be Greater Than port",
				fmt.Sprintf("port_range_end = %d is not above port = %d. The range is INCLUSIVE of "+
					"both ends, and a single port is spelled by omitting port_range_end entirely "+
					"rather than by repeating port.", end, cfg.Port.ValueInt64()))
		default:
			if span := end - cfg.Port.ValueInt64() + 1; span > maxPortRangeSpan {
				resp.Diagnostics.AddAttributeError(at,
					"port_range_end Spans Too Many Ports",
					fmt.Sprintf("%d-%d spans %d ports and the gateway allows at most %d: the "+
						"appliance opens one listening socket per port in the range. Use separate "+
						"listeners for the ports you actually serve.",
						cfg.Port.ValueInt64(), end, span, maxPortRangeSpan))
			}
		}
	}

	// 🔴 THE ONE PORT THAT TAKES THE WHOLE GATEWAY DOWN, NOT JUST THIS LISTENER.
	//
	// The appliance binds the inspector on loopback:9000. A listener whose span
	// covers it renders `bind :9000`, HAProxy refuses to start, and every other
	// listener on that gateway stops with it. The server refuses this for EVERY
	// protocol, single-port and ranged alike — it is the one listener rule whose
	// apply-time cost is an outage rather than a retry, which is why it is worth
	// mirroring when the other server-side lists are not.
	if !cfg.Port.IsNull() && !cfg.Port.IsUnknown() {
		start := cfg.Port.ValueInt64()
		end := start
		if !cfg.PortRangeEnd.IsNull() && !cfg.PortRangeEnd.IsUnknown() {
			if e := cfg.PortRangeEnd.ValueInt64(); e > end {
				end = e
			}
		}
		if start <= inspectorPort && inspectorPort <= end {
			at := path.Root("port")
			if end != start {
				at = path.Root("port_range_end")
			}
			resp.Diagnostics.AddAttributeError(at,
				fmt.Sprintf("Port %d Is Reserved By The Gateway Appliance", inspectorPort),
				fmt.Sprintf("The span %d-%d includes port %d, which the appliance binds for "+
					"itself. It cannot be served to your traffic, and a gateway configured to "+
					"bind it does not start — taking every other listener on the gateway with "+
					"it. Choose a span that does not include %d.",
					start, end, inspectorPort, inspectorPort))
		}
	}

	if !isTCP {
		return
	}

	// The TLS settings and the redirect. A tcp listener negotiates nothing and
	// sends no HTTP response, so the server refuses all three outright.
	// Certificates are already refused above, by the rule that gates them on
	// protocol = "https".
	for _, f := range []struct {
		name string
		set  bool
	}{
		{"tls_min_version", !cfg.TLSMinVersion.IsNull() && !cfg.TLSMinVersion.IsUnknown()},
		{"tls_cipher_profile", !cfg.TLSCipherProfile.IsNull() && !cfg.TLSCipherProfile.IsUnknown()},
	} {
		if f.set {
			resp.Diagnostics.AddAttributeError(path.Root(f.name),
				f.name+" Requires an https Listener",
				"A tcp listener does not terminate TLS -- the handshake stays end to end between "+
					"the client and your backend -- so it negotiates nothing and "+f.name+" would "+
					"never be applied. Remove it.")
		}
	}
	if !cfg.RedirectToHTTPS.IsNull() && !cfg.RedirectToHTTPS.IsUnknown() && cfg.RedirectToHTTPS.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("redirect_to_https"),
			"redirect_to_https Requires an http Listener",
			"A redirect is an HTTP response, and a tcp listener sends none: it forwards bytes. "+
				"Remove redirect_to_https, or put the redirect on an http listener.")
	}
}

func (r *listenerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected Provider Data",
			fmt.Sprintf("Expected *client.Client, got %T", req.ProviderData))
		return
	}
	r.client = c
}

func (r *listenerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan ListenerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	createReq := plan.toCreateRequest(ctx, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	gwID := plan.GatewayID.ValueString()
	apiResp, err := r.client.Post(ctx,
		r.client.TenantPath(fmt.Sprintf("/application-gateways/%s/listeners", gwID)), createReq)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Create Listener", err.Error())
		return
	}

	// 202 here carries the listener itself, not an operation envelope: the row
	// is written synchronously and 202 says only that it is not yet SERVING.
	listener, err := client.ParseResponse[apiListener](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
		return
	}
	plan.fromAPI(ctx, listener, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *listenerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state ListenerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	apiResp, err := r.client.Get(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/listeners/%s", state.GatewayID.ValueString(), state.ID.ValueString(),
	)), nil)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Failed to Read Listener", err.Error())
		return
	}
	listener, err := client.ParseResponse[apiListener](apiResp)
	if err != nil {
		resp.Diagnostics.AddError("Failed to Parse Listener Response", err.Error())
		return
	}
	state.fromAPI(ctx, listener, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update cannot be reached: every attribute carries RequiresReplace. It exists
// because the Resource interface requires it, and it reports rather than
// silently doing nothing -- a no-op Update is how a provider tells Terraform a
// change succeeded when it did not happen.
func (r *listenerResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Listeners Cannot Be Updated In Place",
		"The Application Gateway API has no listener update operation, so every attribute of this "+
			"resource forces a replacement. Reaching this code means an attribute was added to the "+
			"schema without RequiresReplace.")
}

func (r *listenerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state ListenerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Delete(ctx, r.client.TenantPath(fmt.Sprintf(
		"/application-gateways/%s/listeners/%s", state.GatewayID.ValueString(), state.ID.ValueString(),
	)))
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to Delete Listener", err.Error())
	}
}

func (r *listenerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts, err := client.ParseImportID(req.ID, "gateway_id", "listener_id")
	if err != nil {
		resp.Diagnostics.AddError("Invalid Import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("gateway_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), parts[1])...)
}
