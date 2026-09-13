// Package tagsettingstest is an in-memory fake of the platform's tag colour
// routes, for the frostmoln_tag_color resource and frostmoln_tag_colors data
// source tests. It RECORDS every request (method, path, raw body) so a test can
// assert the exact wire shape — above all whether `value` went out as JSON
// null or as "" — and it models the server's constraints rather than echoing
// whatever it is sent:
//
//   - one rule per (organization, key, value), where a null value and "" are
//     different rules (identity's two partial unique indexes); a create or a
//     retargeting PUT that collides answers 409 TAG_COLOR_RULE_EXISTS naming the
//     existing rule in error.details.ruleId;
//   - a request body is decoded strictly (identity's decodeStrictJSON): unknown
//     fields are a 400, and an absent `value` means "any value";
//   - a missing or foreign rule answers the FLAT 404 identity renders, while an
//     organization the caller is not a member of answers the nested 403;
//   - the tenant route answers the owning organization's rules, with its id
//     only when ReportOrganization is set (a platform predating the field
//     omits it).
package tagsettingstest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Rule is a stored rule. Value nil = any value.
type Rule struct {
	ID        string
	Key       string
	Value     *string
	Color     string
	CreatedAt string
	UpdatedAt string
}

// Request is one recorded request.
type Request struct {
	Method string
	Path   string
	Body   []byte
}

// BodyMap decodes the recorded body as a JSON object, so a test can tell an
// absent field from a null one from "".
func (r Request) BodyMap(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body, &m); err != nil {
		t.Fatalf("%s %s: body is not a JSON object: %v (%s)", r.Method, r.Path, err, r.Body)
	}
	return m
}

// Fake is the in-memory server.
type Fake struct {
	TenantID string
	// OrgID owns TenantID. The caller is a member of every organization in
	// Orgs; any other organization answers 403.
	OrgID string
	// ReportOrganization makes the tenant route carry organizationId.
	ReportOrganization bool
	// NotRouted, when set, answers every tag-colour request with the
	// api-gateway's nested unrouted-path 404.
	NotRouted bool
	// DenyTenantRoute answers GET /v1/tenants/{tid}/tag-colors with identity's
	// nested 403 — a key without organizations:read.
	DenyTenantRoute bool

	Server *httptest.Server

	mu       sync.Mutex
	orgs     map[string]map[string]*Rule // org -> rule id -> rule
	requests []Request
	clock    int
}

// New starts a fake whose tenant is owned by one organization, which reports
// itself on the tenant route.
func New(t *testing.T) *Fake {
	t.Helper()
	f := &Fake{
		TenantID:           "11111111-1111-4111-8111-111111111111",
		OrgID:              "2222abcd-2222-4222-8222-22222222abcd", // letters, so a case change is a real change
		ReportOrganization: true,
		orgs:               map[string]map[string]*Rule{},
	}
	f.orgs[f.OrgID] = map[string]*Rule{}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Server.Close)
	return f
}

// AddOrg makes the caller a member of another organization.
func (f *Fake) AddOrg(orgID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orgs[orgID] == nil {
		f.orgs[orgID] = map[string]*Rule{}
	}
}

// Seed stores a rule directly (as the portal would) and returns its id.
func (f *Fake) Seed(orgID, key string, value *string, color string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.orgs[orgID] == nil {
		f.orgs[orgID] = map[string]*Rule{}
	}
	now := f.tick()
	r := &Rule{ID: uuid.NewString(), Key: key, Value: value, Color: color, CreatedAt: now, UpdatedAt: now}
	f.orgs[orgID][r.ID] = r
	return r.ID
}

// Get returns a copy of a stored rule, or nil.
func (f *Fake) Get(orgID, ruleID string) *Rule {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.orgs[orgID][ruleID]
	if !ok {
		return nil
	}
	c := *r
	return &c
}

// Mutate changes a stored rule out of band (as the portal would).
func (f *Fake) Mutate(orgID, ruleID string, fn func(*Rule)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.orgs[orgID][ruleID])
	f.orgs[orgID][ruleID].UpdatedAt = f.tick()
}

// Remove deletes a stored rule out of band.
func (f *Fake) Remove(orgID, ruleID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.orgs[orgID], ruleID)
}

// Requests returns the recorded requests, /v1/me excluded.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Request, len(f.requests))
	copy(out, f.requests)
	return out
}

// Only returns the recorded requests with the given method.
func (f *Fake) Only(method string) []Request {
	var out []Request
	for _, r := range f.Requests() {
		if r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

// Reset forgets the recorded requests.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

func (f *Fake) tick() string {
	f.clock++
	return time.Date(2026, 9, 13, 12, 0, f.clock, 0, time.UTC).Format("2006-01-02T15:04:05.000000Z")
}

type wireRule struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	Value     *string `json:"value"`
	Color     string  `json:"color"`
	CreatedAt string  `json:"createdAt"`
	UpdatedAt string  `json:"updatedAt"`
}

func toWire(r *Rule) wireRule {
	return wireRule{ID: r.ID, Key: r.Key, Value: r.Value, Color: r.Color, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
}

// sorted is identity's order: key, the key-only rule first, then value, then id.
func sorted(m map[string]*Rule) []wireRule {
	rules := make([]*Rule, 0, len(m))
	for _, r := range m {
		rules = append(rules, r)
	}
	sort.Slice(rules, func(i, j int) bool {
		a, b := rules[i], rules[j]
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		if (a.Value == nil) != (b.Value == nil) {
			return a.Value == nil
		}
		if a.Value != nil && *a.Value != *b.Value {
			return *a.Value < *b.Value
		}
		return a.ID < b.ID
	})
	out := make([]wireRule, 0, len(rules))
	for _, r := range rules {
		out = append(out, toWire(r))
	}
	return out
}

func sameTarget(r *Rule, key string, value *string) bool {
	if r.Key != key || (r.Value == nil) != (value == nil) {
		return false
	}
	return value == nil || *r.Value == *value
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func nested(w http.ResponseWriter, status int, code, msg string, details map[string]any) {
	body := map[string]any{"code": code, "message": msg}
	if details != nil {
		body["details"] = details
	}
	writeJSON(w, status, map[string]any{"error": body})
}

func flatNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]any{"code": "NOT_FOUND", "message": "resource not found"})
}

