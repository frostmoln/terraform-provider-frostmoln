package messagingobj

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"go.frostmoln.internal/terraform-provider-frostmoln/internal/client"
)

// The lock is what keeps a -parallelism=10 apply from queueing operations in
// one guest: holders of one instance's lock never overlap, and other
// instances are not blocked by it.
func TestLockSerialisesOneInstance(t *testing.T) {
	var inside, maxInside int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := Lock(context.Background(), "inst-serial")
			if err != nil {
				t.Errorf("lock: %v", err)
				return
			}
			defer release()
			n := atomic.AddInt32(&inside, 1)
			for {
				m := atomic.LoadInt32(&maxInside)
				if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			atomic.AddInt32(&inside, -1)
		}()
	}
	wg.Wait()
	if maxInside != 1 {
		t.Fatalf("%d holders of one instance's lock overlapped; want 1", maxInside)
	}
}

func TestLockIsPerInstanceAndCaseInsensitive(t *testing.T) {
	release, err := Lock(context.Background(), "ABC-lock")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	// Another instance proceeds at once.
	other, err := Lock(context.Background(), "def-lock")
	if err != nil {
		t.Fatalf("another instance was blocked: %v", err)
	}
	other()

	// The same instance in another case waits, and a cancelled wait reports it.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, "abc-lock"); err == nil {
		t.Fatal("the same instance (other case) took the lock while it was held")
	}
}

func TestLockReleaseIsIdempotent(t *testing.T) {
	release, err := Lock(context.Background(), "inst-idem")
	if err != nil {
		t.Fatal(err)
	}
	release()
	release() // must not block or free a lock taken by someone else
	again, err := Lock(context.Background(), "inst-idem")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestJudge(t *testing.T) {
	cases := []struct {
		status, reason string
		want           Outcome
	}{
		{StatusReady, "", Succeeded},
		{StatusReady, "BROKER_REFUSED", ChangeFailed},
		{StatusError, "NOT_DELIVERED", Errored},
		{StatusError, "", Errored},
	}
	for _, c := range cases {
		if got := Judge(c.status, c.reason); got != c.want {
			t.Errorf("Judge(%q, %q) = %v, want %v", c.status, c.reason, got, c.want)
		}
	}
}

func TestFailureTextAndBrokerSentence(t *testing.T) {
	if got := FailureText("IN_FLIGHT", "Too many\x1b[31m operations"); got != "Too many[31m operations (IN_FLIGHT)" {
		t.Errorf("FailureText = %q", got)
	}
	if got := FailureText("", ""); got == "" {
		t.Error("FailureText must never be empty")
	}
	if !strings.Contains(BrokerSentence("BROKER_REFUSED"), "unchanged") {
		t.Error("every reason but OUTCOME_UNKNOWN leaves the broker unchanged")
	}
	if strings.Contains(BrokerSentence(FailureOutcomeUnknown), "unchanged") {
		t.Error("OUTCOME_UNKNOWN must not claim the broker is unchanged")
	}
}

func TestHasCodeMatchesBothCasings(t *testing.T) {
	err := &client.APIError{Code: "object_in_use", StatusCode: http.StatusConflict}
	if !HasCode(err, CodeObjectInUse) {
		t.Error("a lower-cased object code was not matched")
	}
	if HasCode(err, CodeObjectExists) {
		t.Error("matched the wrong code")
	}
}

func TestAddRefusalCapabilityAbsent(t *testing.T) {
	var diags diag.Diagnostics
	AddRefusal(&diags, "Failed", "Vhost", &client.APIError{Code: CodeCapabilityAbsent, Message: "no", StatusCode: 409})
	if !diags.HasError() || !strings.Contains(diags.Errors()[0].Detail(), "cannot manage vhosts") ||
		!strings.Contains(diags.Errors()[0].Detail(), "will not help") {
		t.Fatalf("CAPABILITY_ABSENT must explain and say retrying will not help, got %v", diags)
	}
}

func TestIsTerminal(t *testing.T) {
	for status, want := range map[int]bool{
		400: true, 401: true, 403: true, 409: true, 404: false, 408: false, 429: false, 500: false, 503: false,
	} {
		if got := IsTerminal(&client.APIError{Code: "x", StatusCode: status}); got != want {
			t.Errorf("IsTerminal(%d) = %v, want %v", status, got, want)
		}
	}
	if !IsTerminal(Mismatch("requested %q", "a")) {
		t.Error("an identity mismatch must be terminal")
	}
	if IsTerminal(context.DeadlineExceeded) {
		t.Error("a transport error must be retried")
	}
}

// A refusal no retry can clear ends the wait at once instead of holding the
// per-instance lock to the deadline.
func TestWaitEndsAtOnceOnATerminalError(t *testing.T) {
	start := time.Now()
	calls := 0
	_, gone, err := Wait(context.Background(), time.Millisecond, time.Minute, "messaging vhost",
		func(context.Context) (*Object, error) {
			calls++
			return nil, &client.APIError{Code: "forbidden", Message: "no", StatusCode: http.StatusForbidden}
		},
		func(o *Object) string { return o.Status })
	if err == nil || gone || calls != 1 || time.Since(start) > 5*time.Second {
		t.Fatalf("err=%v gone=%v calls=%d after %s: want one call and an immediate error", err, gone, calls, time.Since(start))
	}
	if !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("the error must carry the refusal: %v", err)
	}
}

func TestWaitRetriesATransientError(t *testing.T) {
	calls := 0
	obj, _, err := Wait(context.Background(), time.Millisecond, time.Minute, "messaging vhost",
		func(context.Context) (*Object, error) {
			calls++
			if calls < 3 {
				return nil, &client.APIError{Code: "unavailable", StatusCode: http.StatusServiceUnavailable}
			}
			return &Object{Status: StatusReady}, nil
		},
		func(o *Object) string { return o.Status })
	if err != nil || obj == nil || calls != 3 {
		t.Fatalf("err=%v calls=%d: a 503 must be retried", err, calls)
	}
}

func TestInstanceIDValidatorsRefuseDotSegmentsAndSlashes(t *testing.T) {
	ctx := context.Background()
	for v, wantErr := range map[string]bool{
		"51455a51-db3d-4231-ac27-4fee2553c15f": false, ".": true, "..": true, "a/b": true, "": true,
	} {
		var diags diag.Diagnostics
		for _, val := range InstanceIDValidators() {
			resp := &validator.StringResponse{}
			val.ValidateString(ctx, validator.StringRequest{ConfigValue: types.StringValue(v)}, resp)
			diags.Append(resp.Diagnostics...)
		}
		if diags.HasError() != wantErr {
			t.Errorf("instance_id %q: error = %v, want %v", v, diags.HasError(), wantErr)
		}
	}
}
