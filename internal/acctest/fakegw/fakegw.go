// Package fakegw is an in-process stand-in for the Frostmoln api-gateway,
// serving the few tenant routes the public-id switch gates exercise (VPCs,
// subnets, security groups and their rules, VPC routes, launch templates and
// the image list) plus GET /v1/me.
//
// It models the one gateway behaviour the gates are about (ADR-0129): entities
// are keyed by UUID, ids on input are accepted as a canonical UUID or as the
// public id of the expected kind (another kind's prefix is a 400), and ids on
// output are rendered as public ids iff the request carries
// Frostmoln-Id-Format: public, and as UUIDs otherwise. Creates answer
// synchronously with 201 and the entity, deletes with 204.
//
// Anything it does not serve is recorded (Unrouted) and answered 404, so a
// test can fail on a route the fake is missing instead of on a confusing
// provider diagnostic.
package fakegw

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/pubid"
)

// idFormatHeader is the request header the gateway renders ids by.
const idFormatHeader = "Frostmoln-Id-Format"

// ImageName is the one image the fake's image list serves.
const ImageName = "Ubuntu 24.04"

// collection describes one entity family: its path segment, the list key its
// GET answers under, and the id fields it renders (a dotted path, element
// steps omitted, to a servicekit prefix).
type collection struct {
	listKey string
	render  map[string]string
}

var collections = map[string]collection{
	"vpcs":             {listKey: "vpcs", render: map[string]string{"id": "vpc"}},
	"subnets":          {listKey: "subnets", render: map[string]string{"id": "sub", "vpcId": "vpc"}},
	"security-groups":  {listKey: "securityGroups", render: map[string]string{"id": "sg", "vpcId": "vpc", "rules.id": "sgr", "rules.remoteSecurityGroupId": "sg"}},
	"launch-templates": {listKey: "launchTemplates", render: map[string]string{"id": "lt", "imageId": "img", "vpcId": "vpc", "securityGroupIds": "sg"}},
	"images":           {listKey: "images", render: map[string]string{"id": "img"}},
}

// ruleRender renders a rule answered on its own (the synchronous create).
var ruleRender = map[string]string{"id": "sgr", "remoteSecurityGroupId": "sg"}

// Server is a running fake gateway. Its URL plus "/api" is the provider's
// api_endpoint.
type Server struct {
	*httptest.Server

	TenantID string // canonical UUID
	UserID   string // canonical UUID
	ImageID  string // canonical UUID of the ImageName image

	mu       sync.Mutex
	store    map[string]map[string]map[string]any // collection -> uuid -> entity
	routes   map[string][]map[string]any          // vpc uuid -> routes
	requests []string
	unrouted []string
	agents   map[string]bool
}

// New starts a fake gateway. Close it when done.
func New() *Server {
	s := &Server{
		TenantID: uuid.NewString(),
		UserID:   uuid.NewString(),
		ImageID:  uuid.NewString(),
		store:    map[string]map[string]map[string]any{},
		routes:   map[string][]map[string]any{},
		agents:   map[string]bool{},
	}
	for name := range collections {
		s.store[name] = map[string]map[string]any{}
	}
	s.store["images"][s.ImageID] = map[string]any{
		"id": s.ImageID, "name": ImageName, "osDistro": "ubuntu", "osVersion": "24.04",
		"minDisk": 10, "minRam": 512, "status": "active", "createdAt": now(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/me", s.me)
	t := "/api/v1/tenants/{tenant}"
	for _, c := range []string{"vpcs", "subnets", "security-groups", "launch-templates"} {
		mux.HandleFunc("POST "+t+"/"+c, s.tenant(s.create(c)))
		mux.HandleFunc("GET "+t+"/"+c, s.tenant(s.list(c)))
		mux.HandleFunc("GET "+t+"/"+c+"/{id}", s.tenant(s.get(c)))
		mux.HandleFunc("DELETE "+t+"/"+c+"/{id}", s.tenant(s.remove(c)))
	}
	mux.HandleFunc("GET "+t+"/images", s.tenant(s.list("images")))
	for _, c := range []string{"vpcs", "subnets", "security-groups"} {
		mux.HandleFunc("PUT "+t+"/"+c+"/{id}", s.tenant(s.update(c)))
	}
	mux.HandleFunc("PATCH "+t+"/launch-templates/{id}", s.tenant(s.update("launch-templates")))
	mux.HandleFunc("POST "+t+"/security-groups/{id}/rules", s.tenant(s.createRule))
	mux.HandleFunc("DELETE "+t+"/security-groups/{id}/rules/{rule}", s.tenant(s.deleteRule))
	mux.HandleFunc("GET "+t+"/vpcs/{id}/routes", s.tenant(s.listRoutes))
	mux.HandleFunc("POST "+t+"/vpcs/{id}/routes", s.tenant(s.createRoute))
	mux.HandleFunc("DELETE "+t+"/vpcs/{id}/routes", s.tenant(s.deleteRoute))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.unrouted = append(s.unrouted, r.Method+" "+r.URL.Path)
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "PATH_NOT_ROUTED", "message": "fakegw does not serve " + r.Method + " " + r.URL.Path}})
	})

	s.Server = httptest.NewServer(s.logged(mux))
	return s
}

