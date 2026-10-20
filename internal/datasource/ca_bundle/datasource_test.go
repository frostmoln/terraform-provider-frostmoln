package cabundle

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

type testCA struct {
	pem  string
	fp   string
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// newCert issues a certificate for cn; parent == nil makes it self-signed.
func newCert(t *testing.T, cn string, isCA bool, parent *testCA) testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA: isCA, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	sum := sha256.Sum256(der)
	return testCA{string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), hex.EncodeToString(sum[:]), cert, key}
}

func testCert(t *testing.T, cn string) (string, string) {
	t.Helper()
	c := newCert(t, cn, true, nil)
	return c.pem, c.fp
}

// serve answers with pemText and a certificate list naming fps.
func serve(pemText string, fps ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		certs := make([]map[string]string, 0, len(fps))
		for _, fp := range fps {
			certs = append(certs, map[string]string{"subject": "server says", "fingerprintSha256": fp, "notBefore": "x", "notAfter": "y"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"pem": pemText, "certificates": certs})
	}
}

func TestMetadata(t *testing.T) {
	var resp datasource.MetadataResponse
	NewDataSource().Metadata(context.Background(), datasource.MetadataRequest{ProviderTypeName: "frostmoln"}, &resp)
	if resp.TypeName != "frostmoln_ca_bundle" {
		t.Errorf("type name = %q, want frostmoln_ca_bundle", resp.TypeName)
	}
}

func TestSchemaShape(t *testing.T) {
	var resp datasource.SchemaResponse
	NewDataSource().Schema(context.Background(), datasource.SchemaRequest{}, &resp)
	if a := resp.Schema.Attributes["expected_fingerprints"]; a == nil || !a.IsOptional() || a.IsComputed() || a.IsRequired() {
		t.Error("expected_fingerprints must be an optional, non-computed argument")
	}
	for _, name := range []string{"pem", "certificates", "fingerprints"} {
		a, ok := resp.Schema.Attributes[name]
		if !ok {
			t.Fatalf("missing attribute %q", name)
		}
		if !a.IsComputed() || a.IsRequired() || a.IsOptional() {
			t.Errorf("attribute %q must be computed only", name)
		}
	}
	if len(resp.Schema.Attributes) != 4 {
		t.Errorf("unexpected attributes: %v", resp.Schema.Attributes)
	}
}

func TestConfigureWrongType(t *testing.T) {
	ds := &caBundleDataSource{}
	var resp datasource.ConfigureResponse
	ds.Configure(context.Background(), datasource.ConfigureRequest{ProviderData: "not-a-client"}, &resp)
	if !resp.Diagnostics.HasError() {
		t.Error("expected an error for the wrong provider data type")
	}
}

// read runs one Read against handler and returns the diagnostics and state model.
func read(t *testing.T, handler http.HandlerFunc, expected ...string) (diag.Diagnostics, caBundleModel, []certificateModel) {
	t.Helper()
	var gotPath, gotAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAccept = r.URL.Path, r.Header.Get("Accept")
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	c := client.NewClient(srv.URL, "test-key") // pragma: allowlist secret
	ds := NewDataSource()
	var cfgResp datasource.ConfigureResponse
	ds.(datasource.DataSourceWithConfigure).Configure(context.Background(), datasource.ConfigureRequest{ProviderData: c}, &cfgResp)
	if cfgResp.Diagnostics.HasError() {
		t.Fatalf("configure: %v", cfgResp.Diagnostics)
	}

	ctx := context.Background()
	var schemaResp datasource.SchemaResponse
	ds.Schema(ctx, datasource.SchemaRequest{}, &schemaResp)
	s := schemaResp.Schema
	objType := s.Type().TerraformType(ctx).(tftypes.Object)
	expectedVal := tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, nil)
	if expected != nil {
		elems := make([]tftypes.Value, 0, len(expected))
		for _, e := range expected {
			elems = append(elems, tftypes.NewValue(tftypes.String, e))
		}
		expectedVal = tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, elems)
	}
	config := tftypes.NewValue(objType, map[string]tftypes.Value{
		"expected_fingerprints": expectedVal,
		"pem":                   tftypes.NewValue(tftypes.String, nil),
		"fingerprints":          tftypes.NewValue(tftypes.List{ElementType: tftypes.String}, nil),
		"certificates":          tftypes.NewValue(objType.AttributeTypes["certificates"], nil),
	})

	var readResp datasource.ReadResponse
	readResp.State = tfsdk.State{Schema: s, Raw: tftypes.NewValue(objType, nil)}
	ds.Read(ctx, datasource.ReadRequest{Config: tfsdk.Config{Schema: s, Raw: config}}, &readResp)

	if gotPath != "/v1/pki/ca-bundle" && !readResp.Diagnostics.HasError() {
		t.Errorf("path = %q, want /v1/pki/ca-bundle (not tenant-scoped)", gotPath)
	}
	if gotAccept != "application/json" && !readResp.Diagnostics.HasError() {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}

	var model caBundleModel
	var certs []certificateModel
	if !readResp.Diagnostics.HasError() {
		if d := readResp.State.Get(ctx, &model); d.HasError() {
			t.Fatalf("state.Get: %v", d)
		}
		if d := model.Certificates.ElementsAs(ctx, &certs, false); d.HasError() {
			t.Fatalf("certificates: %v", d)
		}
	}
	return readResp.Diagnostics, model, certs
}

