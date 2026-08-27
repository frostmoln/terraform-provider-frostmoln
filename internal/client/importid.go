package client

import (
	"fmt"
	"strings"
)

// ParseImportID splits a composite Terraform import ID into exactly the
// segments a resource expects, refusing anything that would not round-trip.
//
// 🔴 AN IMPORT ID BECOMES A URL PATH SEGMENT, AND THE URL IS ASSEMBLED WITH
// path.Join, WHICH CLEANS DOT SEGMENTS. So an id whose last segment is ".."
// does not address a child — it addresses the PARENT, with the same method.
// For a DELETE that means destroying the parent, and neither url.PathEscape
// (which leaves "." and ".." untouched: they are unreserved, so there is
// nothing to escape) nor a well-formed-looking two-part split can prevent it.
// Refusing the value can.
//
// The segment COUNT is exact, not a minimum. A lenient SplitN folds every extra
// segment into the last one, so "a/b/c" parsed as two parts yields an id of
// "b/c" — which then builds a path with a slash in the middle of what should be
// one segment, silently addressing something else entirely.
//
// `kinds` names each segment, so the refusal tells the practitioner the format
// rather than only that theirs was wrong.
func ParseImportID(id string, kinds ...string) ([]string, error) {
	format := "{" + strings.Join(kinds, "}/{") + "}"
	parts := strings.Split(id, "/")
	if len(parts) != len(kinds) {
		return nil, fmt.Errorf("expected import ID format %s, got %q", format, id)
	}
	for i, p := range parts {
		switch p {
		case "":
			return nil, fmt.Errorf("the %s in the import ID is empty; expected %s, got %q",
				kinds[i], format, id)
		case ".", "..":
			return nil, fmt.Errorf("%q is not a valid %s: a dot segment collapses the request path "+
				"onto a different resource", p, kinds[i])
		}
	}
	return parts, nil
}