// ruleRequest mirrors identity's tagColorRuleRequest, read-only fields included.
type ruleRequest struct {
	Key       string          `json:"key"`
	Value     *string         `json:"value"`
	Color     string          `json:"color"`
	ID        json.RawMessage `json:"id"`
	CreatedAt json.RawMessage `json:"createdAt"`
	UpdatedAt json.RawMessage `json:"updatedAt"`
}

func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if r.Method == http.MethodGet && r.URL.Path == "/v1/me" {
		writeJSON(w, http.StatusOK, map[string]string{"id": "user-1", "tenantId": f.TenantID, "email": "t@example.com"})
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.EscapedPath(), Body: body})

	if f.NotRouted {
		nested(w, http.StatusNotFound, "PATH_NOT_ROUTED", "no route found for path", nil)
		return
	}

	segs := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case len(segs) == 4 && segs[0] == "v1" && segs[1] == "tenants" && segs[3] == "tag-colors" && r.Method == http.MethodGet:
		if segs[2] != f.TenantID {
			nested(w, http.StatusForbidden, "TENANT_ACCESS_DENIED", "tenant access denied", nil)
			return
		}
		if f.DenyTenantRoute {
			nested(w, http.StatusForbidden, "INSUFFICIENT_SCOPE", "the API key lacks the organizations:read scope", nil)
			return
		}
		resp := map[string]any{"rules": sorted(f.orgs[f.OrgID])}
		if f.ReportOrganization {
			resp["organizationId"] = f.OrgID
		}
		writeJSON(w, http.StatusOK, resp)
	case len(segs) >= 4 && segs[0] == "v1" && segs[1] == "organizations" && segs[3] == "tag-colors":
		f.serveOrg(w, r, segs, body)
	default:
		nested(w, http.StatusNotFound, "PATH_NOT_ROUTED", fmt.Sprintf("unexpected %s %s", r.Method, r.URL.Path), nil)
	}
}

func (f *Fake) serveOrg(w http.ResponseWriter, r *http.Request, segs []string, body []byte) {
	orgID := segs[2]
	if _, err := uuid.Parse(orgID); err != nil {
		nested(w, http.StatusBadRequest, "INVALID_INPUT", "invalid organization ID", nil)
		return
	}
	rules, member := f.orgs[orgID]
	if !member {
		nested(w, http.StatusForbidden, "PERMISSION_DENIED", "you do not have permission to perform this action on this organization", nil)
		return
	}

	if len(segs) == 4 {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"rules": sorted(rules)})
		case http.MethodPost:
			var req ruleRequest
			if err := decodeStrict(body, &req); err != nil {
				nested(w, http.StatusBadRequest, "INVALID_INPUT", "invalid request body: "+err.Error(), nil)
				return
			}
			for _, existing := range rules {
				if sameTarget(existing, req.Key, req.Value) {
					nested(w, http.StatusConflict, "TAG_COLOR_RULE_EXISTS",
						"a colour rule for this tag key and value already exists in the organization",
						map[string]any{"ruleId": existing.ID})
					return
				}
			}
			now := f.tick()
			rule := &Rule{ID: uuid.NewString(), Key: req.Key, Value: req.Value, Color: req.Color, CreatedAt: now, UpdatedAt: now}
			rules[rule.ID] = rule
			writeJSON(w, http.StatusCreated, toWire(rule))
		default:
			nested(w, http.StatusNotFound, "PATH_NOT_ROUTED", "no route", nil)
		}
		return
	}
	if len(segs) != 5 {
		nested(w, http.StatusNotFound, "PATH_NOT_ROUTED", "no route", nil)
		return
	}

	ruleID := segs[4]
	if _, err := uuid.Parse(ruleID); err != nil {
		nested(w, http.StatusBadRequest, "INVALID_INPUT", "invalid tag colour rule ID", nil)
		return
	}
	rule, ok := rules[ruleID]
	switch r.Method {
	case http.MethodGet:
		if !ok {
			flatNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, toWire(rule))
	case http.MethodPut:
		var req ruleRequest
		if err := decodeStrict(body, &req); err != nil {
			nested(w, http.StatusBadRequest, "INVALID_INPUT", "invalid request body: "+err.Error(), nil)
			return
		}
		if !ok {
			flatNotFound(w)
			return
		}
		for _, existing := range rules {
			if existing.ID != ruleID && sameTarget(existing, req.Key, req.Value) {
				nested(w, http.StatusConflict, "TAG_COLOR_RULE_EXISTS",
					"a colour rule for this tag key and value already exists in the organization",
					map[string]any{"ruleId": existing.ID})
				return
			}
		}
		rule.Key, rule.Value, rule.Color = req.Key, req.Value, req.Color
		rule.UpdatedAt = f.tick()
		writeJSON(w, http.StatusOK, toWire(rule))
	case http.MethodDelete:
		if !ok {
			flatNotFound(w)
			return
		}
		delete(rules, ruleID)
		w.WriteHeader(http.StatusNoContent)
	default:
		nested(w, http.StatusNotFound, "PATH_NOT_ROUTED", "no route", nil)
	}
}
