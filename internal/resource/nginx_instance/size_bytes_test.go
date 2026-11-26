package nginx_instance

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// TestFromAPISizeBytes pins size_bytes to the API's sizeBytes (94 GiB, what a
// 100 GB request allocates), and null when the API sends none.
func TestFromAPISizeBytes(t *testing.T) {
	var m NginxInstanceModel
	var diags diag.Diagnostics
	m.fromAPI(context.Background(), &apiWebserverInstance{ID: "w-1", SizeBytes: 100931731456}, &diags)
	if m.SizeBytes.ValueInt64() != 100931731456 {
		t.Errorf("size_bytes = %v, want 100931731456 from sizeBytes", m.SizeBytes)
	}
	m = NginxInstanceModel{}
	m.fromAPI(context.Background(), &apiWebserverInstance{ID: "w-1"}, &diags)
	if !m.SizeBytes.IsNull() {
		t.Errorf("size_bytes = %v, want null when the API sends no sizeBytes", m.SizeBytes)
	}
}
