package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guide URL is a hardcoded slug in a Go constant, and nothing else ties it
// to the file that produces the page. Renaming the template would silently
// orphan every description that cites it, so assert the two agree.
func TestStateSecretsGuideURLMatchesTheTemplate(t *testing.T) {
	slug := StateSecretsGuide[strings.LastIndex(StateSecretsGuide, "/")+1:]

	tmpl := filepath.Join("..", "..", "templates", "guides", slug+".md.tmpl")
	if _, err := os.Stat(tmpl); err != nil {
		t.Fatalf("StateSecretsGuide points at slug %q but %s does not exist: %v", slug, tmpl, err)
	}

	// tfplugindocs keys the registry's left-nav entry on page_title, which is
	// also the human name the older non-linked descriptions used.
	body, err := os.ReadFile(tmpl)
	if err != nil {
		t.Fatalf("reading %s: %v", tmpl, err)
	}
	if !strings.Contains(string(body), `page_title: "Secrets in Terraform state"`) {
		t.Errorf("%s lost its expected page_title; the guide link text no longer matches the page", tmpl)
	}
}
