# subscriptions.thread.getView — observed contract

Raw cURL examples and a sample response live in
`docs/superpowers/specs/2026-05-12-subscriptions-thread-design.md` lines
436–1825. This file is the distilled factual reference the
implementation tasks read.

## Endpoint
- Method:           POST
- URL path:         `api/subscriptions.thread.getView`           (relative to the workspace base URL, e.g. `https://<workspace>.slack.com/`)
- Required headers: `Authorization: Bearer <xoxc>` + cookie `d=<dxxx>`. The official browser client also sends the token as a form field (`token`) inside a multipart body; slk continues to use the `Bearer` header form via the existing `postForm` helper.
- Content-type slk should send: `application/x-www-form-urlencoded` (same as every other `*.mark` / `*.list` call slk already makes). The official client uses multipart only because it's a browser idiom; the endpoint accepts urlencoded too.

## Request form fields
Required (slk should always send):
- `limit`               — page size. The official client uses `8`. slk will request `100` to minimise round trips.
- `fetch_threads_state` — `true`. Causes the response to include the top-level `threads_state` block (workspace-wide unread/mention counts). We don't need that block for v2, but the flag is cheap to set and matches the official client's behaviour.
- `priority_mode`       — `all`. Other values (e.g. "important") would filter the result; we want every subscribed thread.

Pagination (subsequent pages only):
- `current_ts`          — set to the **previous response's `max_ts`** field. Empty/omit on the first call.

Telemetry fields the official client sends but slk does NOT need: `_x_reason`, `_x_mode`, `_x_sonic`, `_x_app_name`, plus URL query params `_x_id`, `_x_csid`, `slack_route`, `_x_version_ts`, etc. None of these affect the response.

## Pagination cursor location
Continuation is **`current_ts` form field set to the previous response's top-level `max_ts`**, terminated by **`has_more: false`** in the response. There is no `response_metadata.next_cursor`.

## Response shape (top-level)
```json
{
  "ok": true,
  "total_unread_replies": 0,
  "new_threads_count": 0,
  "threads": [ { ... }, { ... } ],
  "has_more": true,
  "max_ts": "1778245531.649079",
  "threads_state": { ... }   // present when fetch_threads_state=true
}
```

Only `ok`, `threads`, `has_more`, and `max_ts` are load-bearing for slk.

## Per-thread item shape
```json
{
  "root_msg": {
    "user": "U054ER7195L",
    "type": "message",
    "ts": "1778245261.738709",
    "text": "...",
    "thread_ts": "1778245261.738709",
    "reply_count": 2,
    "reply_users_count": 2,
    "latest_reply": "1778245323.298449",
    "reply_users": ["U054ER7195L", "U0AQYKDUF5G"],
    "is_locked": false,
    "subscribed": true,
    "last_read": "1778245323.298449",
    "blocks": [ /* rich text blocks */ ],
    "channel": "C0B1PMJJULE",
    // Plus, for bot-authored parents:
    "bot_id": "B0...", "app_id": "A0...", "bot_profile": { ... }
  },
  "latest_replies": [
    { /* same message shape, latest 1–N replies */ }
  ]
}
```

**Crucial implication:** `root_msg` is a **complete parent message** —
text, blocks, user, channel, all of it. The plan's original Task 8
sub-phase ("fetch parents for uncached threads via GetReplies") is
unnecessary: the getView response already contains the parent. We
upsert `root_msg` (and optionally each entry in `latest_replies`) into
the `messages` cache during the subscription phase and skip the extra
`conversations.replies` round trip.

The `ThreadSubscription` row maps to:
- `WorkspaceID` — supplied by the caller (per-workspace bootstrap).
- `ChannelID`   — `root_msg.channel`.
- `ThreadTS`    — `root_msg.thread_ts` (== `root_msg.ts` for parents).
- `LastRead`    — `root_msg.last_read`.
- `Active`      — `root_msg.subscribed`.

## Inactive subscriptions in response?
Not observed. Every `root_msg` in the captured sample has
`subscribed: true`. The endpoint appears to return only currently-
subscribed threads. The implementation should defensively filter on
`root_msg.subscribed == true` anyway, so a future Slack change that
includes inactive entries doesn't pollute the reconcile.

## WebSocket events for subscription changes

A distinct event type **`thread_subscribed`** exists alongside
`thread_marked`. Observed payload:

```json
{
  "type": "thread_subscribed",
  "subscription": {
    "type":        "thread",
    "channel":     "C0ARN0ULQQP",
    "thread_ts":   "1777317306.206819",
    "date_create": 1778593980,
    "active":      true,
    "last_read":   "1777355247.508599"
  },
  "event_ts": "1778593980.013600"
}
```

The `subscription` block is structurally identical to the one inside
`thread_marked` (channel, thread_ts, active, last_read). slk's
handler should treat both events identically: upsert the
`thread_subscriptions` row with the carried (channel, thread_ts,
last_read, active=active).

A symmetric `thread_unsubscribed` event has not been observed yet but
is likely. The implementation should defensively add a case for it
that tombstones the row (upsert with `active=false`).
