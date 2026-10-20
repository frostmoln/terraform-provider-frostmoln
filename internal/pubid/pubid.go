// Package pubid is the provider's copy of the servicekit public-id codec
// (ADR-0129): "<prefix>_<base58 of the 16 UUID bytes>", Bitcoin alphabet, fixed
// width 22. The provider is public and cannot import servicekit, so this copy is
// checked against servicekit's vectors.json and kinds.json, both copied verbatim
// (refresh them from servicekit origin/main pubid/testdata/).
package pubid

import (
	_ "embed"
	"encoding/json"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

const (
	alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz" // pragma: allowlist secret
	bodyLen  = 22
)

//go:embed kinds.json
var kindsJSON []byte

// Registry is the servicekit prefix registry: kind name -> prefix, the
// input-only kinds, and the IAM noun prefixes.
var Registry = func() (r struct {
	Kinds []struct {
		Name, Prefix, RendersAs string
	}
	IAMNouns []struct{ Noun, Prefix string }
},
) {
	if err := json.Unmarshal(kindsJSON, &r); err != nil {
		panic("pubid kinds.json: " + err.Error())
	}
	return r
}()

var prefixes = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range Registry.Kinds {
		m[k.Prefix] = true
	}
	return m
}()

// alias maps an input-only prefix to the prefix its ids render as (isn -> img).
var alias = func() map[string]string {
	m := map[string]string{}
	for _, k := range Registry.Kinds {
		if k.RendersAs != "" {
			m[k.Prefix] = k.RendersAs
		}
	}
	return m
}()

// Kind returns the kind a prefix's ids render as: an input-only alias maps to
// its rendered kind, anything else to itself.
func Kind(prefix string) string {
	if to, ok := alias[prefix]; ok {
		return to
	}
	return prefix
}

var limit = new(big.Int).Lsh(big.NewInt(1), 128)

// Encode returns the public id with prefix for a canonical UUID (any case).
func Encode(prefix, id string) (string, bool) {
	if !prefixes[prefix] || len(id) != 36 {
		return "", false
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return "", false
	}
	n := new(big.Int).SetBytes(u[:])
	out := []byte(strings.Repeat("1", bodyLen))
	m := new(big.Int)
	for i := bodyLen - 1; n.Sign() > 0; i-- {
		n.DivMod(n, big.NewInt(58), m)
		out[i] = alphabet[m.Int64()]
	}
	return prefix + "_" + string(out), true
}

// Canonical returns the lowercase UUID behind s and its prefix ("" for a UUID).
// s must be a canonical 36-character UUID or a public id of a registered kind.
func Canonical(s string) (id, prefix string, ok bool) {
	if len(s) == 36 {
		u, err := uuid.Parse(s)
		return u.String(), "", err == nil
	}
	prefix, body, found := strings.Cut(s, "_")
	if !found || !prefixes[prefix] || len(body) != bodyLen {
		return "", "", false
	}
	n := new(big.Int)
	for i := 0; i < bodyLen; i++ {
		d := strings.IndexByte(alphabet, body[i])
		if d < 0 {
			return "", "", false
		}
		n.Mul(n, big.NewInt(58)).Add(n, big.NewInt(int64(d)))
	}
	if n.Cmp(limit) >= 0 {
		return "", "", false
	}
	var b [16]byte
	n.FillBytes(b[:])
	return uuid.UUID(b).String(), prefix, true
}

// Equal reports whether a and b name the same id: equal strings, or the same
// UUID where a public side's kind must match the other public side's (an
// input-only alias counts as the kind it renders as). A UUID carries no kind, so
// it equals the public id of any kind with its bytes.
func Equal(a, b string) bool {
	if a == b {
		return true
	}
	ua, pa, oka := Canonical(a)
	ub, pb, okb := Canonical(b)
	return oka && okb && ua == ub && (pa == "" || pb == "" || Kind(pa) == Kind(pb))
}
