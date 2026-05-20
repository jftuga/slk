# Manual verification notes — browser-like-headers

Companion to `2026-05-20-browser-like-headers.md`. Records what was
verified automatically vs. what still needs human eyes before merge.

## Automatically verified

- `go test ./... -race` — all packages pass
- `go vet ./...` — clean
- `go build ./...` — clean
- No stray `slk/inline-image-fetcher` references in production code
  (only in `internal/image/fetcher_test.go` where they belong: a negative
  assertion that the string is absent on the wire)
- BrowserTransport unit tests (`internal/slackhttp/transport_test.go`)
  cover positive case (Slack-host requests get all 8 headers) and
  negative case (non-Slack hosts are untouched)
- WebSocket upgrade headers test (`internal/slack/client_test.go::TestStartWebSocket_SendsBrowserHeaders`)
  confirms the upgrade carries User-Agent, Origin, Accept-Language,
  and Sec-Fetch-Dest=websocket
- Type-wiring tests confirm both the slack-go HTTP client and the
  image-fetcher HTTP client use `*slackhttp.BrowserTransport`

## Still requires human verification before merge

These need a real Slack workspace and a packet inspector (mitmproxy,
Wireshark with `SSLKEYLOGFILE`, or Burp). Cannot be done from CI or by
an agent.

### 1. Wire-level header inspection

Run slk against a non-Enterprise workspace with mitmproxy in front of
it. Confirm by eye every request to `slack.com` / `*.slack.com` has:

- `User-Agent: Mozilla/5.0 ... Chrome/120 ...` (per-OS)
- `Origin: https://app.slack.com`
- `Referer: https://app.slack.com/`
- `Accept: */*`
- `Accept-Language: en-US,en;q=0.9`
- `Sec-Fetch-Site: same-site`
- `Sec-Fetch-Mode: cors`
- `Sec-Fetch-Dest: empty` (or `websocket` on the WS upgrade)

And confirm:

- `Go-http-client/1.1` does not appear on any Slack-host request
- `slk/inline-image-fetcher` does not appear anywhere
- File CDN fetches (`files.slack.com`) carry both browser headers *and*
  the per-team `Authorization: Bearer xoxc-...` + `Cookie: d=...`

If any of the above is missing or wrong, **stop and fix** before merging.

### 2. Functional smoke test

Run the full slk flow against the workspace: onboarding → channel list →
open channel → send message → receive message → image attachment →
thread reply. Nothing should be broken at the application level.

### 3. Enterprise Grid retest

After merge, post on issue #5 asking the reporters (focusaurus, raff,
antoszka) to retest. Draft below.

## Draft issue #5 comment

```markdown
**Update — browser-like headers landed in `main`**

We just landed [PR/commit range] which decorates every outbound Slack
request with browser-like headers:

- `User-Agent: Mozilla/5.0 ... Chrome/120 ...` (per-OS)
- `Origin: https://app.slack.com`, `Referer: https://app.slack.com/`
- `Accept: */*`, `Accept-Language: en-US,en;q=0.9`
- `Sec-Fetch-Site: same-site`, `Sec-Fetch-Mode: cors`, `Sec-Fetch-Dest: empty`
  (or `websocket` on the upgrade)

slk's traffic should now look indistinguishable from `app.slack.com` at
the header level. We can't verify this fixes the Enterprise Grid issue
because we don't have an Enterprise Grid workspace to test against.

If you're willing to retest:

1. Build from `main` (or wait for the next release).
2. Try adding your Enterprise workspace.
3. Report whether you (a) stayed logged in, (b) got the security email
   but stayed logged in, or (c) got booted out exactly as before.

Either outcome is useful data. If header parity isn't enough, the next
escalation is TLS fingerprint matching (uTLS), which is significantly
more invasive — so knowing whether this moves the needle at all is
important.

Thanks for your patience.
```

(Do NOT auto-post. Maintainer reviews and posts manually after
verification step 1.)
