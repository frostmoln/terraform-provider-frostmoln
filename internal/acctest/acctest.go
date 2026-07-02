// Package acctest provides shared helpers for acceptance tests.
package acctest

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/acctest"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
	"go.frostmoln.internal/terraform-provider-frostmoln/internal/provider"
)

// TestAccProtoV6ProviderFactories is the standard map of provider factories
// used by all acceptance tests.
var TestAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"frostmoln": providerserver.NewProtocol6WithError(provider.New("test")()),
}

// TestAccPreCheck validates that required environment variables are set.
func TestAccPreCheck(t *testing.T) {
	t.Helper()

	if os.Getenv("FROSTMOLN_API_ENDPOINT") == "" {
		t.Fatal("FROSTMOLN_API_ENDPOINT must be set for acceptance tests")
	}
	if os.Getenv("FROSTMOLN_API_KEY") == "" {
		t.Fatal("FROSTMOLN_API_KEY must be set for acceptance tests")
	}
}

// TestAccPreCheckKubernetes gates the managed-K8s acceptance tests. They boot
// real (billed) cluster VMs, so they require BOTH:
//
//  1. An explicit opt-in: FROSTMOLN_TEST_KUBERNETES=1. Without it they always
//     skip — the nightly CI acceptance run must never create a cluster by
//     accident (e.g. if the feature gate is reverted platform-side, a 403
//     probe alone would silently stop protecting it).
//  2. A tenant holding the `kubernetes` entitlement (ADR-0038 per-tenant
//     feature gate) and an API key with the kubernetes:read/kubernetes:write
//     scopes. The probe hits the tenant-scoped cluster list — the surface the
//     tests actually use — and skips loudly on 403.
func TestAccPreCheckKubernetes(t *testing.T) {
	t.Helper()
	TestAccPreCheck(t)

	if os.Getenv("FROSTMOLN_TEST_KUBERNETES") != "1" {
		t.Skip("SKIPPING managed-K8s acceptance test: set FROSTMOLN_TEST_KUBERNETES=1 to opt in " +
			"(these tests create real, billed Kubernetes clusters)")
	}

	c, err := TestClient()
	if err != nil {
		t.Fatalf("failed to build test client for the kubernetes entitlement probe: %s", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := c.Get(ctx, c.TenantPath("/kubernetes-clusters"), nil); err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
			t.Skipf("SKIPPING managed-K8s acceptance test: 403 from the cluster API — the tenant lacks "+
				"the `kubernetes` entitlement (ADR-0038) or the API key lacks the kubernetes:read/"+
				"kubernetes:write scopes: %s", err)
		}
		t.Fatalf("kubernetes cluster-list probe failed with an unexpected error: %s", err)
	}
}

// RandomName generates a unique name for acceptance test resources.
func RandomName(prefix string) string {
	return fmt.Sprintf("acctest-%s-%s", prefix, acctest.RandStringFromCharSet(8, acctest.CharSetAlphaNum))
}

// TestClient returns a configured API client using environment variables.
// Useful for out-of-band verification in acceptance tests.
func TestClient() (*client.Client, error) {
	endpoint := os.Getenv("FROSTMOLN_API_ENDPOINT")
	apiKey := os.Getenv("FROSTMOLN_API_KEY")
	if endpoint == "" || apiKey == "" {
		return nil, fmt.Errorf("FROSTMOLN_API_ENDPOINT and FROSTMOLN_API_KEY must be set")
	}

	c := client.NewClient(endpoint, apiKey)
	if err := c.Configure(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to configure test client: %w", err)
	}
	return c, nil
}

// CheckDestroyByTenantPath returns a TestCheckFunc that verifies a resource
// has been destroyed by checking for a 404 response on the tenant-scoped path.
func CheckDestroyByTenantPath(resourceType, subpath string) func(*terraform.State) error {
	return func(s *terraform.State) error {
		c, err := TestClient()
		if err != nil {
			return err
		}

		for _, rs := range s.RootModule().Resources {
			if rs.Type != resourceType {
				continue
			}

			_, err := c.Get(context.Background(), c.TenantPath(subpath+"/"+rs.Primary.ID), nil)
			if err == nil {
				return fmt.Errorf("resource %s (%s) still exists", resourceType, rs.Primary.ID)
			}
			if !client.IsNotFound(err) {
				return fmt.Errorf("unexpected error checking %s (%s): %s", resourceType, rs.Primary.ID, err)
			}
		}
		return nil
	}
}

// CheckDestroyByUserPath returns a TestCheckFunc that verifies a user-scoped
// resource has been destroyed.
func CheckDestroyByUserPath(resourceType, subpath string) func(*terraform.State) error {
	return func(s *terraform.State) error {
		c, err := TestClient()
		if err != nil {
			return err
		}

		for _, rs := range s.RootModule().Resources {
			if rs.Type != resourceType {
				continue
			}

			_, err := c.Get(context.Background(), c.UserPath(subpath+"/"+rs.Primary.ID), nil)
			if err == nil {
				return fmt.Errorf("resource %s (%s) still exists", resourceType, rs.Primary.ID)
			}
			if !client.IsNotFound(err) {
				return fmt.Errorf("unexpected error checking %s (%s): %s", resourceType, rs.Primary.ID, err)
			}
		}
		return nil
	}
}

// TestImageName returns the image name to use in acceptance tests.
func TestImageName() string {
	if v := os.Getenv("FROSTMOLN_TEST_IMAGE_NAME"); v != "" {
		return v
	}
	return "Ubuntu 24.04"
}

// TestFlavorName returns the flavor name to use in acceptance tests.
func TestFlavorName() string {
	if v := os.Getenv("FROSTMOLN_TEST_FLAVOR_NAME"); v != "" {
		return v
	}
	return "nl.small"
}
