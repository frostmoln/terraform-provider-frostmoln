package iam_policy

import (
	_ "embed"
	"encoding/json"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

// identity normalises every stored policy document to the canonical public FRN
// form (ADR-0102 as amended 2026-10-18, ADR-0129; PUBLIC-IDS-PLAN Phase 3b): a
// UUID tenant segment, a UUID id segment of a noun with a public-id kind, and a
// frn:tenant value come back as their public ids (ten_…, ins_…). A config that
// wrote the UUID form denotes the same policy, so the read-back comparison
// undoes exactly that rewrite on both sides, and nothing more: folding a
// position identity leaves alone (a bucket name, an unregistered prefix) would
// make two policies the evaluator tells apart compare equal and hide drift.

// iamNouns maps "<service>:<type>" to the public-id prefix identity encodes that
// noun's id segment with: the output of servicekit pubid.IAMNouns/KindForNoun in
// the release identity links (v1.66.2). A noun added there later is not folded
// here, so it shows as a visible diff until this table catches up; it never
// hides one.
//
//go:embed iam_nouns.json
var iamNounsJSON []byte

var iamNouns = func() map[string]string {
	var rows []struct{ Noun, Prefix string }
	if err := json.Unmarshal(iamNounsJSON, &rows); err != nil {
		panic("iam_nouns.json: " + err.Error())
	}
	m := make(map[string]string, len(rows))
	for _, r := range rows {
		m[r.Noun] = r.Prefix
	}
	return m
}()

// tenantPrefix is the public-id prefix of a tenant.
const tenantPrefix = "ten"

// base58Alphabet is the Bitcoin alphabet ADR-0129 public ids use.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz" // pragma: allowlist secret

// publicIDBodyLen is the fixed width of a public id's body.
const publicIDBodyLen = 22

var maxUUID = new(big.Int).Lsh(big.NewInt(1), 128)

// canonicalPolicyDoc undoes identity's normalisation of a decoded policy
// document (one policy object or a policy-set array), in place: rule target
// tenant and id segments and string frn:tenant constraint values.
func canonicalPolicyDoc(v any) {
	switch d := v.(type) {
	case []any:
		for _, p := range d {
			canonicalPolicyDoc(p)
		}
	case map[string]any:
		rules, _ := d["rules"].([]any)
		for _, r := range rules {
			rule, ok := r.(map[string]any)
			if !ok {
				continue
			}
			if targets, ok := rule["targets"].([]any); ok {
				for i, t := range targets {
					if s, ok := t.(string); ok {
						targets[i] = canonicalFRN(s)
					}
				}
			}
			constraints, _ := rule["constraints"].(map[string]any)
			for _, kv := range constraints {
				if m, ok := kv.(map[string]any); ok {
					if s, ok := m["frn:tenant"].(string); ok {
						m["frn:tenant"] = canonicalID(tenantPrefix, s)
					}
				}
			}
		}
	}
}

// canonicalFRN folds the tenant segment of a five-field FRN, and its id segment
// when the noun has a public-id kind. Anything else is returned unchanged.
func canonicalFRN(s string) string {
	parts := strings.SplitN(s, ":", 5)
	if len(parts) != 5 || parts[0] != "frn" {
		return s
	}
	parts[3] = canonicalID(tenantPrefix, parts[3])
	if typ, id, ok := strings.Cut(parts[4], "/"); ok {
		if prefix, ok := iamNouns[parts[1]+":"+typ]; ok {
			parts[4] = typ + "/" + canonicalID(prefix, id)
		}
	}
	return strings.Join(parts, ":")
}

// canonicalID returns the canonical lowercase UUID behind s when s is a UUID in
// any spelling identity accepts (uuid.Parse) or a public id with the given
// prefix, and s unchanged otherwise ("*", another kind's id, a typo).
func canonicalID(prefix, s string) string {
	if u, err := uuid.Parse(s); err == nil {
		return u.String()
	}
	body, ok := strings.CutPrefix(s, prefix+"_")
	if !ok || len(body) != publicIDBodyLen {
		return s
	}
	n := new(big.Int)
	base := big.NewInt(58)
	for i := 0; i < len(body); i++ {
		d := strings.IndexByte(base58Alphabet, body[i])
		if d < 0 {
			return s
		}
		n.Mul(n, base).Add(n, big.NewInt(int64(d)))
	}
	if n.Cmp(maxUUID) >= 0 {
		return s
	}
	var b [16]byte
	n.FillBytes(b[:])
	return uuid.UUID(b).String()
}
