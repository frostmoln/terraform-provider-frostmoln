package image

import (
	"encoding/json"
	"testing"
)

// Customer tags are read from `resourceTags`. The response's `tags` is the
// deprecated string-list rendering of the same tags and must never fail the
// decode or be read as the tags.
func TestImageTagsDecode(t *testing.T) {
	var img apiImage
	body := `{"tags":["env=prod","golden"],"resourceTags":{"env":"prod","golden":""}}`
	if err := json.Unmarshal([]byte(body), &img); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(img.ResourceTags) != 2 || img.ResourceTags["env"] != "prod" || img.ResourceTags["golden"] != "" {
		t.Errorf("resourceTags = %v", img.ResourceTags)
	}
	b, err := json.Marshal(apiUpdateImageRequest{Tags: &map[string]string{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"resourceTags":{}}` {
		t.Errorf("a clear sends %s, want resourceTags {}", b)
	}
}
