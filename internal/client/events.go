package client

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"
)

// Event-driven waiting, over the gateway's tenant SSE stream (ADR-0014).
//
// WHAT THIS IS NOT: it is not "read the operation's result off the stream". The
// stream deliberately cannot do that. Its wire projection carries
// id/type/resourceType/resourceId/status/timestamp and NO operation id, and
// ADR-0014 amendment 7 considered adding a correlation id and rejected it --
// "for clients the stream is a SIGNAL; the operation endpoint is the record".
// Correlating by resourceId is worse than unsupported, it is ambiguous: during a
// create that field carries the provisioning REQUEST id, then the real resource
// id, then the request id again on a trailing superseded frame, with no
// discriminator saying which id space you are looking at.
//
// So this is the shape that ADR sanctions, and the shape the portal already uses
// for compute: the stream says SOMETHING CHANGED IN THIS TENANT, and the
// operation endpoint is then read once to find out what. What it removes is the
// TIMER, not the read.
//
// The frame's payload is therefore never parsed, and no filtering is done on
// resource type. That is deliberate rather than lazy: WaitForOperation is handed
// an operation id and genuinely does not know which resource type the operation
// belongs to, so a filter here could only be a guess -- and a wrong guess fails
// in the worst available way, as "push looked connected and simply never fired".
// A spurious wake costs one cheap GET. A missed wake costs a whole heartbeat.
const (
	// eventStreamSubpath is the gateway-local SSE route, tenant-scoped. The path
	// tenant is load-bearing (ADR-0052): without it the stream stays scoped to the
	// caller's HOME tenant and would deliver another tenant's events.
	eventStreamSubpath = "/events"

	// pushHeartbeat is the BACKSTOP re-read interval while the stream is live. It
	// is not the mechanism -- pushed events are -- it is what bounds the damage
	// when one is missed. Missing one is expected, not hypothetical: the gateway
	// caps a stream at 5 minutes and writes no `id:` line, so there is no
	// Last-Event-ID replay and any wait longer than that has blind windows.
	pushHeartbeat = 30 * time.Second

	// streamRetryDelay is how long to wait before reopening a stream that HAD
	// connected. The 5-minute lifetime cap makes that reconnection the NORMAL
	// path for a long wait, not an error path, so it is deliberately short.
	streamRetryDelay = 2 * time.Second

	// streamBackoffMax caps the retry delay for a stream that has NEVER
	// connected, and the two delays are separate for a reason worth stating.
	//
	// Reusing the 2-second reconnect delay for a gateway that answers 404 turns
	// a 30-minute wait into ~900 connection attempts -- strictly worse than the
	// timer poll this exists to replace, and invisible, because the wait still
	// succeeds. A stream that has never opened has no gap to repair and no news
	// to deliver, so backing off costs nothing: the fallback ticker owns the
	// pacing on that path.
	streamBackoffMax = 60 * time.Second
)

// eventWatcher turns the tenant SSE stream into wake-ups for a poller.
//
// It never surfaces an error. A stream that cannot be opened -- an older
// gateway, a buffering proxy, no free slot, a 426 version floor -- is a
// DEGRADATION to timer polling, not a failure of the operation being waited on.
// Failing an apply because a telemetry channel was unavailable is the wrong
// trade in every case.
type eventWatcher struct {
	client *Client
	// wake carries every reason to re-read the operation. Buffered at 1 and
	// written non-blockingly: a waiter that is already awake needs no second
	// nudge, and the watcher must never block on a slow reader.
	wake chan struct{}
	// connected reports whether a stream is currently established. Read by the
	// fallback ticker to decide whether it still needs to drive the poll.
	connected atomic.Bool
	// fallbackInterval is the cadence used while NOT connected -- the
	// pre-existing polling behaviour, preserved exactly.
	fallbackInterval time.Duration
}

// newEventWatcher starts watching in the background and returns the wake
// channel. The watcher stops when ctx is done.
func (c *Client) newEventWatcher(ctx context.Context, fallbackInterval time.Duration) <-chan struct{} {
	w := &eventWatcher{
		client:           c,
		wake:             make(chan struct{}, 1),
		fallbackInterval: fallbackInterval,
	}
	go w.runStream(ctx)
	go w.runFallbackTicker(ctx)
	return w.wake
}