func TestReadSetsPEMAndCertificates(t *testing.T) {
	pemA, fpA := testCert(t, "Frostmoln Customer Root A")
	pemB, fpB := testCert(t, "Frostmoln Customer Root B")
	diags, model, certs := read(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"pem": pemA + pemB,
			"certificates": []map[string]string{
				{"subject": "CN=Frostmoln Customer Root A", "fingerprintSha256": fpA, "notBefore": "2026-01-01T00:00:00Z", "notAfter": "2036-01-01T00:00:00Z"},
				{"subject": "CN=Frostmoln Customer Root B", "fingerprintSha256": fpB, "notBefore": "2026-06-01T00:00:00Z", "notAfter": "2036-06-01T00:00:00Z"},
			},
		})
	})
	if diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	if model.PEM.ValueString() != pemA+pemB {
		t.Errorf("pem = %q, want both certificates", model.PEM.ValueString())
	}
	if len(certs) != 2 {
		t.Fatalf("certificates = %d, want 2", len(certs))
	}
	got := certs[1]
	if got.Subject.ValueString() != "CN=Frostmoln Customer Root B" || got.FingerprintSHA256.ValueString() != fpB ||
		got.NotBefore.ValueString() != "2026-01-01T00:00:00Z" || got.NotAfter.ValueString() != "2036-01-01T00:00:00Z" {
		t.Errorf("unexpected second certificate: %+v", got)
	}
}

func TestReadUnavailableIsAClearDiagnostic(t *testing.T) {
	diags, _, _ := read(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"CA_BUNDLE_UNAVAILABLE","message":"no CA bundle has been published"}}`))
	})
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if got := diags.Errors()[0].Summary(); got != "Platform CA bundle not available yet" {
		t.Errorf("summary = %q", got)
	}
}

func TestReadOtherErrorsAreNotReportedAsUnavailable(t *testing.T) {
	diags, _, _ := read(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"code":"SERVICE_UNAVAILABLE","message":"down"}}`))
	})
	if !diags.HasError() {
		t.Fatal("expected an error diagnostic")
	}
	if got := diags.Errors()[0].Summary(); got != "Failed to read the CA bundle" {
		t.Errorf("summary = %q, want the generic read failure", got)
	}
}

func TestReadRefusesAnInconsistentBundle(t *testing.T) {
	pemA, _ := testCert(t, "A")
	_, fpOther := testCert(t, "other")
	cases := map[string]map[string]any{
		"fingerprint mismatch": {"pem": pemA, "certificates": []map[string]string{{"subject": "A", "fingerprintSha256": fpOther}}},
		"no certificate":       {"pem": "", "certificates": []map[string]string{}},
		"not listed":           {"pem": pemA, "certificates": []map[string]string{}},
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			diags, _, _ := read(t, func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(body)
			})
			if !diags.HasError() || !strings.Contains(diags.Errors()[0].Summary(), "Invalid CA bundle") {
				t.Errorf("expected an Invalid CA bundle error, got %v", diags)
			}
		})
	}
}

// pem and every certificate field are derived from the parsed certificates.
func TestReadReEncodesAndIgnoresServerText(t *testing.T) {
	a := newCert(t, "Root A", true, nil)
	messy := "\r\n\n  " + strings.ReplaceAll(a.pem, "\n", "\r\n") + "\n\n"
	diags, model, certs := read(t, serve(messy, strings.ToUpper(a.fp)))
	if diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	if model.PEM.ValueString() != a.pem {
		t.Errorf("pem = %q, want the canonical re-encoding", model.PEM.ValueString())
	}
	if len(certs) != 1 || certs[0].Subject.ValueString() != "CN=Root A" || certs[0].NotBefore.ValueString() != "2026-01-01T00:00:00Z" {
		t.Errorf("certificate fields not derived from the certificate: %+v", certs)
	}
}

