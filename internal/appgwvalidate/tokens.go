// Package appgwvalidate holds the Application Gateway's token rules, copied
// from the server so the provider can apply them at PLAN time.
//
// 🔴 ONE COPY, BECAUSE THE ONLY THING THAT MATTERS ABOUT THESE IS THAT THEY DO
// NOT DRIFT FROM THE SERVER.
//
// They are appgw's `internal/domain/wire.go` verbatim. A provider that is
// STRICTER refuses a configuration the platform would accept; one that is
// LOOSER moves the error back to apply time, after the gateway and the listener
// are already created. Both are worse than not validating at all, so there is
// exactly one place to compare against the server rather than one per resource
// that happens to need them.
package appgwvalidate

import (
	"regexp"
	"strings"
)

// Token is RFC 7230's token rule, and the regexp is the server's byte for byte.
var Token = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+\-.^_` + "`" + `|~]+$`)

// Unrenderable is the intersection of the token set with what the gateway's
// configuration renderer cannot express.
//
// Both characters are legal HTTP. Both land in a BARE, whitespace-separated
// argument in the rendered configuration, so `#` opens a comment and truncates
// the line, and an apostrophe opens strong quoting mid-word. Either way the
// proxy refuses its WHOLE configuration and the appliance goes on serving its
// previous revision while the API reports the new one.
const Unrenderable = "#'"

// MaxHeaderValueLength and MaxCookieNameLength mirror the server's bounds. Both
// are BYTES: the server compares len(), so a non-ASCII value reaches the bound
// sooner than its character count suggests.
const (
	MaxHeaderValueLength = 1024
	MaxCookieNameLength  = 64
)

// HasUnrenderable reports whether a name carries a character the gateway cannot
// render.
func HasUnrenderable(name string) bool { return strings.ContainsAny(name, Unrenderable) }
