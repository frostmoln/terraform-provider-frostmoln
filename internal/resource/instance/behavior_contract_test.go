package instance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// The schema⇄behavior contract pins for the drift-prone instance attributes
// (Mechanic 4 of the surface contract program, work 01a0801a-184f). Each test
// pins what the BACKEND actually does against what the schema description
// claims — the pair is re-pinned by TestAttributeDescriptionContract (the
// pinnedBehaviorSentences table), so losing either the claim or the behavior
// fails CI.

// TestInstanceResource_TFSDKCreateWithPinnedSubnetRefusedForMissingSecurityGroups
// pins the 01a041f8-98ef contract: subnet_id pins the port, and provisioning
// refuses to a fix the schema said Optional and the old description promised a
// create-time clear-fallback the backend refuses inside the saga — for every
// successful create the saga must reach the pin-port step, fail, and surface
// the workflow's own "security_group_ids is required" prose. (The improve-the-
// message option — a plan-time cross-attribute validator — belongs to that
// work item's own fix; not this leg.)
func TestInstanceResource_TFSDKCreateWithPinnedSubnetRefusedForMissingSecurityGroups(t *testing.T) {
	var listingHits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)

		case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances":
			// The 202 is accepted — the refusal fires INSIDE the saga, at
			// pin-port, not at the create edge (which has no view of the VPC
			// attachments yet).
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-pin-port", "status": "pending", "resourceType": "instance",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-pin-port":
			// The workflow's own refusal, as provisioning surfaces it.
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operationId": "op-pin-port", "status": "failed", "resourceType": "instance",
				"error": "pin port to subnet: security_group_ids is required (network refuses an empty list to avoid the silent tenant-default fall-back)",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances":
			atomic.AddInt32(&listingHits, 1)
			_ = json.NewEncoder(w).Encode(apiInstanceList{})

		case strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)

		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := orphanInstanceResource(t, server)
	schemaResp := getInstanceSchema(t)
	tfType := schemaResp.Schema.Type().TerraformType(context.Background())

	planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"name":      tftypes.NewValue(tftypes.String, "pinned-and-deserted"),
		"subnet_id": tftypes.NewValue(tftypes.String, "subnet-123"),
		// security_groups left at its default (null) — the fiction the schema
		// used to promise was that this clears to default-drop. It refuses.
	})

	createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Create(context.Background(), resource.CreateRequest{
		Plan:   tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: planVal},
	}, createResp)

	if !createResp.Diagnostics.HasError() {
		t.Fatal("expected the pinned-subnet create without security groups to error")
	}
	if atomic.LoadInt32(&listingHits) != 0 {
		t.Errorf("a saga refusal created nothing to adopt; listing was hit %d times", listingHits)
	}
	text := orphanInstanceDiagText(createResp.Diagnostics)
	if !strings.Contains(text, "security_group_ids is required") {
		t.Errorf("the diagnostic must surface the workflow's own refusal prose, not a generic failure:\n%s", text)
	}
}

// TestInstanceCreateSecurityGroupsWireShape pins the identifier-space fact the
// description rests on: on CREATE, an unset security_groups and an explicitly
// empty one are wire-identical (the create body has no clear flag — only the
// PUT subresource carries clearSecurityGroups), and a set carries the IDs.
func TestInstanceCreateSecurityGroupsWireShape(t *testing.T) {
	sgType := tftypes.Set{ElementType: tftypes.String}
	emptySet := tftypes.NewValue(sgType, []tftypes.Value{})
	setOfTwo := tftypes.NewValue(sgType, []tftypes.Value{
		tftypes.NewValue(tftypes.String, "sg-web"),
		tftypes.NewValue(tftypes.String, "sg-db"),
	})

	cases := map[string]struct {
		value      tftypes.Value
		wantInBody bool
	}{
		"unset is omitted": {value: tftypes.NewValue(sgType, nil), wantInBody: false},
		"empty is omitted": {value: emptySet, wantInBody: false}, // omitempty: [] == unset on create, by wire contract
		"set carries IDs":  {value: setOfTwo, wantInBody: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var createBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
					meHandler(w, r)
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
					w.WriteHeader(http.StatusNotFound)
				case r.Method == http.MethodPost && r.URL.Path == "/v1/tenants/tenant-456/instances":
					mu.Lock()
					_ = json.NewDecoder(r.Body).Decode(&createBody)
					mu.Unlock()
					w.WriteHeader(http.StatusAccepted)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"operationId": "op-sg-shape", "status": "completed", "resourceType": "instance",
					})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/operations/op-sg-shape":
					_ = json.NewEncoder(w).Encode(map[string]any{
						"operationId": "op-sg-shape", "status": "completed", "resourceType": "instance",
						"resourceId": "inst-sg-shape",
					})
				case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-sg-shape":
					_ = json.NewEncoder(w).Encode(apiInstance{
						ID: "inst-sg-shape", Name: "test-vm", Status: "running",
						FlavorID: "flavor-small", ImageID: "img-ubuntu",
					})
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			r := orphanInstanceResource(t, server)
			// Completed on the first poll, so create returns well inside the default.
			r.pollTimeout = 2 * time.Second
			schemaResp := getInstanceSchema(t)
			tfType := schemaResp.Schema.Type().TerraformType(context.Background())

			planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
				"id":              tftypes.NewValue(tftypes.String, "inst-sg-shape"),
				"security_groups": tc.value,
			})

			createResp := &resource.CreateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
			r.Create(context.Background(), resource.CreateRequest{
				Plan:   tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
				Config: tfsdk.Config{Schema: schemaResp.Schema, Raw: planVal},
			}, createResp)
			if createResp.Diagnostics.HasError() {
				t.Fatalf("create must succeed for the wire-shape pin: %v", createResp.Diagnostics.Errors())
			}

			mu.Lock()
			defer mu.Unlock()
			sgRaw, present := createBody["securityGroupIds"]
			if tc.wantInBody != present {
				t.Fatalf("securityGroupIds presence on the create wire = %v, want %v", present, tc.wantInBody)
			}
			if !tc.wantInBody {
				return
			}
			got, _ := sgRaw.([]any)
			if len(got) != 2 {
				t.Errorf("expected the two configured SG IDs on the wire, got %v", sgRaw)
			}
		})
	}
}