func TestReadStrictParse(t *testing.T) {
	a := newCert(t, "A", true, nil)
	leaf := newCert(t, "leaf", false, nil)
	intermediate := newCert(t, "intermediate", true, &a)
	key := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")}))
	withHeader := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"X": "y"}, Bytes: a.cert.Raw}))
	garbage := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not der")}))
	cases := map[string]string{
		"text before":            "hello\n" + a.pem,
		"text between":           a.pem + "junk\n" + a.pem,
		"text after":             a.pem + "trailing",
		"private key block":      a.pem + key,
		"block with headers":     withHeader,
		"unparsable certificate": garbage,
		"not a CA":               leaf.pem,
		"CA but not self-signed": intermediate.pem,
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			diags, _, _ := read(t, serve(text, a.fp))
			if !diags.HasError() || diags.Errors()[0].Summary() != "Invalid CA bundle" {
				t.Errorf("expected an Invalid CA bundle error, got %v", diags)
			}
		})
	}
}

func TestReadExpectedFingerprints(t *testing.T) {
	a := newCert(t, "A", true, nil)
	b := newCert(t, "B", true, nil)
	c := newCert(t, "C", true, nil)
	upperColons := strings.ToUpper(a.fp[:2]) + ":" + strings.ToUpper(a.fp[2:])
	h := serve(a.pem+b.pem, a.fp, b.fp)

	if diags, _, certs := read(t, h, upperColons, b.fp); diags.HasError() || len(certs) != 2 {
		t.Fatalf("matching expected_fingerprints: %v", diags)
	}
	for name, exp := range map[string][]string{
		"one expected absent":            {a.fp, b.fp, c.fp},
		"bundle holds an unexpected one": {a.fp},
	} {
		t.Run(name, func(t *testing.T) {
			diags, _, _ := read(t, h, exp...)
			if !diags.HasError() || diags.Errors()[0].Summary() != "CA bundle does not match expected_fingerprints" {
				t.Errorf("expected a mismatch diagnostic, got %v", diags)
			}
		})
	}
}

func TestReadInvalidExpectedFingerprintIsRefusedBeforeTheRequest(t *testing.T) {
	called := false
	diags, _, _ := read(t, func(_ http.ResponseWriter, _ *http.Request) { called = true }, "nothex")
	if !diags.HasError() || diags.Errors()[0].Summary() != "Invalid expected fingerprint" {
		t.Errorf("expected an invalid-fingerprint diagnostic, got %v", diags)
	}
	if called {
		t.Error("a malformed expected fingerprint must be refused before any request")
	}
}

func TestReadFingerprintsFollowCertificateOrder(t *testing.T) {
	a := newCert(t, "A", true, nil)
	b := newCert(t, "B", true, nil)
	diags, model, certs := read(t, serve(b.pem+a.pem, strings.ToUpper(a.fp), b.fp))
	if diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	var fps []string
	if d := model.Fingerprints.ElementsAs(context.Background(), &fps, false); d.HasError() {
		t.Fatal(d)
	}
	if len(fps) != 2 || fps[0] != b.fp || fps[1] != a.fp {
		t.Errorf("fingerprints = %v, want [%s %s] (PEM order, lowercase)", fps, b.fp, a.fp)
	}
	for i, c := range certs {
		if c.FingerprintSHA256.ValueString() != fps[i] {
			t.Errorf("fingerprints[%d] does not match certificates[%d]", i, i)
		}
	}
}

func TestReadWarnsOnAnExpiredRootButSucceeds(t *testing.T) {
	valid := newCert(t, "Valid Root", true, nil)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "Old Root"},
		NotBefore: time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	expiredFP := hex.EncodeToString(sum[:])
	expiredPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))

	diags, model, _ := read(t, serve(valid.pem+expiredPEM, valid.fp, expiredFP))
	if diags.HasError() {
		t.Fatalf("an expired root must warn, not fail: %v", diags)
	}
	warnings := diags.Warnings()
	if len(warnings) != 1 || warnings[0].Summary() != "CA certificate expired" || !strings.Contains(warnings[0].Detail(), expiredFP) {
		t.Errorf("want one expiry warning naming the expired root, got %v", warnings)
	}
	if model.PEM.ValueString() != valid.pem+expiredPEM {
		t.Error("the bundle must still be exposed")
	}
}
