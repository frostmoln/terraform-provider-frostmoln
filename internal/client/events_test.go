package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// The tests below all use a fallback interval FAR LONGER than the assertion
// window. That is the whole design of them: if the timer could have driven the
// wait, none of these could tell push from polling, and the suite would stay
// green after someone deleted the stream entirely.

// sseOperationServer serves both halves a waiter needs: the operations endpoint
// and the tenant event stream. stream is called with the response writer once a
// client connects, and controls what that connection does.
func sseOperationServer(t *testing.T, gets *atomic.Int32, status func(n int32) string, stream func(w http.ResponseWriter, r *http.Request, flush func())) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tenants/t-1/operations/op-1":
			n := gets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Operation{OperationID: "op-1", Status: status(n), ResourceID: "db-1"})

		case "/v1/tenants/t-1/events":
			if stream == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fl, ok := w.(http.Flusher)
			if !ok {
				t.Error("test server response writer cannot flush; the SSE tests are meaningless without it")
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			fl.Flush()
			stream(w, r, fl.Flush)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newWaitClient(url string) *Client {
	c := NewClient(url, "test-key") // pragma: allowlist secret
	c.SetTenantIDForTest("t-1")
	return c
}

// TestWaitForOperationIsDrivenByPush is the point of the whole change: a pushed
// event resolves the wait, and the timer is not what got there first.
//
// The fallback interval is 10s and the backstop is pushHeartbeat (30s), so a
// return inside two seconds cannot have come from either.
func TestWaitForOperationIsDrivenByPush(t *testing.T) {
	var gets atomic.Int32
	// Terminal only from the SECOND read on, so a wait that never wakes cannot
	// pass by reading the answer on its initial poll.
	srv := sseOperationServer(t, &gets, func(n int32) string {
		if n >= 2 {
			return OperationStatusCompleted
		}
		return "running"
	}, func(w http.ResponseWriter, r *http.Request, flush func()) {
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, "event: resource\ndata: {\"type\":\"database.created\",\"resourceType\":\"database\",\"resourceId\":\"db-1\",\"status\":\"running\"}\n\n")
		flush()
		// Hold the connection open until the waiter tears its watcher down, rather
		// than sleeping: a fixed sleep makes httptest.Server.Close block on this
		// handler long after the assertion has been made.
		<-r.Context().Done()
	})
	defer srv.Close()

	start := time.Now()
	op, err := newWaitClient(srv.URL).WaitForOperation(context.Background(), "op-1", 10*time.Second, 5*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("WaitForOperation failed: %v", err)
	}
	if op.ResourceID != "db-1" {
		t.Errorf("expected resourceId db-1, got %s", op.ResourceID)
	}
	if elapsed > 2*time.Second {
		t.Errorf("took %v: the fallback interval is 10s and the backstop is %v, so this wait was NOT resolved by the pushed event", elapsed, pushHeartbeat)
	}
	if n := gets.Load(); n != 2 {
		t.Errorf("read the operation %d times, want exactly 2 (once at entry, once on the pushed event): a higher count means something is still polling on a timer", n)
	}
}

// TestWaitForOperationFallsBackWhenStreamUnavailable pins the degradation. An
// older gateway, a buffering proxy or a version floor must cost latency, never
// the apply.
func TestWaitForOperationFallsBackWhenStreamUnavailable(t *testing.T) {
	var gets atomic.Int32
	srv := sseOperationServer(t, &gets, func(n int32) string {
		if n >= 3 {
			return OperationStatusCompleted
		}
		return "running"
	}, nil) // 404 on /events
	defer srv.Close()

	op, err := newWaitClient(srv.URL).WaitForOperation(context.Background(), "op-1", 50*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatalf("a wait whose event stream is unavailable must still complete on the timer, got: %v", err)
	}
	if op.Status != OperationStatusCompleted {
		t.Errorf("expected completed, got %s", op.Status)
	}
	if n := gets.Load(); n < 3 {
		t.Errorf("read the operation %d times, want at least 3: the fallback timer did not drive the poll", n)
	}
}