// signal nudges the waiter. Non-blocking by design -- see eventWatcher.wake.
func (w *eventWatcher) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// runFallbackTicker preserves the old timer behaviour for as long as push is not
// established, and goes quiet once it is.
//
// It keeps ticking rather than stopping when connected: the check is made AT
// tick time, so a stream that drops mid-wait resumes timer polling within one
// interval, with no restart logic to get wrong.
func (w *eventWatcher) runFallbackTicker(ctx context.Context) {
	ticker := time.NewTicker(w.fallbackInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !w.connected.Load() {
				w.signal()
			}
		}
	}
}

// runStream keeps a stream open until ctx ends, reopening after each close.
//
// The two exit paths are NOT the same event and are not treated the same:
//
//   - A stream that had connected and ended: reopen promptly, and ALWAYS signal
//     first. That is the repair for the stream's missing replay -- whatever
//     happened during the gap is invisible, so the only safe assumption is that
//     something did.
//   - A stream that never connected: back off, and do NOT signal. There was no
//     gap to repair, nothing was delivered, and signalling here would quietly
//     re-time the poll to the retry delay while looking like push.
func (w *eventWatcher) runStream(ctx context.Context) {
	backoff := streamRetryDelay
	for ctx.Err() == nil {
		connected := w.readStreamOnce(ctx)
		w.connected.Store(false)

		delay := backoff
		if connected {
			// A working stream: reset, so one bad night does not leave a healthy
			// connection reopening a minute at a time for the rest of the wait.
			backoff = streamRetryDelay
			delay = streamRetryDelay
		} else if backoff < streamBackoffMax {
			backoff *= 2
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
			if connected {
				w.signal()
			}
		}
	}
}

// readStreamOnce opens one stream and pumps it until it ends. It reports whether
// the stream was ever established, which is what tells the caller apart a
// lifetime-capped reconnect from a gateway that will not serve this at all.
func (w *eventWatcher) readStreamOnce(ctx context.Context) bool {
	req, err := w.client.newStreamRequest(ctx)
	if err != nil {
		return false
	}
	resp, err := w.client.streamHTTPClient().Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	w.connected.Store(true)

	// Frames are small; bufio's default limit is ample. One that somehow exceeds
	// it ends the stream, which reconnects and re-reads -- degraded, never wrong.
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return true
		}
		line := scanner.Text()
		// Only the event NAME is inspected. A line starting with ':' is a
		// keepalive comment: it proves the connection is alive and carries no
		// news, and treating it as news would reinstate a 15-second timer poll
		// wearing a push costume.
		if name, ok := strings.CutPrefix(line, "event:"); ok {
			switch strings.TrimSpace(name) {
			case "resource", "resync":
				w.signal()
			}
		}
	}
	return true
}

// newStreamRequest builds the SSE request with this client's auth, whichever of
// the two paths it is on. The API-key path is explicitly supported by the
// gateway route: its OpenAPI operation declares no security override, so it
// inherits ApiKeyAuth, and its own 426 response exists only for non-browser
// clients like this one.
func (c *Client) newStreamRequest(ctx context.Context) (*http.Request, error) {
	u, err := url.Parse(c.baseURL)
	if err != nil {
		return nil, err
	}
	u.Path = path.Join(u.Path, c.TenantPath(eventStreamSubpath))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("User-Agent", c.userAgent)
	if c.version != "" {
		req.Header.Set(ProviderVersionHeader, c.version)
	}
	if c.bearer != nil {
		// Refresh proactively: a stream lives up to 5 minutes and cannot re-auth
		// mid-flight, and the gateway's deadline is min(lifetime, token.exp) -- so
		// a token near expiry buys a stream that dies early for no visible reason.
		if _, err := c.bearer.ensureFresh(ctx); err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.bearer.token())
	} else if c.apiKey != "" {
		req.Header.Set("X-API-Key", c.apiKey)
	}
	return req, nil
}

// streamHTTPClient returns a client with NO overall timeout, and the SAME
// redirect policy as every other request this client makes.
//
// The shared client carries a 60s Timeout, which is right for a request and
// fatal for a stream: it would sever every connection at 60 seconds, making push
// look permanently broken while the fallback quietly carried the whole wait --
// a regression that no test of the happy path would ever show. The context
// bounds the stream instead.
//
// CheckRedirect is carried over deliberately, and dropping it was a real defect
// in the first version of this file. Go's default policy FOLLOWS up to 10
// redirects and re-sends the request -- so a 302 or 307 on the stream would
// hand this caller's Authorization header or X-API-Key to a host the operator
// never named. Every other request from this client refuses that; a telemetry
// stream is not the place to make an exception.
func (c *Client) streamHTTPClient() *http.Client {
	return &http.Client{
		Transport:     c.httpClient.Transport,
		CheckRedirect: c.httpClient.CheckRedirect,
	}
}
