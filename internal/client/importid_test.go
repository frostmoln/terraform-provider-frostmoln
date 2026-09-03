package client

import "testing"

func TestParseImportID(t *testing.T) {
	got, err := ParseImportID("agw-1/lsn-2", "gateway_id", "listener_id")
	if err != nil {
		t.Fatalf("a well-formed id was refused: %v", err)
	}
	if len(got) != 2 || got[0] != "agw-1" || got[1] != "lsn-2" {
		t.Fatalf("parsed %v", got)
	}
}

// TestParseImportIDRefusesDotSegments is the one that matters.
//
// ".." as the last segment makes path.Join address the PARENT with the same
// method, so an import followed by a destroy would delete the parent. Escaping
// cannot stop it: "." and ".." are unreserved.
func TestParseImportIDRefusesDotSegments(t *testing.T) {
	for _, id := range []string{"agw-1/..", "../lsn-1", "agw-1/.", "./."} {
		if _, err := ParseImportID(id, "gateway_id", "listener_id"); err == nil {
			t.Errorf("import ID %q was accepted; a dot segment collapses the request path", id)
		}
	}
}

// TestParseImportIDCountIsExact. A lenient split folds every extra segment into
// the last one, producing an id with a slash in it that addresses something
// else.
func TestParseImportIDCountIsExact(t *testing.T) {
	for _, id := range []string{"a/b/c", "a", "", "a/b/"} {
		if _, err := ParseImportID(id, "gateway_id", "listener_id"); err == nil {
			t.Errorf("import ID %q was accepted for a two-segment format", id)
		}
	}
}

// TestParseImportIDNamesTheFormat, so the refusal tells a practitioner what to
// type rather than only that they were wrong.
func TestParseImportIDNamesTheFormat(t *testing.T) {
	_, err := ParseImportID("nope", "gateway_id", "policy_id", "rule_key")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if want := "{gateway_id}/{policy_id}/{rule_key}"; !contains(err.Error(), want) {
		t.Fatalf("error %q does not name the format %q", err, want)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()
}