// Endpoint is the provider's api_endpoint for this fake.
func (s *Server) Endpoint() string { return s.URL + "/api" }

// Requests returns every request served so far as "METHOD path [format]".
func (s *Server) Requests() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

// Unrouted returns the requests the fake had no route for.
func (s *Server) Unrouted() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.unrouted...)
}

// UserAgents returns the distinct User-Agent headers seen, sorted: the
// provider stamps its version there, so it names the release a step ran.
func (s *Server) UserAgents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.agents))
	for a := range s.agents {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// Remaining lists the entities still stored (excluding the seeded image), as
// "collection/uuid" and "vpcs/uuid/routes/destination".
func (s *Server) Remaining() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for c, m := range s.store {
		if c == "images" {
			continue
		}
		for id := range m {
			out = append(out, c+"/"+id)
		}
	}
	for vpc, rs := range s.routes {
		for _, r := range rs {
			out = append(out, fmt.Sprintf("vpcs/%s/routes/%v", vpc, r["destination"]))
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) logged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, fmt.Sprintf("%s %s [%s]", r.Method, r.URL.Path, r.Header.Get(idFormatHeader)))
		s.agents[r.UserAgent()] = true
		s.mu.Unlock()
		if r.Header.Get("X-API-Key") == "" && r.Header.Get("Authorization") == "" {
			writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "no credential")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func public(r *http.Request) bool { return r.Header.Get(idFormatHeader) == "public" }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr answers in servicekit's flat envelope.
func writeErr(w http.ResponseWriter, code int, errCode, msg string) {
	writeJSON(w, code, map[string]any{"code": errCode, "message": msg})
}

// decodeID returns the UUID behind s, which must be a canonical UUID or a
// public id whose kind is want's.
func decodeID(s, want string) (string, error) {
	id, prefix, ok := pubid.Canonical(s)
	if !ok {
		return "", fmt.Errorf("%q is not a UUID or a public id", s)
	}
	if prefix != "" && pubid.Kind(prefix) != pubid.Kind(want) {
		return "", fmt.Errorf("%q has prefix %q, want %q", s, prefix, want)
	}
	return id, nil
}

// pathID decodes the path value name as an id of kind want, answering 400 on
// failure.
func pathID(w http.ResponseWriter, r *http.Request, name, want string) (string, bool) {
	id, err := decodeID(r.PathValue(name), want)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_ID", err.Error())
		return "", false
	}
	return id, true
}

// tenant checks the {tenant} path segment names this fake's tenant.
func (s *Server) tenant(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r, "tenant", "ten")
		if !ok {
			return
		}
		if id != s.TenantID {
			writeErr(w, http.StatusForbidden, "TENANT_ACCESS_DENIED", "not this key's tenant")
			return
		}
		next(w, r)
	}
}

