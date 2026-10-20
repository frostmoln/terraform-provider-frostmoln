// Package cabundle implements the frostmoln_ca_bundle Terraform data source: the
// platform CA bundle that signs every managed instance's TLS certificate
// (MANAGED-OFFER-TLS-AND-INTERNAL-DNS-PLAN §5.3).
package cabundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// caBundlePath is not tenant-scoped: the bundle is the same public material for
// every authenticated principal.
const caBundlePath = "/v1/pki/ca-bundle"

// errCodeUnavailable is the API error code for "the platform has no CA bundle yet"
// (HTTP 503).
const errCodeUnavailable = "CA_BUNDLE_UNAVAILABLE"

var _ datasource.DataSource = &caBundleDataSource{}

// NewDataSource returns a new frostmoln_ca_bundle data source factory.
func NewDataSource() datasource.DataSource {
	return &caBundleDataSource{}
}

type caBundleDataSource struct {
	client *client.Client
}

type caBundleModel struct {
	ExpectedFingerprints types.Set    `tfsdk:"expected_fingerprints"`
	PEM                  types.String `tfsdk:"pem"`
	Certificates         types.List   `tfsdk:"certificates"`
	Fingerprints         types.List   `tfsdk:"fingerprints"`
}

type certificateModel struct {
	Subject           types.String `tfsdk:"subject"`
	FingerprintSHA256 types.String `tfsdk:"fingerprint_sha256"`
	NotBefore         types.String `tfsdk:"not_before"`
	NotAfter          types.String `tfsdk:"not_after"`
}

var certificateAttrTypes = map[string]attr.Type{
	"subject":            types.StringType,
	"fingerprint_sha256": types.StringType,
	"not_before":         types.StringType,
	"not_after":          types.StringType,
}

// apiCABundle is the JSON form of GET /v1/pki/ca-bundle.
type apiCABundle struct {
	PEM          string           `json:"pem"`
	Certificates []apiCertificate `json:"certificates"`
}

type apiCertificate struct {
	Subject           string `json:"subject"`
	FingerprintSHA256 string `json:"fingerprintSha256"`
	NotBefore         string `json:"notBefore"`
	NotAfter          string `json:"notAfter"`
}

func (d *caBundleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ca_bundle"
}

func (d *caBundleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Frostmoln customer CA bundle: the root certificate(s) that sign every managed instance's TLS certificate (databases, caches, message brokers, webservers). Trust the whole `pem` in a client to connect with full certificate and hostname verification. During a root rollover the bundle holds both the current and the next root. Only self-signed CA certificates are accepted. Pin the bundle with `expected_fingerprints` (from the published documentation) to verify it.",
		Attributes: map[string]schema.Attribute{
			"expected_fingerprints": schema.SetAttribute{
				Description: "SHA-256 fingerprints the bundle must hold, taken from the Frostmoln documentation rather than from this API. When set, the bundle must hold exactly these certificates, or the read fails. Hex, any case, colons allowed. This is the integrity control: the provider's own check that the PEM matches the API's certificate list only catches an inconsistent server, since both come from the same response. Root rollover: a rollover first ADDS the next root to the bundle, and its fingerprint is published on the documentation page in advance, so a pinned configuration fails until this set is updated to the newly published one (old and next root); when the old root is later removed, the published set changes again and the pin must be updated again. Update the pin whenever the published set changes.",
				ElementType: types.StringType,
				Optional:    true,
			},
			"fingerprints": schema.ListAttribute{
				Description: "The SHA-256 fingerprints of the certificates, as lowercase hex, in the same order as `certificates`.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"pem": schema.StringAttribute{
				Description: "The CA bundle as PEM text, re-encoded from the parsed certificates: one CERTIFICATE block per self-signed CA certificate.",
				Computed:    true,
			},
			"certificates": schema.ListNestedAttribute{
				Description: "The certificates in the bundle.",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"subject": schema.StringAttribute{
							Description: "The certificate subject, read from the certificate.",
							Computed:    true,
						},
						"fingerprint_sha256": schema.StringAttribute{
							Description: "The SHA-256 fingerprint of the certificate (DER), as lowercase hex.",
							Computed:    true,
						},
						"not_before": schema.StringAttribute{
							Description: "Start of the certificate's validity (RFC 3339).",
							Computed:    true,
						},
						"not_after": schema.StringAttribute{
							Description: "End of the certificate's validity (RFC 3339).",
							Computed:    true,
						},
					},
				},
			},
		},
	}
}

