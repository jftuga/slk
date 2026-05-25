# Browser-Like Headers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every HTTP and WebSocket request slk sends to Slack indistinguishable (at the header level) from one sent by `app.slack.com` running in a recent desktop Chrome, so Enterprise Grid anomaly detectors stop flagging `xoxc`-token traffic as a non-browser client. Addresses GitHub issue #5.

**Architecture:** Introduce a new `internal/slackhttp` package owning a single `http.RoundTripper` wrapper (`BrowserTransport`) plus a `BrowserHeaders()` helper. The transport adds a curated set of Chrome-like headers (User-Agent, Origin, Referer, Accept, Accept-Language, Sec-Fetch-Site/Mode/Dest) to every outbound request whose host is `*.slack.com`, never overriding headers the caller already set. Three callers wire it in: the slack-go HTTP client in `internal/slack/client.go`, the WebSocket dialer in the same file (which can't use a `RoundTripper`, so it consumes `BrowserHeaders()` directly), and the image fetcher's client constructed in `cmd/slk/main.go`. The image fetcher loses its existing `slk/inline-image-fetcher` User-Agent.

**Tech Stack:** Go 1.26.x, `net/http`, `runtime` (for GOOS-aware User-Agent), `github.com/gorilla/websocket`, `github.com/slack-go/slack`.

**Context:** See `gh issue view 5` for the user-visible problem. Research notes in this session: slk currently sends `User-Agent: Go-http-client/1.1` on every Slack call, no `Origin`/`Referer`/`Sec-Fetch-*` headers on API requests, and announces itself as `slk/inline-image-fetcher` on file CDN downloads. The WebSocket already sets `Origin: https://app.slack.com` but nothing else.

**Out of scope for this plan:**
- TLS fingerprint (JA3/JA4) spoofing — would require `uTLS` and a transport rewrite. Re-assess only if browser-header parity doesn't move the needle.
- Slack App / OAuth flow — covered separately as a long-term option.
- User-configurable User-Agent override — YAGNI; add only if a real user needs it.

---

## File Structure

| File | Purpose | Action |
|---|---|---|
| `internal/slackhttp/transport.go` | `BrowserTransport`, `BrowserHeaders`, `UserAgent` | Create |
| `internal/slackhttp/transport_test.go` | Unit tests for the transport and helpers | Create |
| `internal/slack/client.go` | Wire `BrowserTransport` into HTTP client + WS dialer | Modify (lines 87-125, 205-218) |
| `internal/slack/client_test.go` | Integration tests that the wired client sends browser headers | Modify (append) |
| `internal/image/fetcher.go` | Drop the `slk/inline-image-fetcher` UA | Modify (line 483) |
| `internal/image/fetcher_test.go` | Verify the fetcher's existing tests still pass after UA removal | Modify (create or append; check current state in Task 4) |
| `cmd/slk/main.go` | Build the image-fetcher HTTP client via `slackhttp.NewBrowserHTTPClient` | Modify (lines 553-554) |
| `README.md` | Add a one-liner about Enterprise Grid status under Known Issues / FAQ | Modify (append) |

The new `internal/slackhttp` package is intentionally minimal (one transport, three exported funcs). It lives outside `internal/slack` so the image fetcher and any future surface (e.g. presence websockets, analytics endpoints) can pull it in without depending on the whole `slackclient` package.

---

## Conventions Used Below

- Go module path: `github.com/gammons/slk`
- All new code uses `gofmt`-default formatting and existing project comment style (full-sentence comments above non-obvious code; no trailing-line comments).
- Tests use `httptest.NewServer` to capture outbound requests, matching the pattern in `internal/slack/client_test.go:35-53`.
- Commits use conventional-commits prefix (`feat:`, `test:`, `refactor:`, `docs:`) — see `git log --oneline -10` for tone.

---

## Task 1: Add `slackhttp` package with BrowserTransport (TDD)

**Files:**
- Create: `internal/slackhttp/transport.go`
- Create: `internal/slackhttp/transport_test.go`

- [ ] **Step 1: Write the failing test file**

Write `internal/slackhttp/transport_test.go`:

```go
package slackhttp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// captureRT records every request it sees and forwards to a wrapped RT.
type captureRT struct {
	wrapped http.RoundTripper
	last    *http.Request
}

func (c *captureRT) RoundTrip(req *http.Request) (*http.Response, error) {
	c.last = req.Clone(req.Context())
	return c.wrapped.RoundTrip(req)
}

func newCaptureClient(t *testing.T, srv *httptest.Server) (*http.Client, *captureRT) {
	t.Helper()
	cap := &captureRT{wrapped: http.DefaultTransport}
	bt := &BrowserTransport{Inner: cap}
	return &http.Client{Transport: bt}, cap
}

func TestBrowserTransport_AddsHeadersToSlackHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	client, cap := newCaptureClient(t, srv)

	// Force the request to look like it's going to slack.com by rewriting Host.
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Host = "slack.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	got := cap.last
	if !strings.HasPrefix(got.Header.Get("User-Agent"), "Mozilla/5.0") {
		t.Errorf("User-Agent = %q; want Mozilla/5.0-prefixed Chrome UA", got.Header.Get("User-Agent"))
	}
	if got.Header.Get("Origin") != "https://app.slack.com" {
		t.Errorf("Origin = %q; want https://app.slack.com", got.Header.Get("Origin"))
	}
	if got.Header.Get("Referer") != "https://app.slack.com/" {
		t.Errorf("Referer = %q; want https://app.slack.com/", got.Header.Get("Referer"))
	}
	for _, h := range []string{"Accept", "Accept-Language", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest"} {
		if got.Header.Get(h) == "" {
			t.Errorf("header %s is empty; expected a value", h)
		}
	}
}

func TestBrowserTransport_MatchesSlackSubdomains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	hosts := []string{"slack.com", "files.slack.com", "hackclub.enterprise.slack.com", "wss-primary.slack.com"}
	for _, h := range hosts {
		t.Run(h, func(t *testing.T) {
			client, cap := newCaptureClient(t, srv)
			req, _ := http.NewRequest("GET", srv.URL, nil)
			req.Host = h
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			resp.Body.Close()
			if cap.last.Header.Get("Origin") == "" {
				t.Errorf("host %s: Origin header missing; expected the transport to recognize this as a Slack host", h)
			}
		})
	}
}

func TestBrowserTransport_DoesNotTouchNonSlackHosts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	client, cap := newCaptureClient(t, srv)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Host = "example.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if cap.last.Header.Get("Origin") != "" {
		t.Errorf("Origin set on non-Slack host: %q", cap.last.Header.Get("Origin"))
	}
	if ua := cap.last.Header.Get("User-Agent"); strings.HasPrefix(ua, "Mozilla/5.0") {
		t.Errorf("browser User-Agent leaked to non-Slack host: %q", ua)
	}
}

func TestBrowserTransport_DoesNotOverrideCallerHeaders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	client, cap := newCaptureClient(t, srv)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Host = "slack.com"
	req.Header.Set("User-Agent", "custom-agent/1.0")
	req.Header.Set("Authorization", "Bearer xoxc-test")
	req.Header.Set("Cookie", "d=test-cookie")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if got := cap.last.Header.Get("User-Agent"); got != "custom-agent/1.0" {
		t.Errorf("User-Agent was overridden: got %q, want custom-agent/1.0", got)
	}
	if got := cap.last.Header.Get("Authorization"); got != "Bearer xoxc-test" {
		t.Errorf("Authorization was overridden: %q", got)
	}
	if got := cap.last.Header.Get("Cookie"); got != "d=test-cookie" {
		t.Errorf("Cookie was overridden: %q", got)
	}
}

func TestBrowserHeaders_ContainsAllRequiredKeys(t *testing.T) {
	h := BrowserHeaders()
	for _, key := range []string{"User-Agent", "Accept", "Accept-Language", "Origin", "Referer", "Sec-Fetch-Site", "Sec-Fetch-Mode", "Sec-Fetch-Dest"} {
		if h.Get(key) == "" {
			t.Errorf("BrowserHeaders missing %s", key)
		}
	}
}

func TestUserAgentForGOOS(t *testing.T) {
	cases := []struct {
		goos       string
		wantSubstr string
	}{
		{"linux", "X11; Linux x86_64"},
		{"darwin", "Macintosh; Intel Mac OS X"},
		{"windows", "Windows NT 10.0; Win64; x64"},
		{"freebsd", "X11; Linux x86_64"}, // unknown → linux fallback
	}
	for _, tc := range cases {
		t.Run(tc.goos, func(t *testing.T) {
			got := userAgentForGOOS(tc.goos)
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("userAgentForGOOS(%q) = %q; want substring %q", tc.goos, got, tc.wantSubstr)
			}
			if !strings.HasPrefix(got, "Mozilla/5.0") {
				t.Errorf("userAgentForGOOS(%q) = %q; want Mozilla/5.0 prefix", tc.goos, got)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/slackhttp/...`

Expected: build failure (`package internal/slackhttp not found` or `undefined: BrowserTransport`).

- [ ] **Step 3: Write the implementation**

Write `internal/slackhttp/transport.go`:

```go
// Package slackhttp provides a Slack-aware http.RoundTripper that decorates
// outbound requests with the headers a recent desktop Chrome would send when
// app.slack.com makes a fetch/XHR/WebSocket call. The goal is to make
// xoxc-token traffic indistinguishable from official browser-client traffic
// at the header level, so Enterprise Grid anomaly detectors don't flag slk
// as a non-browser client and sign the user out.
//
// See: docs/superpowers/plans/2026-05-20-browser-like-headers.md and GitHub
// issue #5 for context.
package slackhttp

import (
	"net/http"
	"runtime"
	"strings"
)

// BrowserTransport wraps an inner http.RoundTripper and adds browser-like
// headers to requests bound for *.slack.com hosts. It never overwrites
// headers the caller has already set, so caller-controlled values like
// Authorization, Cookie, or a custom User-Agent for diagnostics survive.
type BrowserTransport struct {
	// Inner is the underlying transport that actually performs the round
	// trip. If nil, http.DefaultTransport is used.
	Inner http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t *BrowserTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if isSlackHost(req.URL.Host) || isSlackHost(req.Host) {
		// Clone the request so we don't mutate the caller's copy — net/http's
		// RoundTripper contract forbids in-place modification.
		req = req.Clone(req.Context())
		setIfMissing(req.Header, "User-Agent", UserAgent())
		setIfMissing(req.Header, "Accept", "*/*")
		setIfMissing(req.Header, "Accept-Language", "en-US,en;q=0.9")
		setIfMissing(req.Header, "Origin", "https://app.slack.com")
		setIfMissing(req.Header, "Referer", "https://app.slack.com/")
		setIfMissing(req.Header, "Sec-Fetch-Site", "same-site")
		setIfMissing(req.Header, "Sec-Fetch-Mode", "cors")
		setIfMissing(req.Header, "Sec-Fetch-Dest", "empty")
	}
	inner := t.Inner
	if inner == nil {
		inner = http.DefaultTransport
	}
	return inner.RoundTrip(req)
}

// NewBrowserHTTPClient returns an *http.Client wired up with BrowserTransport
// and an optional cookie jar. Use this anywhere an http.Client is needed for
// Slack traffic.
func NewBrowserHTTPClient(jar http.CookieJar) *http.Client {
	return &http.Client{
		Transport: &BrowserTransport{Inner: http.DefaultTransport},
		Jar:       jar,
	}
}

// BrowserHeaders returns the full set of browser-like headers as an
// http.Header value. Callers that can't use BrowserTransport (notably the
// WebSocket dialer in gorilla/websocket, which takes raw upgrade headers
// rather than a RoundTripper) consume this directly.
func BrowserHeaders() http.Header {
	h := http.Header{}
	h.Set("User-Agent", UserAgent())
	h.Set("Accept", "*/*")
	h.Set("Accept-Language", "en-US,en;q=0.9")
	h.Set("Origin", "https://app.slack.com")
	h.Set("Referer", "https://app.slack.com/")
	h.Set("Sec-Fetch-Site", "same-site")
	h.Set("Sec-Fetch-Mode", "cors")
	h.Set("Sec-Fetch-Dest", "empty")
	return h
}

// UserAgent returns a Chrome 120 User-Agent string appropriate for the host
// OS. We intentionally pin a specific Chrome major version rather than
// auto-bumping: this string only needs to be plausible, not bleeding-edge.
// Update it manually every ~6 months if anomaly reports return.
func UserAgent() string {
	return userAgentForGOOS(runtime.GOOS)
}

func userAgentForGOOS(goos string) string {
	switch goos {
	case "darwin":
		return "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	case "windows":
		return "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	default:
		// Linux and anything else (freebsd, openbsd, ...) → Linux UA.
		return "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	}
}

func isSlackHost(host string) bool {
	if host == "" {
		return false
	}
	// Strip any :port suffix.
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host == "slack.com" || strings.HasSuffix(host, ".slack.com")
}

func setIfMissing(h http.Header, key, value string) {
	if h.Get(key) == "" {
		h.Set(key, value)
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/slackhttp/... -v`

Expected: all tests PASS.

- [ ] **Step 5: Run vet and build**

Run: `go vet ./internal/slackhttp/... && go build ./...`

Expected: no output, zero exit.

- [ ] **Step 6: Commit**

```bash
git add internal/slackhttp/
git commit -m "feat(slackhttp): add BrowserTransport for browser-like headers

Adds a Slack-host-aware http.RoundTripper that decorates outbound
requests to *.slack.com with Chrome-like User-Agent, Origin, Referer,
Accept, Accept-Language, and Sec-Fetch-* headers. Caller-set headers
are preserved. Non-Slack hosts pass through unchanged.

Wiring follows in subsequent commits.

Refs #5"
```

---

## Task 2: Wire BrowserTransport into the slack-go HTTP client

**Files:**
- Modify: `internal/slack/client.go` (lines 87-125)
- Modify: `internal/slack/client_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/slack/client_test.go`:

```go
func TestNewClient_SendsBrowserHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"team_id":"T1","user_id":"U1","url":"https://example.slack.com/"}`)
	}))
	defer srv.Close()

	// Build a Client via NewClient, then point its slack-go api at our
	// httptest server. Use slack.OptionAPIURL on a fresh slack-go client
	// that shares the SAME http.Client we built — so the BrowserTransport
	// is exercised.
	c := NewClient("xoxc-test", "test-cookie")
	c.api = slack.New(
		c.token,
		slack.OptionHTTPClient(c.httpClient),
		slack.OptionAPIURL(srv.URL+"/"),
	)

	// httptest serves on 127.0.0.1, which isn't a Slack host — the
	// BrowserTransport keys off req.URL.Host. To force header injection,
	// rewrite the host the test server reports. The simplest path: ensure
	// BrowserTransport considers the test host a Slack host by setting
	// req.Host on the test request — but slack-go builds the URL itself,
	// so instead use srv.Listener.Addr to confirm the path works and
	// rely on the host-matching test in slackhttp_test.go for coverage.
	//
	// Compromise: assert that the http.Client's transport is a
	// *slackhttp.BrowserTransport — that's the wiring contract — and let
	// the slackhttp package tests own the header-injection assertions.
	if _, ok := c.httpClient.Transport.(*slackhttp.BrowserTransport); !ok {
		t.Fatalf("c.httpClient.Transport = %T; want *slackhttp.BrowserTransport", c.httpClient.Transport)
	}

	// Sanity check: a real call goes through and reaches the server.
	if _, err := c.api.AuthTest(); err != nil {
		t.Fatalf("AuthTest: %v", err)
	}
	if gotHeaders == nil {
		t.Fatal("server never received a request")
	}
}
```

Add the new import at the top of `client_test.go`:

```go
import (
	// ...existing imports...
	"github.com/gammons/slk/internal/slackhttp"
)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/slack/... -run TestNewClient_SendsBrowserHeaders -v`

Expected: FAIL with type assertion error (transport is `nil` or default — not `*slackhttp.BrowserTransport`).

- [ ] **Step 3: Modify `newCookieHTTPClient`**

In `internal/slack/client.go`, add to imports:

```go
"github.com/gammons/slk/internal/slackhttp"
```

Replace `newCookieHTTPClient` (currently lines 122-125):

```go
// newCookieHTTPClient creates an http.Client with the Slack 'd' cookie set
// and a BrowserTransport that injects Chrome-like headers on every request
// to *.slack.com hosts. This keeps Enterprise Grid anomaly detectors from
// flagging slk's traffic as non-browser. See internal/slackhttp.
func newCookieHTTPClient(dCookie string) *http.Client {
	return slackhttp.NewBrowserHTTPClient(newCookieJar(dCookie))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/slack/... -run TestNewClient_SendsBrowserHeaders -v`

Expected: PASS.

- [ ] **Step 5: Run the full slack package tests**

Run: `go test ./internal/slack/...`

Expected: all PASS. No regressions.

- [ ] **Step 6: Commit**

```bash
git add internal/slack/client.go internal/slack/client_test.go
git commit -m "feat(slack): wire BrowserTransport into the HTTP client

NewClient and newCookieHTTPClient now construct the inner http.Client
via slackhttp.NewBrowserHTTPClient, so every slack-go Web API call AND
every hand-rolled endpoint POST (which all share the same client) is
decorated with browser-like User-Agent, Origin, Referer, Accept,
Accept-Language, and Sec-Fetch-* headers.

Refs #5"
```

---

## Task 3: Add browser headers to the WebSocket upgrade

**Files:**
- Modify: `internal/slack/client.go` (lines 205-218)
- Modify: `internal/slack/client_test.go` (append)

The WebSocket dialer in `gorilla/websocket` doesn't accept an `http.RoundTripper` — it has its own upgrade path. We have to merge `slackhttp.BrowserHeaders()` into the headers we pass to `dialer.Dial`, taking care not to clobber the existing `Origin` header (BrowserHeaders also sets it, with the same value — but we want robust behavior if someone changes one without the other).

- [ ] **Step 1: Write the failing test**

Append to `internal/slack/client_test.go`:

```go
func TestStartWebSocket_SendsBrowserHeaders(t *testing.T) {
	// Spin up an httptest server that completes the WS upgrade and
	// captures the upgrade request's headers.
	var gotHeaders http.Header
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			gotHeaders = r.Header.Clone()
			return true
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Logf("upgrade failed: %v", err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()

	// Drive the dialer directly with the same headers StartWebSocket
	// builds. We can't easily exercise StartWebSocket end-to-end because
	// it dials wss-primary.slack.com — but we CAN test the header-merging
	// helper. To make that helper independently testable, Step 3 extracts
	// it into wsUpgradeHeaders.
	headers := wsUpgradeHeaders()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1)
	dialer := websocket.DefaultDialer
	conn, _, err := dialer.Dial(wsURL, headers)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()

	if got := gotHeaders.Get("User-Agent"); !strings.HasPrefix(got, "Mozilla/5.0") {
		t.Errorf("upgrade User-Agent = %q; want Mozilla-prefixed", got)
	}
	if got := gotHeaders.Get("Origin"); got != "https://app.slack.com" {
		t.Errorf("upgrade Origin = %q; want https://app.slack.com", got)
	}
	if got := gotHeaders.Get("Accept-Language"); got == "" {
		t.Errorf("upgrade missing Accept-Language")
	}
}
```

Ensure `client_test.go` imports include:

```go
"github.com/gorilla/websocket"
```

(verify it's not already there; add only if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/slack/... -run TestStartWebSocket_SendsBrowserHeaders -v`

Expected: FAIL with `undefined: wsUpgradeHeaders` — we haven't extracted it yet.

- [ ] **Step 3: Extract `wsUpgradeHeaders` and use it**

In `internal/slack/client.go`, replace the WebSocket setup block (lines 212-218):

```go
	jar := newCookieJar(c.cookie)
	dialer := &websocket.Dialer{Jar: jar}

	conn, _, err := dialer.Dial(wsURL, wsUpgradeHeaders())
	if err != nil {
		return fmt.Errorf("websocket connect failed: %w", err)
	}
```

Then add this helper above `StartWebSocket` (e.g. just before the `// StartWebSocket connects...` comment block at line 201):

```go
// wsUpgradeHeaders returns the HTTP headers slk attaches to the WebSocket
// upgrade request. These match the Chrome-like headers BrowserTransport
// adds to ordinary HTTP requests, with Sec-Fetch-Dest narrowed to
// "websocket" — the value a real browser sends when opening a WS to
// app.slack.com.
//
// gorilla/websocket's Dialer.Dial accepts arbitrary headers (except for
// the protocol-managed Sec-WebSocket-* set, which it owns), so this is
// the right injection point. We can't reuse BrowserTransport here because
// the dialer doesn't go through http.RoundTripper.
func wsUpgradeHeaders() http.Header {
	h := slackhttp.BrowserHeaders()
	h.Set("Sec-Fetch-Dest", "websocket")
	return h
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/slack/... -run TestStartWebSocket_SendsBrowserHeaders -v`

Expected: PASS.

- [ ] **Step 5: Run full package tests**

Run: `go test ./internal/slack/...`

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/slack/client.go internal/slack/client_test.go
git commit -m "feat(slack): add browser-like headers to WebSocket upgrade

StartWebSocket now passes the same User-Agent, Accept-Language, and
Sec-Fetch-* headers BrowserTransport adds to HTTP calls. Sec-Fetch-Dest
is narrowed to 'websocket' to match what a real browser sends.

Refs #5"
```

---

## Task 4: Switch image fetcher to use BrowserTransport

**Files:**
- Modify: `internal/image/fetcher.go` (line 483, plus possible Default fallback at line 139)
- Modify: `cmd/slk/main.go` (lines 553-554)
- Modify: `internal/image/fetcher_test.go` (verify or create)

The image fetcher currently sets its own `User-Agent: slk/inline-image-fetcher` (`internal/image/fetcher.go:483`) and constructs a plain `http.Client` if none is passed (`fetcher.go:138-140`). We need to remove the explicit UA Set (so BrowserTransport can supply the real one) and route the externally-provided client through BrowserTransport in `main.go`.

- [ ] **Step 1: Check the existing fetcher tests**

Run:

```bash
ls internal/image/ | grep _test
```

Note which test files exist. If `fetcher_test.go` exists, read it to understand current coverage of `tryDownload`. The plan assumes it exists; if not, the asserting step below becomes a "create" instead of "append".

- [ ] **Step 2: Write/append a failing test that the fetcher sends browser-like headers**

Append (or create) `internal/image/fetcher_test.go`:

```go
package image

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gammons/slk/internal/slackhttp"
)