// renderValue returns a deep copy of v with every id field named by kinds
// rendered in the request's id format.
func renderValue(v any, kinds map[string]string, path string, pub bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			p := k
			if path != "" {
				p = path + "." + k
			}
			out[k] = renderValue(e, kinds, p, pub)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = renderValue(e, kinds, path, pub)
		}
		return out
	case string:
		prefix, ok := kinds[path]
		if !ok || !pub || t == "" {
			return t
		}
		enc, ok := pubid.Encode(prefix, t)
		if !ok {
			return t
		}
		return enc
	}
	return v
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, renderValue(map[string]any{
		"id": s.UserID, "tenantId": s.TenantID, "email": "gate@example.test", "name": "Gate",
	}, map[string]string{"id": "usr", "tenantId": "ten"}, "", public(r)))
}

func readBody(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	body := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "INVALID_BODY", err.Error())
		return nil, false
	}
	return body, true
}

// inputIDs names, per collection, the id fields a write body carries, their
// kind, and the collection that must hold the referenced entity.
var inputIDs = map[string]map[string][2]string{
	"subnets":          {"vpcId": {"vpc", "vpcs"}},
	"security-groups":  {"vpcId": {"vpc", "vpcs"}},
	"launch-templates": {"imageId": {"img", "images"}, "vpcId": {"vpc", "vpcs"}, "securityGroupIds": {"sg", "security-groups"}},
}

// decodeInputs rewrites the body's id fields to UUIDs in place. The caller
// holds s.mu.
func (s *Server) decodeInputs(c string, body map[string]any) error {
	for field, spec := range inputIDs[c] {
		v, ok := body[field]
		if !ok || v == nil {
			continue
		}
		one := func(raw any) (string, error) {
			str, _ := raw.(string)
			id, err := decodeID(str, spec[0])
			if err != nil {
				return "", fmt.Errorf("%s: %w", field, err)
			}
			if _, ok := s.store[spec[1]][id]; !ok {
				return "", fmt.Errorf("%s: %s %s not found", field, spec[1], id)
			}
			return id, nil
		}
		switch t := v.(type) {
		case []any:
			out := make([]any, len(t))
			for i, e := range t {
				id, err := one(e)
				if err != nil {
					return err
				}
				out[i] = id
			}
			body[field] = out
		default:
			id, err := one(t)
			if err != nil {
				return err
			}
			body[field] = id
		}
	}
	return nil
}

// createFields maps a create body to the stored entity's fields, per
// collection (request and response names differ in places).
func createEntity(c, id string, body map[string]any) map[string]any {
	e := map[string]any{"id": id, "createdAt": now()}
	for k, v := range body {
		e[k] = v
	}
	switch c {
	case "vpcs":
		e["status"] = "active"
		e["isDefault"] = false
		e["subnetCount"] = 0
	case "subnets":
		if dns, ok := e["dnsNameservers"]; ok {
			e["dnsServers"] = dns
			delete(e, "dnsNameservers")
		}
		e["status"] = "active"
		e["availableIpCount"] = 250
	case "security-groups":
		e["isDefault"] = false
		e["rules"] = []any{}
	case "launch-templates":
		delete(e, "userData")
	}
	return e
}

func (s *Server) create(c string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if err := s.decodeInputs(c, body); err != nil {
			writeErr(w, http.StatusBadRequest, "INVALID_ID", err.Error())
			return
		}
		e := createEntity(c, uuid.NewString(), body)
		s.store[c][e["id"].(string)] = e
		writeJSON(w, http.StatusCreated, renderValue(e, collections[c].render, "", public(r)))
	}
}

func (s *Server) list(c string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		ids := make([]string, 0, len(s.store[c]))
		for id := range s.store[c] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		items := make([]any, 0, len(ids))
		for _, id := range ids {
			items = append(items, renderValue(s.store[c][id], collections[c].render, "", public(r)))
		}
		writeJSON(w, http.StatusOK, map[string]any{collections[c].listKey: items})
	}
}

// lookup decodes the {id} path value as an entity of collection c. The caller
// holds s.mu.
func (s *Server) lookup(w http.ResponseWriter, r *http.Request, c string) (map[string]any, bool) {
	id, ok := pathID(w, r, "id", collections[c].render["id"])
	if !ok {
		return nil, false
	}
	e, ok := s.store[c][id]
	if !ok {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", c+" "+id+" not found")
		return nil, false
	}
	return e, true
}

