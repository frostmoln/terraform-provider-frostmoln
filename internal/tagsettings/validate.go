package tagsettings

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

// Plan-time checks for a colour rule. They are EXACTLY identity's
// ValidateTagColorRule (internal/domain/tag_settings.go) and never stricter: a
// colour rule must be able to target any tag that is legal on some resource, so
// it is NOT held to the default-tag character set — keys 1 to 255 characters
// and values 0 to 256, both free of control and formatting characters, the
// lower-case platform prefixes refused CASE-SENSITIVELY (`Frostmoln_Team` is an
// ordinary customer tag and can be coloured), and a `#RRGGBB` colour whose
// letter case is kept. Keep the two in lockstep; the constants and the
// display-safe rune rule are copied from that file.
//
// The one check that is not identity's is the SPELLING of an organization id:
// the server accepts any form uuid.Parse does (upper case, braces, urn:uuid:),
// but it reports ids in the canonical lowercase form, and Terraform compares
// strings — so a configured `{6F1C…}` would read as a different organization
// from the canonical id an import or a refresh records, and plan a replacement
// of the rule. It is refused with the canonical spelling in the message, the
// vpc_route destination precedent: a convergence rule, not an acceptance one.

// MaxKeyRunes / MaxValueRunes bound a colour rule's key and value, in
// characters (identity MaxTagColorKeyRunes / MaxTagColorValueRunes).
const (
	MaxKeyRunes   = 255
	MaxValueRunes = 256
)

// reservedKeyPrefixes is the platform namespace a colour rule may not target,
// compared case-sensitively (identity reservedTagColorKeyPrefixes).
var reservedKeyPrefixes = []string{"frostmoln_", "frostmoln-"}

// colorPattern is identity's tagColorRegex.
var colorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// isDisplaySafeRune is identity's rule: no control characters, no format
// characters (Cf — which includes the bidi overrides that reorder displayed
// text), no line or paragraph separators. Ordinary spaces are allowed.
func isDisplaySafeRune(r rune) bool {
	if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
		return false
	}
	return !unicode.In(r, unicode.Zl, unicode.Zp)
}

func displaySafe(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if !isDisplaySafeRune(r) {
			return false
		}
	}
	return true
}

// CheckKey returns why key cannot be a colour rule's key, or "" — in identity's
// order: length, then characters, then the reserved namespace.
func CheckKey(key string) string {
	if n := utf8.RuneCountInString(key); key == "" || n > MaxKeyRunes {
		return fmt.Sprintf("Tag key %q must be 1 to %d characters.", clip(key), MaxKeyRunes)
	}
	if !displaySafe(key) {
		return fmt.Sprintf("Tag key %q contains a control or formatting character.", clip(key))
	}
	for _, p := range reservedKeyPrefixes {
		if strings.HasPrefix(key, p) {
			return fmt.Sprintf("Tag key %q is reserved for the platform: keys starting with %q cannot be coloured. "+
				"(The check is case-sensitive, like the platform's own: a key such as %q can be.)",
				clip(key), p, "Frostmoln_Team")
		}
	}
	return ""
}

// CheckValue returns why value cannot be a colour rule's value, or "". The
// empty string is a legal value (it matches the empty tag value).
func CheckValue(value string) string {
	if utf8.RuneCountInString(value) > MaxValueRunes {
		return fmt.Sprintf("The value is too long (maximum %d characters).", MaxValueRunes)
	}
	if !displaySafe(value) {
		return "The value contains a control or formatting character."
	}
	return ""
}

// CheckColor returns why color is not a colour the platform accepts, or "".
func CheckColor(color string) string {
	if !colorPattern.MatchString(color) {
		return fmt.Sprintf("Color %q is not a hex colour: use the form #RRGGBB, e.g. \"#1f6feb\". Letter case is "+
			"kept as written.", clip(color))
	}
	return ""
}

// CheckOrganizationID returns why id cannot be an organization id, or "". The
// server parses the path segment with uuid.Parse, and so does this; a parseable
// id in any spelling but the canonical one is refused naming that spelling (see
// the note at the top of this file). It also keeps "." and ".." (which the
// client's path cleaning would collapse onto another route) from ever reaching
// a request path.
func CheckOrganizationID(id string) string {
	canonical, err := CanonicalUUID(id)
	if err != nil {
		return fmt.Sprintf("%q is not an organization id (a UUID, e.g. \"6f1c2a7e-0b8d-4e3a-9a51-2d4c8e7f1b30\").", clip(id))
	}
	if canonical != id {
		return fmt.Sprintf("Write the organization id as %q. The platform reports ids in that form, and Terraform "+
			"compares them as text, so %q would read as a different organization and plan a replacement.", canonical, clip(id))
	}
	return ""
}

// CanonicalUUID returns id in the canonical form the platform reports
// (lowercase, hyphenated, no braces or urn prefix), or uuid.Parse's error.
func CanonicalUUID(id string) (string, error) {
	u, err := uuid.Parse(id)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

// CheckRuleID returns why id cannot be a colour rule id, or "" — uuid.Parse,
// as the server parses the path segment.
func CheckRuleID(id string) string {
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Sprintf("%q is not a tag colour rule id (a UUID).", clip(id))
	}
	return ""
}

// clip bounds a value quoted back in a diagnostic.
func clip(s string) string {
	const max = 64
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max]) + "…"
}

// checkValidator adapts a Check* function to a framework string validator.
// Null and unknown values are not checked: null is the attribute's own
// business (Required/Optional), and an unknown value is checked on apply.
type checkValidator struct {
	summary     string
	description string
	check       func(string) string
}

func (v checkValidator) Description(_ context.Context) string { return v.description }

func (v checkValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }

func (v checkValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if problem := v.check(req.ConfigValue.ValueString()); problem != "" {
		resp.Diagnostics.AddAttributeError(req.Path, v.summary, problem)
	}
}

// KeyValidator validates a colour rule's key (CheckKey).
func KeyValidator() validator.String {
	return checkValidator{"Invalid Tag Key", "1 to 255 characters, no control or formatting characters, not starting with `frostmoln_` or `frostmoln-`", CheckKey}
}

// ValueValidator validates a colour rule's value (CheckValue).
func ValueValidator() validator.String {
	return checkValidator{"Invalid Tag Value", "at most 256 characters, no control or formatting characters", CheckValue}
}

// ColorValidator validates a colour (CheckColor).
func ColorValidator() validator.String {
	return checkValidator{"Invalid Color", "a `#RRGGBB` hex colour", CheckColor}
}

// OrganizationIDValidator validates an organization id (CheckOrganizationID).
func OrganizationIDValidator() validator.String {
	return checkValidator{"Invalid Organization ID", "an organization id (UUID)", CheckOrganizationID}
}