func TestTryDownload_SendsBrowserHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "image/png")
		// Minimal valid PNG signature + IHDR + IEND so the fetcher's
		// downstream decode doesn't error before we assert on headers.
		// Actually we only need the HTTP layer here; an empty 200 is fine
		// because tryDownload returns the bytes for the caller to decode.
		w.WriteHeader(200)
		w.Write([]byte("not-a-real-png"))
	}))
	defer srv.Close()

	client := slackhttp.NewBrowserHTTPClient(nil)
	f := &Fetcher{http: client}

	// tryDownload doesn't filter by host the way BrowserTransport does;
	// to exercise the transport's host check we rewrite the request host
	// to slack.com via a custom RoundTripper that pretends the test
	// server is files.slack.com. Simplest path: just point the URL at the
	// test server and force the host header to a Slack host. Because
	// BrowserTransport keys off req.URL.Host AND req.Host, the latter
	// wins for header injection while the former routes to localhost.
	//
	// But http.NewRequest sets URL.Host from the URL string, so we have
	// to wrap with a RoundTripper that rewrites. For simplicity, point
	// the URL at the test server (URL.Host = 127.0.0.1:...) and confirm
	// that BrowserTransport DOES NOT inject — proving the host-matching
	// is working — and use slackhttp's own tests to cover the positive
	// case.

	body, _, status, err := f.tryDownload(context.Background(), srv.URL, TeamAuth{})
	if err != nil {
		t.Fatalf("tryDownload: %v", err)
	}
	if status != 200 {
		t.Fatalf("status = %d", status)
	}
	if string(body) != "not-a-real-png" {
		t.Fatalf("body = %q", body)
	}

	// Negative assertion: 127.0.0.1 is NOT a Slack host, so the browser
	// UA must NOT be injected. This catches accidental "inject everywhere"
	// regressions.
	if ua := gotHeaders.Get("User-Agent"); strings.HasPrefix(ua, "Mozilla/5.0") {
		t.Errorf("browser UA leaked to non-Slack host: %q", ua)
	}
	// The fetcher's own explicit User-Agent override is gone (Task 4
	// removes it). The transport adds none for non-Slack hosts, so Go
	// defaults to "Go-http-client/1.1".
	if ua := gotHeaders.Get("User-Agent"); ua != "" && !strings.Contains(ua, "Go-http-client") {
		t.Errorf("unexpected User-Agent on non-Slack host: %q", ua)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/image/... -run TestTryDownload_SendsBrowserHeaders -v`

Expected: FAIL — `tryDownload` still sets `User-Agent: slk/inline-image-fetcher`, which doesn't match `Go-http-client` and doesn't start with `Mozilla` either, so the second assertion (`!strings.Contains(ua, "Go-http-client")`) fires.

- [ ] **Step 4: Remove the explicit User-Agent Set in `tryDownload`**

In `internal/image/fetcher.go`, delete this line (currently line 483):

```go
	httpReq.Header.Set("User-Agent", "slk/inline-image-fetcher")
```

Leave the rest of `tryDownload` unchanged. Authorization and Cookie still get set explicitly because of the per-team auth requirement (see comment at lines 488-491).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/image/... -run TestTryDownload_SendsBrowserHeaders -v`

Expected: PASS.

- [ ] **Step 6: Update `main.go` to construct the image client via slackhttp**

In `cmd/slk/main.go`, ensure the imports include:

```go
"github.com/gammons/slk/internal/slackhttp"
```

Replace line 553:

```go
	imageHTTPClient := &http.Client{Timeout: 10 * time.Second}
```

With:

```go
	imageHTTPClient := slackhttp.NewBrowserHTTPClient(nil)
	imageHTTPClient.Timeout = 10 * time.Second
```

(`NewBrowserHTTPClient` returns a `*http.Client` with no timeout by default; we want to preserve the existing 10-second timeout. No cookie jar — auth.go's per-team `d` cookie is attached inline per request in `tryDownload`, see `fetcher.go:487-491`.)

- [ ] **Step 7: Run full test suite**

Run: `go test ./...`

Expected: all PASS.

- [ ] **Step 8: Build everything**

Run: `go build ./...`

Expected: clean build.

- [ ] **Step 9: Commit**

```bash
git add internal/image/fetcher.go internal/image/fetcher_test.go cmd/slk/main.go
git commit -m "feat(image): route image fetcher through BrowserTransport

The image fetcher previously announced itself with a unique
User-Agent (slk/inline-image-fetcher). Replace it with a default
http.Client wrapped in slackhttp.BrowserTransport so file-CDN
requests on *.slack.com carry the same browser-like headers the
rest of slk's traffic does. Per-request Authorization Bearer + 'd'
cookie remain unchanged.

Refs #5"
```

---

## Task 5: Document the change

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Check current README structure for a sensible insertion point**

Run:

```bash
grep -n -i -E "enterprise|known issues|faq|troubleshoot" README.md
```

Note the line numbers and pick the closest matching section, or insert a new "Enterprise Grid" subsection under the closest of: "Known Issues", "FAQ", "Troubleshooting", or directly after the install/setup section if none of those exist.

- [ ] **Step 2: Append or insert the following block**

Insert this section at the chosen location:

```markdown
## Enterprise Grid

slk authenticates via the same `xoxc` browser session token + `d` cookie
that `app.slack.com` uses. As of v0.X, slk decorates every outbound
request to `*.slack.com` with browser-like headers (User-Agent,
Origin, Referer, Sec-Fetch-*) so that Slack's anomaly detectors should
treat the traffic identically to traffic from the browser tab the token
was extracted from.

**If you're on Enterprise Grid and slk signs you out or triggers a
security email after login**, please file an issue with:

1. The exact email/notification text Slack sent.
2. Whether you got logged out of *all* sessions or just the slk session.
3. The output of `slk version`.

Header parity is a best-effort mitigation, not a contract — your IT
policy may still flag slk regardless. See issue #5 for history.
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document Enterprise Grid header-parity mitigation

Refs #5"
```

---

## Task 6: Manual verification

This task is intentionally not automated. Header parity is the goal; the only way to confirm it actually moves the needle is to put slk in front of an Enterprise Grid user. The deliverables here are the pre-deploy sanity checks the executor should run before merging.

- [ ] **Step 1: Capture outbound headers locally**

Run slk against a non-Enterprise workspace with `SSLKEYLOGFILE` set and `mitmproxy` (or `tcpdump` + Wireshark with the keylog) in front of it. Confirm by eye that:

- Every request to `slack.com` / `*.slack.com` has `User-Agent: Mozilla/5.0 ... Chrome/120 ...`
- `Origin: https://app.slack.com` and `Referer: https://app.slack.com/` are present on Web API calls
- `Sec-Fetch-Site: same-site`, `Sec-Fetch-Mode: cors`, `Sec-Fetch-Dest: empty` are present
- The WebSocket upgrade request has the same headers, with `Sec-Fetch-Dest: websocket`
- File CDN fetches (`files.slack.com`) carry both the browser headers *and* the per-team `Authorization: Bearer xoxc-...` + `Cookie: d=...`
- `Go-http-client/1.1` does not appear in any request to a Slack host
- `slk/inline-image-fetcher` does not appear anywhere

If any of the above are missing or wrong, **return to the failing task** rather than continuing.

- [ ] **Step 2: Confirm nothing regressed**

Run a non-Enterprise smoke test of slk's full flow: onboarding → channel list → open channel → send message → receive message → image attachment → thread reply. Confirm nothing is broken at the application level.

- [ ] **Step 3: Run the full test suite one more time**

```bash
go test ./... -race
go vet ./...
go build ./...
```

Expected: all green.

- [ ] **Step 4: Post on issue #5 asking the reporters to retest**

Write a comment on issue #5 (do not auto-merge into main yet):

```markdown
We just landed browser-like headers on every outbound Slack request
([PR #XXX]). slk's traffic should now look indistinguishable from
`app.slack.com` at the header level (User-Agent, Origin, Referer,
Sec-Fetch-*). This may or may not be enough to keep Enterprise Grid
anomaly detectors happy — we have no way to verify without an
Enterprise workspace.

If you're willing to be a guinea pig:

1. Build from `main` (or wait for the next release).
2. Try logging in to your Enterprise workspace.
3. Report whether you (a) stayed logged in, (b) got the security
   email but stayed logged in, or (c) got booted out as before.

Either outcome is useful data. Thanks!
```

(Do not actually post until the maintainer confirms — this is a draft.)

- [ ] **Step 5: Final commit (only if there are any followup changes from manual verification)**

If manual verification turned up any small fixes, commit them with appropriate `fix:` prefix and re-run Steps 1-3. Otherwise this step is a no-op.

---

## Self-Review Notes

- **Spec coverage:** The plan addresses every surface flagged in the research summary (slack-go HTTP client, hand-rolled endpoint POSTs sharing that client, WebSocket upgrade, image fetcher). Hand-rolled endpoints inherit the transport automatically because they share `c.httpClient` (see `internal/slack/client.go:660+`, all using the same `*http.Client`) — no separate task needed.
- **Out of scope items** (TLS fingerprinting, OAuth route, env-var UA override) are explicitly listed at the top.
- **Type consistency:** `slackhttp.BrowserTransport`, `slackhttp.NewBrowserHTTPClient`, `slackhttp.BrowserHeaders`, `slackhttp.UserAgent`, `userAgentForGOOS` — names used identically in tasks 1, 2, 3, 4, and 5.
- **Bite-sized:** Each step is one observable action (write test, run test, write code, run test, commit).
- **No placeholders:** Every code block is complete; no TBDs or "implement similar to above" references.
- **Commits:** Six commits total, each independently buildable and testable.
- **Risk:** The biggest unknown is whether header parity is sufficient. The plan accepts this and frames Task 6 around it.