func (d *caBundleDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *caBundleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg caBundleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var want []string
	if !cfg.ExpectedFingerprints.IsNull() && !cfg.ExpectedFingerprints.IsUnknown() {
		var raw []string
		resp.Diagnostics.Append(cfg.ExpectedFingerprints.ElementsAs(ctx, &raw, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		for _, r := range raw {
			fp, err := normalizeFingerprint(r)
			if err != nil {
				resp.Diagnostics.AddAttributeError(path.Root("expected_fingerprints"), "Invalid expected fingerprint", err.Error())
				return
			}
			want = append(want, fp)
		}
	}

	apiResp, err := d.client.Get(ctx, caBundlePath, nil)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code == errCodeUnavailable {
			resp.Diagnostics.AddError(
				"Platform CA bundle not available yet",
				"The Frostmoln platform has not published a CA bundle yet ("+errCodeUnavailable+"). Try again later.",
			)
			return
		}
		resp.Diagnostics.AddError("Failed to read the CA bundle", err.Error())
		return
	}

	var bundle apiCABundle
	if err := json.Unmarshal(apiResp.Body, &bundle); err != nil {
		resp.Diagnostics.AddError("Failed to parse the CA bundle response", err.Error())
		return
	}

	certs, err := parseBundle(bundle.PEM)
	if err != nil {
		resp.Diagnostics.AddError("Invalid CA bundle", err.Error())
		return
	}
	computed := make([]string, 0, len(certs))
	for _, c := range certs {
		computed = append(computed, fingerprint(c))
	}

	// Server consistency only, NOT integrity: the PEM and the list come from the
	// same response. Integrity is expected_fingerprints below.
	listed := make([]string, 0, len(bundle.Certificates))
	for _, c := range bundle.Certificates {
		listed = append(listed, strings.ToLower(c.FingerprintSHA256))
	}
	if !sameList(computed, listed) {
		resp.Diagnostics.AddError("Invalid CA bundle",
			fmt.Sprintf("The PEM returned by the API holds certificates %v but the response lists %v.", computed, listed))
		return
	}
	if len(want) > 0 && !sameSet(computed, want) {
		resp.Diagnostics.AddAttributeError(path.Root("expected_fingerprints"), "CA bundle does not match expected_fingerprints",
			fmt.Sprintf("The platform CA bundle holds %v, but expected_fingerprints is %v. Compare them with the fingerprints published in the Frostmoln documentation.", computed, want))
		return
	}

	// An expired root still goes into the bundle (the server decides what it
	// holds), but the practitioner hears about it.
	now := time.Now()
	for _, c := range certs {
		if now.After(c.NotAfter) {
			resp.Diagnostics.AddWarning("CA certificate expired",
				fmt.Sprintf("CA certificate %s (SHA-256 %s) in the platform CA bundle expired on %s; clients will not accept certificates that chain to it.",
					c.Subject.String(), fingerprint(c), c.NotAfter.UTC().Format(time.RFC3339)))
		}
	}

	// Everything exposed is derived from the parsed certificates; nothing the
	// server sent as text reaches state.
	var pemBuf bytes.Buffer
	items := make([]certificateModel, 0, len(certs))
	for _, c := range certs {
		_ = pem.Encode(&pemBuf, &pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})
		items = append(items, certificateModel{
			Subject:           types.StringValue(c.Subject.String()),
			FingerprintSHA256: types.StringValue(fingerprint(c)),
			NotBefore:         types.StringValue(c.NotBefore.UTC().Format(time.RFC3339)),
			NotAfter:          types.StringValue(c.NotAfter.UTC().Format(time.RFC3339)),
		})
	}
	certList, diags := types.ListValueFrom(ctx, types.ObjectType{AttrTypes: certificateAttrTypes}, items)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	fpList, diags := types.ListValueFrom(ctx, types.StringType, computed)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	state := caBundleModel{
		ExpectedFingerprints: cfg.ExpectedFingerprints,
		PEM:                  types.StringValue(pemBuf.String()),
		Certificates:         certList,
		Fingerprints:         fpList,
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// parseBundle parses a CA bundle strictly: only CERTIFICATE blocks without
// headers, separated by nothing but whitespace, each a parsable, self-signed CA
// certificate. At least one certificate is required.
func parseBundle(pemText string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(pemText)
	for {
		rest = bytes.TrimLeft(rest, " \t\r\n")
		if len(rest) == 0 {
			break
		}
		// pem.Decode silently skips text before a block, so stray text is refused
		// here, before it gets the chance.
		if !bytes.HasPrefix(rest, []byte("-----BEGIN ")) {
			return nil, errors.New("the CA bundle returned by the API contains text outside a PEM block")
		}
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, errors.New("the CA bundle returned by the API contains a malformed PEM block")
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("the CA bundle returned by the API contains a %q block; only CERTIFICATE is allowed", block.Type)
		}
		if len(block.Headers) != 0 {
			return nil, errors.New("the CA bundle returned by the API contains a PEM block with headers")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("the CA bundle returned by the API contains an unparsable certificate: %w", err)
		}
		if !cert.BasicConstraintsValid || !cert.IsCA {
			return nil, fmt.Errorf("the CA bundle returned by the API contains certificate %s, which is not a CA", fingerprint(cert))
		}
		if err := cert.CheckSignatureFrom(cert); err != nil {
			return nil, fmt.Errorf("the CA bundle returned by the API contains certificate %s, which is not self-signed: %w", fingerprint(cert), err)
		}
		certs = append(certs, cert)
	}
	if len(certs) == 0 {
		return nil, errors.New("the CA bundle returned by the API contains no certificate")
	}
	return certs, nil
}

// normalizeFingerprint accepts a SHA-256 fingerprint as hex in any case, with or
// without ':' separators, and returns 64 lowercase hex characters.
func normalizeFingerprint(s string) (string, error) {
	fp := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), ":", ""))
	if _, err := hex.DecodeString(fp); err != nil || len(fp) != sha256.Size*2 {
		return "", fmt.Errorf("%q is not a SHA-256 fingerprint: want 64 hex characters", s)
	}
	return fp, nil
}

func fingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}

// sameList reports whether a and b hold the same values the same number of times,
// in any order.
func sameList(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

// sameSet reports whether a and b hold the same distinct values.
func sameSet(a, b []string) bool {
	return slices.Equal(slices.Compact(slices.Sorted(slices.Values(a))), slices.Compact(slices.Sorted(slices.Values(b))))
}