// TestInstanceUpdateSecurityGroupsSendsTheClearFlag pins the UPDATE half of
// the contract: clearing the set PUTs the subresource with
// clearSecurityGroups: true (an empty list alone is refused by the backend as
// a probable dropped field), and the PUT waits for the async apply to converge.
func TestInstanceUpdateSecurityGroupsSendsTheClearFlag(t *testing.T) {
	var clearFlag, sgListKey bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/me":
			meHandler(w, r)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/events"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-sgc/security-groups":
			// The authoritative per-port view after the clear.
			_ = json.NewEncoder(w).Encode(map[string]any{"security_groups": []any{}, "uniform": true})
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-sgc/security-groups":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			clearFlag = body["clearSecurityGroups"] == true
			sgListKey = body["securityGroupIds"] != nil
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-sgc":
			// The instance update (name/tags) that shares the Update walk.
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/tenants/tenant-456/instances/inst-sgc":
			_ = json.NewEncoder(w).Encode(apiInstance{
				ID: "inst-sgc", Name: "test-vm", Status: "running",
				FlavorID: "flavor-small", ImageID: "img-ubuntu",
			})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	r := orphanInstanceResource(t, server)
	schemaResp := getInstanceSchema(t)
	tfType := schemaResp.Schema.Type().TerraformType(context.Background())
	emptySet := tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{})
	priorSet := tftypes.NewValue(tftypes.Set{ElementType: tftypes.String}, []tftypes.Value{
		tftypes.NewValue(tftypes.String, "sg-old"),
	})

	planVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "inst-sgc"),
		"security_groups": emptySet,
	})
	stateVal := instanceTFValue(t, tfType, map[string]tftypes.Value{
		"id":              tftypes.NewValue(tftypes.String, "inst-sgc"),
		"name":            tftypes.NewValue(tftypes.String, "test-vm"),
		"flavor_id":       tftypes.NewValue(tftypes.String, "flavor-small"),
		"image_id":        tftypes.NewValue(tftypes.String, "img-ubuntu"),
		"status":          tftypes.NewValue(tftypes.String, "ACTIVE"),
		"created_at":      tftypes.NewValue(tftypes.String, "2025-01-01T00:00:00Z"),
		"security_groups": priorSet,
		"user_data_hash":  tftypes.NewValue(tftypes.String, nil),
	})

	updateResp := &resource.UpdateResponse{State: tfsdk.State{Schema: schemaResp.Schema}}
	r.Update(context.Background(), resource.UpdateRequest{
		Plan:  tfsdk.Plan{Schema: schemaResp.Schema, Raw: planVal},
		State: tfsdk.State{Schema: schemaResp.Schema, Raw: stateVal},
	}, updateResp)

	if updateResp.Diagnostics.HasError() {
		t.Fatalf("the clear update must succeed: %v", updateResp.Diagnostics.Errors())
	}
	if !clearFlag || !sgListKey {
		t.Errorf("the clear must PUT both securityGroupIds: [] AND clearSecurityGroups: true (clear=%v, ids=%v)", clearFlag, sgListKey)
	}
}
