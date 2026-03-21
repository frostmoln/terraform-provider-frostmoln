// Package acctest provides shared helpers for acceptance tests.
package acctest

import (
	"context"
	"fmt"
	"os"
	"testing"

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
