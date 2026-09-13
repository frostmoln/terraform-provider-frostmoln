package tftags

import (
	"fmt"
	"regexp"
	"strings"
)

// The provider's default_tags are held to EXACTLY the rules identity enforces on
// a tenant's default tags (identity internal/domain/tag_settings.go,
// ValidateTenantDefaultTags, since identity v5.8.0), for the same reason: a
// default is written onto every taggable resource, so it must be accepted by
// every backend it can land on — the intersection of their rules. A default
// only one backend refuses would fail every create of that type with an error
// the resource's own configuration did not cause. Keep the two in lockstep;
// the constants, patterns and checking order below are copied from that file,
// which carries the per-backend evidence for each of them.

// MaxDefaultTags is how many default tags the provider accepts (identity
// MaxTenantDefaultTags): every default spends a slot of the smallest
// per-resource tag budget it can land on (a DNS zone's 32).
const MaxDefaultTags = 10

// MaxDefaultTagKeyBytes bounds a default tag key, in bytes (network's DNS zone
// and storage's key bound).
const MaxDefaultTagKeyBytes = 64

// MaxDefaultTagValueBytes bounds a default tag value, in bytes (storage's and
// Nova's value bound).
const MaxDefaultTagValueBytes = 255

// defaultTagKeyRegex: alphanumeric at both ends, `._:-` inside — the DNS and
// storage key shape with `/` removed because Nova metadata keys refuse it.
var defaultTagKeyRegex = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9._:-]*[a-zA-Z0-9])?$`)

// defaultTagValueRegex: the S3 bucket-tag value charset, the strictest value
// rule on the platform (empty allowed).
var defaultTagValueRegex = regexp.MustCompile(`^[a-zA-Z0-9+\-._:/@ =]*$`)

// reservedTagKeyPrefixes: the platform-owned namespace every backend reserves.
var reservedTagKeyPrefixes = []string{"frostmoln_", "frostmoln-"}

// reservedDefaultTagKeyPrefixes: the prefixes compute refuses on instance
// metadata for every caller (compute DefaultMetadataConfig().ReservedPrefixes).
// "__" cannot pass the key pattern (it must start alphanumeric); it is listed so
// the set reads as compute's.
var reservedDefaultTagKeyPrefixes = []string{"__", "os_", "instance_", "nova_"}

// reservedDefaultTagKeys: storage's volume/snapshot control keys and the six
// bucket control-plane keys, all matched case-insensitively.
var reservedDefaultTagKeys = map[string]bool{
	"request-id":    true,
	"customer-id":   true,
	"project-id":    true,
	"tenant-id":     true,
	"created-at":    true,
	"acl":           true,
	"storage-class": true,
	"quota-bytes":   true,
	"cors-config":   true,
}

// DefaultTagProblem is why a default tag was refused.
type DefaultTagProblem struct {
	// Rule is identity's details.reason: reserved_prefix, reserved_key,
	// key_length, key_charset, value_length or value_charset.
	Rule    string
	Summary string
	Detail  string
}

// CheckDefaultTag checks one default tag, in identity's order: the reserved
// namespace, then the key's length and shape, then the value's. value is nil
// when it is not known yet (a plan whose provider configuration references a
// value computed during the apply); the key is always known, and the value is
// checked by the apply-time configure. nil means the tag is accepted.
func CheckDefaultTag(key string, value *string) *DefaultTagProblem {
	if p := reservedPrefixOfFolded(key, reservedTagKeyPrefixes); p != "" {
		return &DefaultTagProblem{"reserved_prefix", "Reserved Default Tag Key",
			fmt.Sprintf("Tag key %q is reserved for the platform: keys starting with %q cannot be set.", key, p)}
	}
	if p := reservedPrefixOfFolded(key, reservedDefaultTagKeyPrefixes); p != "" {
		return &DefaultTagProblem{"reserved_prefix", "Reserved Default Tag Key",
			fmt.Sprintf("Tag key %q cannot be a default tag: keys starting with %q are reserved on instances, "+
				"and default tags are added to every taggable resource.", key, p)}
	}
	if reservedDefaultTagKeys[strings.ToLower(key)] {
		return &DefaultTagProblem{"reserved_key", "Reserved Default Tag Key",
			fmt.Sprintf("Tag key %q is reserved for the platform and cannot be set.", key)}
	}
	if key == "" || len(key) > MaxDefaultTagKeyBytes {
		return &DefaultTagProblem{"key_length", "Invalid Default Tag Key",
			fmt.Sprintf("Tag key %q must be 1 to %d bytes.", key, MaxDefaultTagKeyBytes)}
	}
	if !defaultTagKeyRegex.MatchString(key) {
		return &DefaultTagProblem{"key_charset", "Invalid Default Tag Key",
			fmt.Sprintf("Tag key %q is not allowed as a default tag: keys may contain only letters (a-z, A-Z), "+
				"digits and . _ : - and must start and end with a letter or digit, because default tags are "+
				"added to every taggable resource.", key)}
	}
	if value == nil {
		return nil
	}
	if len(*value) > MaxDefaultTagValueBytes {
		return &DefaultTagProblem{"value_length", "Invalid Default Tag Value",
			fmt.Sprintf("The value of tag %q is too long (maximum %d bytes).", key, MaxDefaultTagValueBytes)}
	}
	if !defaultTagValueRegex.MatchString(*value) {
		return &DefaultTagProblem{"value_charset", "Invalid Default Tag Value",
			fmt.Sprintf("The value of tag %q is not allowed as a default tag: values may contain only letters "+
				"(a-z, A-Z), digits, spaces and + - . _ : / @ =, because default tags are added to every "+
				"taggable resource (it may also be empty).", key)}
	}
	return nil
}

// TooManyDefaultTagsDetail is the refusal for a set over MaxDefaultTags.
func TooManyDefaultTagsDetail(count int) string {
	return fmt.Sprintf("default_tags has %d tags (maximum %d). Default tags are added to every taggable "+
		"resource, so they are kept few enough to leave room for each resource's own tags.", count, MaxDefaultTags)
}

// reservedPrefixOfFolded returns the reserved prefix key starts with, compared
// case-insensitively, or "".
func reservedPrefixOfFolded(key string, prefixes []string) string {
	k := strings.ToLower(key)
	for _, p := range prefixes {
		if strings.HasPrefix(k, p) {
			return p
		}
	}
	return ""
}