func (s *Server) get(c string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		e, ok := s.lookup(w, r, c)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, renderValue(e, collections[c].render, "", public(r)))
	}
}

func (s *Server) update(c string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		e, ok := s.lookup(w, r, c)
		if !ok {
			return
		}
		if err := s.decodeInputs(c, body); err != nil {
			writeErr(w, http.StatusBadRequest, "INVALID_ID", err.Error())
			return
		}
		for k, v := range body {
			if k == "userData" {
				continue
			}
			// A null field is "leave it": the provider sends a field it does
			// not change as null and clears one with an empty value.
			if v == nil {
				continue
			}
			e[k] = v
		}
		e["updatedAt"] = now()
		writeJSON(w, http.StatusOK, renderValue(e, collections[c].render, "", public(r)))
	}
}

func (s *Server) remove(c string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		e, ok := s.lookup(w, r, c)
		if !ok {
			return
		}
		id := e["id"].(string)
		delete(s.store[c], id)
		if c == "vpcs" {
			delete(s.routes, id)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sg, ok := s.lookup(w, r, "security-groups")
	if !ok {
		return
	}
	rule := map[string]any{"id": uuid.NewString(), "createdAt": now()}
	for k, v := range body {
		switch k {
		case "remoteIpPrefix":
			rule["remoteCidr"] = v
		case "remoteGroupId":
			str, _ := v.(string)
			id, err := decodeID(str, "sg")
			if err != nil {
				writeErr(w, http.StatusBadRequest, "INVALID_ID", "remoteGroupId: "+err.Error())
				return
			}
			if _, ok := s.store["security-groups"][id]; !ok {
				writeErr(w, http.StatusBadRequest, "INVALID_ID", "remoteGroupId: security group not found")
				return
			}
			rule["remoteSecurityGroupId"] = id
		default:
			rule[k] = v
		}
	}
	sg["rules"] = append(sg["rules"].([]any), rule)
	writeJSON(w, http.StatusCreated, renderValue(rule, ruleRender, "", public(r)))
}

func (s *Server) deleteRule(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sg, ok := s.lookup(w, r, "security-groups")
	if !ok {
		return
	}
	ruleID, ok := pathID(w, r, "rule", "sgr")
	if !ok {
		return
	}
	rules := sg["rules"].([]any)
	for i, raw := range rules {
		if raw.(map[string]any)["id"] == ruleID {
			sg["rules"] = append(rules[:i:i], rules[i+1:]...)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "NOT_FOUND", "rule "+ruleID+" not found")
}

func (s *Server) routeList(vpc string) map[string]any {
	out := make([]any, 0, len(s.routes[vpc]))
	for _, rt := range s.routes[vpc] {
		out = append(out, rt)
	}
	return map[string]any{"routes": out}
}

func (s *Server) listRoutes(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vpc, ok := s.lookup(w, r, "vpcs")
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.routeList(vpc["id"].(string)))
}

func (s *Server) createRoute(w http.ResponseWriter, r *http.Request) {
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	vpc, ok := s.lookup(w, r, "vpcs")
	if !ok {
		return
	}
	id := vpc["id"].(string)
	dest, _ := body["destination"].(string)
	for _, rt := range s.routes[id] {
		if rt["destination"] == dest {
			writeErr(w, http.StatusConflict, "ROUTE_EXISTS", "a route to "+dest+" exists")
			return
		}
	}
	s.routes[id] = append(s.routes[id], map[string]any{"destination": dest, "nextHop": body["nextHop"]})
	writeJSON(w, http.StatusCreated, s.routeList(id))
}

func (s *Server) deleteRoute(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	vpc, ok := s.lookup(w, r, "vpcs")
	if !ok {
		return
	}
	id := vpc["id"].(string)
	dest := r.URL.Query().Get("destination")
	for i, rt := range s.routes[id] {
		if rt["destination"] == dest {
			s.routes[id] = append(s.routes[id][:i:i], s.routes[id][i+1:]...)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	writeErr(w, http.StatusNotFound, "ROUTE_NOT_FOUND", "no route to "+strings.TrimSpace(dest))
}