// TestKeepaliveDoesNotWakeTheWaiter is the test that stops this becoming a
// 15-second timer poll wearing a push costume. The gateway sends a `: keepalive`
// comment every 15s on an idle stream; treating one as news would mean every
// idle stream polls the operation endpoint forever.
func TestKeepaliveDoesNotWakeTheWaiter(t *testing.T) {
	var gets atomic.Int32
	srv := sseOperationServer(t, &gets, func(int32) string { return "running" },
		func(w http.ResponseWriter, r *http.Request, flush func()) {
			for i := 0; i < 20; i++ {
				fmt.Fprint(w, ": keepalive\n\n")
				flush()
				time.Sleep(20 * time.Millisecond)
			}
			<-r.Context().Done()
		})
	defer srv.Close()

	// Never completes: the assertion is about what did NOT happen before the
	// deadline, so a timeout error here is the expected outcome.
	_, err := newWaitClient(srv.URL).WaitForOperation(context.Background(), "op-1", 10*time.Second, 1500*time.Millisecond)
	if err == nil {
		t.Fatal("expected a timeout for an operation that never completes")
	}
	if n := gets.Load(); n != 1 {
		t.Errorf("read the operation %d times, want exactly 1 (the entry read): keepalive comments are being treated as events, which turns an idle stream into a 15-second poll", n)
	}
}

// TestReconnectWakesTheWaiter covers the stream's missing replay. The gateway
// caps a connection at 5 minutes and writes no `id:` line, so a long wait has
// blind windows by construction -- and the only safe assumption on reconnect is
// that something happened during the gap.
func TestReconnectWakesTheWaiter(t *testing.T) {
	var gets atomic.Int32
	srv := sseOperationServer(t, &gets, func(n int32) string {
		if n >= 2 {
			return OperationStatusCompleted
		}
		return "running"
	}, func(_ http.ResponseWriter, _ *http.Request, _ func()) {
		// Connect and immediately end the stream, delivering no event at all --
		// exactly what the lifetime cap does mid-wait.
	})
	defer srv.Close()

	start := time.Now()
	op, err := newWaitClient(srv.URL).WaitForOperation(context.Background(), "op-1", 30*time.Second, 10*time.Second)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("WaitForOperation failed: %v", err)
	}
	if op.Status != OperationStatusCompleted {
		t.Errorf("expected completed, got %s", op.Status)
	}
	// One streamRetryDelay plus scheduling slack; the 30s fallback and the 30s
	// backstop are both far outside this window.
	if elapsed > streamRetryDelay+3*time.Second {
		t.Errorf("took %v: the reconnect did not trigger a re-read, so events missed across a reconnect gap are lost until the backstop", elapsed)
	}
}

// TestUnavailableStreamBacksOff is the test that was missing when this code
// first passed its own mutation check, and its absence hid a real defect.
//
// A stream that never connects has no gap to repair, so retrying it on the short
// RECONNECT delay turns a 30-minute wait into ~900 connection attempts against a
// gateway that has already said no -- strictly worse than the timer poll this
// replaces, and silent, because the wait still succeeds on the fallback.
func TestUnavailableStreamBacksOff(t *testing.T) {
	var gets, attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tenants/t-1/operations/op-1":
			gets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(Operation{OperationID: "op-1", Status: "running"})
		case "/v1/tenants/t-1/events":
			attempts.Add(1)
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	// Never completes; the assertions are about the shape of the traffic.
	_, _ = newWaitClient(srv.URL).WaitForOperation(context.Background(), "op-1", 200*time.Millisecond, 10*time.Second)

	// Doubling from 2s reaches ~3 attempts in 10 seconds. A flat retry at
	// streamRetryDelay would reach 6.
	if n := attempts.Load(); n > 4 {
		t.Errorf("attempted the event stream %d times in 10s: a stream that has never connected is not backing off, so a long wait hammers a gateway that already refused it", n)
	}
	// And the fallback must still be carrying the wait meanwhile.
	if n := gets.Load(); n < 10 {
		t.Errorf("read the operation only %d times in 10s at a 200ms fallback interval: the timer is not driving the poll while push is unavailable", n)
	}
}
