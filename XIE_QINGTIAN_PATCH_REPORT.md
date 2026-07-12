# Xie Qingtian Patch Code Report

## 1. Scope and method

This report covers the patch series authored by **Xie Qingtian
<xqtxyz@gmail.com>** on the current `main` branch.

- Reviewed head: `43485cc70edf9619f553999364480b0dd50b5b10`
- Upstream comparison point: `origin/main` at
  `e316ebf52838a89d57fc790981cce7520f819ac8`
- The upstream comparison point is an ancestor of the reviewed head, so
  `git diff origin/main..HEAD` is a clean view of the patch's surviving net
  changes.
- Xie-authored commits in the range: **58** (50 non-merge commits and 8 merge
  commits).
- Two additional commits in the range were made by `github-actions[bot]` to
  synchronize version metadata. They are not attributed to Xie.
- Net patch size: **227 files, 30,529 insertions, 664 deletions**.

The large line count does not mean all 30,529 lines are hand-written product
logic. About 12,881 added lines are generated Ent ORM code, and about 5,316
added lines are tests. The approximate breakdown is:

| Area | Files | Added | Deleted |
| --- | ---: | ---: | ---: |
| Backend product code | 71 | 7,198 | 293 |
| Frontend product code | 49 | 4,907 | 110 |
| Generated Ent ORM code | 45 | 12,881 | 135 |
| Tests | 52 | 5,316 | 113 |
| CI workflows | 4 | 76 | 13 |
| Deployment/docs | 6 | 151 | 0 |

Merge commits were inspected for conflict-resolution changes, but upstream
features brought in by those merges are not described as Xie's patch.

## 2. Executive summary

The patch adds or substantially changes six functional areas:

1. OpenAI account scheduling now detects slow or repeatedly failing accounts,
   escapes unhealthy sticky bindings, optionally probes recovered
   higher-priority accounts, and can safely rebind some `previous_response_id`
   chains.
2. OpenAI failover and failback events can be delivered to Telegram with
   detailed request/account context, asynchronous delivery, retries, and
   shutdown draining.
3. OpenAI accounts gain manual and "smart" upstream User-Agent policies, and
   the gateway's Codex identity is made consistent across request paths.
4. Users gain a complete, admin-gated Chat page with server-side conversation
   history, streaming responses, model/reasoning controls, Markdown/math,
   exports, and browser-local attachments.
5. Users gain an Image Studio for OpenAI, Gemini, and Antigravity image models,
   including streaming OpenAI responses and a browser-local gallery.
6. Subscriptions can be configured with a monthly allowance of one-day quota
   boosts; activating a boost doubles the effective daily quota for the current
   server-calendar day.

The patch also adds a patched Docker-image workflow, refreshes GitHub Action
versions, fixes compact-stream error framing, changes payment/key UI defaults,
and updates generated client configuration examples.

## 3. OpenAI scheduling, failover, and failback

### 3.1 Sticky-session recovery to higher-priority accounts

OpenAI account priority uses the existing convention that a **smaller numeric
priority is better**. When a session is sticky to a lower-priority fallback,
the new logic can search for eligible accounts with a better priority.

The feature is controlled by
`gateway.openai_scheduler.sticky_prefer_higher_priority_enabled` and is off by
default. It operates through the advanced OpenAI scheduler. For each potential
failback it:

1. Loads schedulable accounts for the same group and platform.
2. Keeps only accounts with a numerically lower priority than the currently
   bound account.
3. Applies the existing model, endpoint capability, compact-mode, image
   capability, transport, shadow-parent health, channel restriction, and
   runtime-block checks.
4. Sorts candidates by priority, current load, waiting count, least-recent use,
   and ID.
5. Skips accounts in failback cooldown or transient-failure cooldown.
6. Optionally sends a real upstream health probe.
7. Requires the probe to be both successful and fast enough.
8. Acquires a concurrency slot before changing the sticky binding.

Attempts are throttled per binding. The default minimum interval is 60 seconds.
If a newly selected failback account immediately fails again, it is put in a
separate 300-second failback cooldown.

### 3.2 Real upstream failback probe

The health check is a lightweight real request, not just a database/status
check.

- Model: `gpt-5.5`, after account model mapping.
- Endpoint: `/responses`, or `/responses/compact` for a compact request.
- OAuth accounts use the ChatGPT Codex endpoint and account headers.
- API-key accounts use their configured OpenAI base URL.
- The account's configured proxy is honored.
- Headers identify the probe as Codex (`Originator: codex-tui`, Codex
  User-Agent, version, and Responses beta header).
- The prompt is randomly selected from five small prompts to avoid an identical
  request signature on every probe.
- Default timeout: 5 seconds.
- Success cache: 30 seconds; failure cache: 60 seconds.
- Concurrent identical probes are collapsed with `singleflight`.
- A successful probe taking more than the slow-recovery threshold (10 seconds
  by default) is still rejected as too slow.
- Image-capability requests skip the generic text probe.

If a candidate had already been marked slow, the scheduler bypasses cached
probe results, requires a fresh fast probe, and clears the slow state only after
that fresh probe succeeds quickly.

### 3.3 Slow-account escape

The scheduler keeps per-account, in-process runtime statistics:

- Error-rate EWMA, alpha 0.2.
- First-token-time EWMA, alpha 0.2.
- Sample count.
- Consecutive slow and fast streaks.
- A bounded slow score.
- A `slow until` deadline.

Default scoring behavior:

- TTFT over 30,000 ms: hard-slow sample, score `+3`, slow streak increments.
- TTFT over 15,000 ms: soft-slow sample, score `+1`.
- TTFT at or below 10,000 ms: recovery sample, score `-1`, fast streak
  increments.
- Between 10,000 and 15,000 ms: neutral sample; streaks reset.
- Score decays by 1 for every 60 seconds without a score update.
- Score is capped at 10.
- At least 3 samples are required before marking an account slow.
- Score 5 permits sticky escape and marks the account slow.
- Score 8 causes the account to be skipped when another candidate exists.
- Slow cooldown defaults to 300 seconds.
- Two consecutive fast samples can recover the account early once its score is
  below the mark threshold.

Slow state changes and scheduler skips are emitted as structured logs. Slow
accounts remain usable as a last resort when no alternative account exists.
The state is not stored in PostgreSQL or Redis, so it is reset by a process
restart and is local to each application instance.

### 3.4 Transient upstream failure cooldown

Repeated transient status codes now create a short account-local cooldown.
Defaults are:

- Enabled.
- Statuses: `502,503,504`.
- Sliding window: 60 seconds.
- Threshold: 3 failures.
- Cooldown: 60 seconds.

Failures are recorded when OpenAI forwarding produces a failover error. A
successful request clears the accumulated state. During cooldown, sticky and
load-balanced selection skip the account when alternatives exist. Like the slow
score, this state is in process memory only and does not modify the account's
database `Temp Unschedulable` state.

### 3.5 `previous_response_id` rebind

Normally an OpenAI response chain must remain on the account that owns its
`previous_response_id`. The patch adds an opt-in escape mechanism for cases
where that account is unavailable, slow, cooling down, or (with a less
conservative option) merely lower priority.

Rebinding is allowed only when all important safety conditions hold:

- `previous_response_rebind_enabled` is true.
- The request input can be replayed without the previous response.
- The request is not a `function_call_output` continuation.
- The scheduler found and acquired a valid replacement account.

Replayable input includes non-empty text/message input and image input with an
image URL or file ID. Tool-result continuations and unknown input item types are
not considered replayable. When a move is approved, the handler removes
`previous_response_id` from the HTTP or first WebSocket payload so the upstream
builds a new chain from the supplied input. The decision and reason are logged.

The default is conservative: rebind is disabled, and when enabled the
`only_when_current_unhealthy` option defaults to true.

### 3.6 Configuration and admin controls

The admin Settings page exposes:

- Prefer recovered higher-priority account.
- Probe minimum interval.
- Failure cooldown.
- Allow `previous_response_id` rebind.
- Rebind only when the current account is unhealthy.

These database settings use the loaded config values as defaults. The update
logic avoids writing implicit defaults into the settings table, which preserves
future environment/config changes. Cached values are refreshed after settings
updates.

Important environment defaults are:

| Variable | Default |
| --- | --- |
| `GATEWAY_OPENAI_SCHEDULER_STICKY_PREFER_HIGHER_PRIORITY_ENABLED` | `false` |
| `..._MIN_INTERVAL_SECONDS` | `60` |
| `..._FAILBACK_FAILURE_COOLDOWN_SECONDS` | `300` |
| `..._FAILBACK_PROBE_ENABLED` | `true` |
| `..._FAILBACK_PROBE_TIMEOUT_SECONDS` | `5` |
| `..._PROBE_SUCCESS_TTL_SECONDS` | `30` |
| `..._PROBE_FAILURE_TTL_SECONDS` | `60` |
| `GATEWAY_OPENAI_SCHEDULER_PREVIOUS_RESPONSE_REBIND_ENABLED` | `false` |
| `..._REBIND_ONLY_WHEN_CURRENT_UNHEALTHY` | `true` |
| `GATEWAY_OPENAI_SCHEDULER_SLOW_ACCOUNT_ESCAPE_ENABLED` | `true` |
| `GATEWAY_OPENAI_SCHEDULER_TRANSIENT_FAILURE_COOLDOWN_ENABLED` | `true` |

The Compose files now load the whole `.env` file into the application
container, allowing all of these nested Viper environment settings to reach the
process. The main Compose file also declares the transient-failure variables
with explicit defaults.

## 4. Telegram account-switch notifications

### 4.1 Events and content

The gateway can send Telegram messages for OpenAI failover activity across the
Responses, Messages, WebSocket, chat-completions, embeddings, and images paths.
It models five phases:

- `started`: an account failed and switching began; disabled by default.
- `completed`: the replacement account completed successfully.
- `failed`: failover exhausted or the selected target failed.
- `cancelled`: for example, a WebSocket client disconnected before completion.
- `failback`: a sticky or previous-response chain returned to the highest
  eligible priority account.

Messages include the phase-specific source/target accounts and priorities,
upstream/final/client status, model, route, switch count, latency, user, API
key, group, request IDs, HTTP method/path, stream state, and failure reason.
Failure messages also indicate whether output had already started, whether a
fallback response was written, and whether retry is still possible.

The latest fixes track the actual target account through multi-hop failover,
emit a failure for an intermediate target that fails before completion, and
distinguish successful, failed, cancelled, and failback outcomes.

### 4.2 Delivery behavior

- Disabled unless Telegram is enabled and both bot token and chat ID exist.
- Delivery is off the request path through a bounded queue of 1,024 events.
- One worker drains the queue.
- A full queue drops the new notification and writes an error log; it does not
  block the API request.
- Each attempt has the configured timeout, 5 seconds by default.
- Retryable network, `429`, and `5xx` failures are retried up to 3 attempts.
- Backoff is 250 ms then 500 ms unless Telegram supplies `retry_after`.
- Non-retryable Telegram errors stop immediately.
- Message text is capped at Telegram's 4,096-rune limit.
- Bot tokens are redacted from logged error strings.
- Numeric chat IDs are sent as numbers; channel/user names remain strings.
- Application shutdown closes the queue and waits for pending deliveries, with
  cancellation if the shutdown context expires.

Configuration:

| Variable | Default |
| --- | --- |
| `GATEWAY_OPENAI_SWITCH_NOTIFY_TELEGRAM_ENABLED` | `false` |
| `GATEWAY_OPENAI_SWITCH_NOTIFY_TELEGRAM_BOT_TOKEN` | empty |
| `GATEWAY_OPENAI_SWITCH_NOTIFY_TELEGRAM_CHAT_ID` | empty |
| `GATEWAY_OPENAI_SWITCH_NOTIFY_MIN_INTERVAL_SECONDS` | `60` |
| `GATEWAY_OPENAI_SWITCH_NOTIFY_TELEGRAM_TIMEOUT_SECONDS` | `5` |

`send_started` also exists in YAML/config (`false` by default), although it is
not listed as a separate variable in `.env.example`.

## 5. OpenAI upstream User-Agent and Codex identity

OpenAI account create/edit forms now store two optional credential values:

- `user_agent`: a fixed manual upstream User-Agent override.
- `smart_user_agent_enabled`: enables normalized smart mode and disables the
  manual text field in the UI.

Manual mode sends the configured value instead of the client's User-Agent.
Smart mode accepts only recognizable Codex Desktop and `codex-tui` shapes and
maps them to a small fixed identity set:

- Windows 10 x86_64.
- macOS arm64.
- Red Hat Enterprise Linux x86_64.

It preserves a recognized client version and terminal/build suffix where
possible. Unknown client User-Agents fall back to the gateway's fixed Codex CLI
User-Agent. This policy is applied to normal and passthrough OpenAI forwarding.
The global `ForceCodexCLI` option still takes precedence.

The patch also centralizes the Codex originator as `codex-tui`, adds
`codex-tui/` to official-client recognition, and uses the same identity for
usage probes, PAT validation, failback probes, gateway requests, and tests. The
fixed CLI version remains `0.144.1`.

## 6. Built-in Chat

### 6.1 Access control

Chat is disabled per user by default through the new `users.chat_enabled`
column. Admin user-create and user-edit dialogs expose the flag. It is included
in user DTOs and refreshed auth state.

Protection exists at three levels:

1. The sidebar hides Chat unless `chat_enabled` is true.
2. The router waits for the initial user refresh, then redirects stale or
   unauthorized navigation away from `/chat`.
3. Every Chat backend handler calls `EnsureUserCanUseChat`; direct API calls by
   disabled users return `CHAT_DISABLED`.

### 6.2 Database model

`chat_conversations` stores:

- Owner user ID.
- Title (default `New chat`, maximum 120).
- Optional API-key ID; deleting the key sets this to null.
- Model (maximum 128).
- System prompt (maximum 8 KiB at the service layer).
- Reasoning effort (maximum 32).
- Created/updated timestamps and soft-delete timestamp.

`chat_messages` stores:

- Conversation and owner IDs.
- Role: `user` or `assistant`.
- Content, limited to 64 KiB by the service.
- Status: `complete`, `error`, or `cancelled`.
- Error text and JSON metadata.
- Timestamps.

Conversation deletion is soft deletion; database cascading removes messages if
the conversation is physically deleted. Repository queries always constrain
records by the authenticated user.

### 6.3 API routes

All routes are JWT-authenticated and user-scoped:

| Method | Route | Purpose |
| --- | --- | --- |
| GET | `/api/v1/chat/models?api_key_id=...` | Models available to an owned active key |
| GET | `/api/v1/chat/export` | Stream all conversations and messages as JSON |
| GET | `/api/v1/chat/conversations` | Paginated conversation list |
| POST | `/api/v1/chat/conversations` | Create a conversation |
| GET | `/api/v1/chat/conversations/:id` | Load a conversation and messages |
| PUT | `/api/v1/chat/conversations/:id` | Update title/key/model/system prompt/reasoning |
| DELETE | `/api/v1/chat/conversations/:id` | Soft-delete a conversation |
| POST | `/api/v1/chat/conversations/:id/messages` | Persist a user or assistant message |
| POST | `/api/v1/chat/conversations/:id/stream` | Generate and stream the next assistant message |
| DELETE | `/api/v1/chat/conversations/:id/messages/:message_id` | Delete one message |

Export uses cursor pagination in batches of 100 and writes a versioned JSON
document incrementally, avoiding loading all history into memory.

### 6.4 Generation flow

The frontend creates or updates the conversation, persists the user's message,
then calls the stream endpoint. The backend:

1. Rechecks ownership and active status of the selected API key.
2. Rejects missing model/key and image-generation models (those belong in Image
   Studio).
3. Builds a chat-completions request from the stored conversation history.
4. Prepends the optional system prompt.
5. Adds `reasoning_effort` when selected.
6. Calls the local `/v1/chat/completions` gateway with the user's API key, so
   existing routing, billing, quotas, and account selection still apply.
7. Relays assistant text as SSE `delta` events.
8. Filters reasoning-only payloads so hidden reasoning is not mixed into final
   answer text.
9. Persists the assistant result as complete, error, or cancelled and finishes
   with a `done` or `error` event containing the saved message.

The browser retries once after refreshing an expired access token. The UI
supports stop, regenerate, copy, rename, delete, new chat, system prompt, chat
export, collapsible history, and smooth batched rendering of streaming deltas.
The first message becomes a title of up to 60 display characters.

### 6.5 Models, reasoning, and rendering

The model list comes from the selected API key's group and respects available
models/custom model lists. Image-only models are removed. The selected key,
model, reasoning effort, and sidebar state are remembered in local storage.

Reasoning choices are model-aware:

- GPT-5.4 and GPT-5.5: none, low, medium, high, xhigh.
- GPT-5.6: the same plus max.
- Other models: default, none, minimal, low, medium, high, max, xhigh.

Assistant output is rendered with `marked`, sanitized with DOMPurify, and
supports KaTeX inline `\(...\)` and display `\[...\]`/`$$...$$` math with
KaTeX trust disabled.

### 6.6 Attachments

The Chat composer accepts up to 8 attachments:

- Images: data URLs, maximum 10 MiB each.
- Text/code files: maximum 256 KiB each.
- Total request attachment data: maximum 16 MiB.
- Supported files include text MIME types, JSON/XML/YAML/JavaScript, and common
  text/code extensions such as Markdown, CSV, JS/TS, Go, Python, Java, C/C++,
  Rust, SQL, shell, and PowerShell.

Images become OpenAI-compatible `image_url` content parts. Text files are
embedded into a labeled text part containing filename, MIME type, byte size,
and content.

The actual attachment files are **not stored on the server**. They are saved in
browser IndexedDB (`sub2api-chat`) and linked locally to server message IDs.
This gives draft restoration and attachment previews on the same browser while
keeping server history small. The store:

- Retains data for 30 days.
- Caps each user's browser-local attachment storage at 250 MiB.
- Evicts expired then least-recently-accessed records.
- Refuses writes when estimated browser storage would exceed 95% of quota.
- Removes local files with deleted conversations and supports clearing all
  local attachments.

Consequently, Chat JSON export contains messages but not the browser-local
attachment blobs, and attachments do not follow the user to another browser.

## 7. Image Studio

### 7.1 Access and API

Image Studio is available at `/image-studio`; its generation API is:

`POST /api/v1/image-playground/generations`

The request must identify an owned, active API key whose group:

- Uses OpenAI, Gemini, or Antigravity.
- Has `allow_image_generation` enabled.

The backend rechecks these conditions, so a user cannot submit another user's
key ID or bypass the group feature flag.

Accepted options are 1-4 images, sizes `1024x1024`, `1536x1024`, or
`1024x1536`, OpenAI qualities low/medium/high, backgrounds auto/opaque, and
formats PNG/JPEG/WebP. Responses are limited to 64 MiB.

### 7.2 OpenAI generation

OpenAI requests always use `gpt-image-2` and are sent through the local
`/v1/images/generations` gateway with `stream: true`. The handler accepts either
JSON or `text/event-stream`; for SSE it relays bytes directly while enforcing
the response-size limit. The frontend parser understands partial/completed
image events and Responses-style completion events.

### 7.3 Gemini and Antigravity generation

Supported model names are:

- `gemini-3.1-flash-image`
- `gemini-3.1-flash-image-preview`
- `gemini-3.1-flash-lite-image`
- `gemini-3-pro-image`
- `gemini-3-pro-image-preview`
- `gemini-2.5-flash-image`
- `gemini-2.0-flash-exp-image-generation`

Gemini calls use `/v1beta/models/:model:generateContent`; Antigravity uses
`/antigravity/v1beta/models/:model:generateContent`. The selected API key is
passed as `x-goog-api-key`. Image size maps to Gemini aspect ratios 1:1, 3:2, or
2:3. Quality/background/format controls are hidden for Gemini-family keys.

The backend parses inline image data and MIME type from Gemini candidates and
uses accompanying candidate text as the revised prompt. It repeats generation
calls until the requested count is collected, truncating extra returned images.

### 7.4 Gallery and user experience

The page shows per-image generation placeholders, elapsed time, cancellation,
failure/retry state, result reveal animation, full-size preview, prompt reuse,
download, item deletion, and clear-gallery actions. The last API key and Gemini
model are remembered in local storage.

Generated image data and settings are stored only in browser IndexedDB
(`sub2api-image-studio`), separated by user ID. There is no server-side gallery,
retention policy, or explicit byte budget.

## 8. Subscription daily quota boosts

### 8.1 Data and admin policy

Each user subscription gains:

- `quota_boost_monthly_limit` (0-31, default 0/off).
- `quota_boost_monthly_used`.
- `quota_boost_period_start`.
- `quota_boost_activated_at`.

An admin can set the allowance with:

`PUT /api/v1/admin/subscriptions/:id/quota-boost`

The policy is allowed only for an active, unexpired subscription whose group
has a daily dollar limit. Setting zero disables boosts and clears today's
activation. The admin subscription page shows monthly use and active-today
state.

### 8.2 User activation and billing effect

A user activates a boost through:

`POST /api/v1/subscriptions/:id/quota-boost`

Activation is performed inside a transaction with a row lock. It verifies
ownership, status, expiration, daily-limit eligibility, and remaining monthly
allowance before incrementing use. Repeating activation on the same server day
is idempotent and does not consume another allowance.

For the current server-calendar day, `EffectiveDailyLimitUSDAt` returns twice
the group's normal daily limit. Weekly and monthly limits are unchanged. The
effective limit is used by limit checks and exposed in subscription DTOs so the
user/admin progress bars show the doubled denominator. Cache entries are
invalidated after policy or activation changes.

The monthly counter is interpreted against the configured application timezone
and resets logically at the next calendar month without requiring an immediate
database rewrite. DTOs expose monthly limit/used/remaining, active/available
flags, activation time, and next day/month reset times.

## 9. Compact streaming fix

Compact clients may request SSE while the upstream compact operation is unary
JSON. The existing keepalive can commit HTTP 200 before a later local validation
or upstream error occurs. Writing a normal JSON error after that point corrupts
the SSE protocol.

The patch adds `writeOpenAICompactAwareJSONError`:

- Before keepalive commitment, it returns the normal HTTP status and JSON error.
- After commitment, it stops the keepalive and emits a valid
  `response.failed` SSE event carrying the intended error code/message.

The helper replaces local JSON errors for Codex restrictions, unsupported WSv1,
image permissions/options, and related passthrough paths. Streamed upstream
failure rules also use an SSE failure after commitment. Unit tests cover both
pre-commit and post-commit behavior.

## 10. CI, deployment, and smaller frontend changes

### 10.1 Patched Docker image

`.github/workflows/patched-docker.yml` runs on every `main` push and manual
dispatch. It builds `linux/amd64`, logs into GHCR with `GITHUB_TOKEN`, and pushes:

- A configurable tag, default `patched`.
- The first 8 characters of the commit SHA.
- The full commit SHA.

Build metadata receives `VERSION=patched-<short-sha>` and the full `COMMIT`.
This makes the running patched image traceable to exact source.

### 10.2 GitHub Actions updates

CI/security/release workflows update checkout from v6 to v7 and Docker setup,
Buildx, and login actions from v3 to v4. Earlier intermediate commits tried
specific Node-24-compatible versions; the surviving net result uses current
major tags.

### 10.3 Use-key and API-key UI

- Generated Codex config defaults move from GPT-5.5/xhigh to
  GPT-5.6-sol/high.
- OpenCode model context values are corrected (for example GPT-5.6 to 352k),
  `ultra` is added for GPT-5.6-sol, and obsolete `codex-mini-latest` is removed.
- API-key primary actions (Use Key and CC Switch import) become more prominent,
  while status/edit/delete remain grouped secondary actions.

### 10.4 Payment behavior

The payment page now opens on the Subscription tab by default. It still selects
Recharge when explicitly requested or when restoring a balance-recharge
session, and restores Subscription for a subscription order.

### 10.5 Frontend i18n and navigation

English and Chinese locale additions cover all new scheduler, Chat, Image
Studio, account User-Agent, user grant, quota boost, and notification UI. Later
commits adapt these keys to the repository's split locale modules. Route
prefetch adjacency is updated to include Chat, and the sidebar gains Chat and
Image Studio entries.

## 11. Review observations and operational caveats

These are observations from the final net code, not additional changes made by
this report.

### 11.1 Image Studio is hidden for Gemini-only users

`ImageStudioView.vue` and the backend correctly accept OpenAI, Gemini, and
Antigravity keys. However, `useImageGenerationAccess.ts`, which controls sidebar
visibility, currently recognizes only OpenAI keys. A user with only an eligible
Gemini or Antigravity key can use `/image-studio` directly but may not see the
sidebar entry. The access composable should use the same three-platform rule as
the page/backend.

### 11.2 Telegram deduplication is effectively per event instance

The documented minimum interval says it suppresses repeated notifications for
the same event/account/status/model. The actual dedupe key also includes
request IDs and `OccurredAt.UnixNano()`. Separately created notifications almost
always have a unique key, so the 60-second interval will not suppress a storm
of equivalent events across requests. It does still prevent an identical event
object from being delivered concurrently. If cross-request suppression is the
goal, volatile request/time fields should not be part of the key.

### 11.3 Browser-local data does not roam

Chat attachment blobs and the entire Image Studio gallery are local to the
browser. They are not part of server backups or Chat export. This is intentional
in the current implementation but should be understood by operators and users.
Chat attachments have retention/budget controls; Image Studio currently does
not, so repeated base64 image storage can eventually hit browser quota.

### 11.4 Scheduler health state is instance-local

Slow-account scores, failback cooldowns, probe caches, attempt throttles, and
transient failure cooldowns are process-local. In a multi-instance deployment,
instances can make different choices until each observes similar traffic. A
restart also clears the state. This avoids persistent false positives but means
the feature is not a cluster-wide circuit breaker.

## 12. Test coverage added by the patch

The patch adds focused backend and frontend tests for:

- Scheduler sticky failback, slow scoring/recovery, probes, cooldowns,
  transient failures, previous-response replay safety, and settings defaults.
- Telegram phases, formatting, retry policy, queue behavior, shutdown, token
  redaction, and handler outcomes.
- Smart User-Agent normalization.
- Chat access, repository/service behavior, streaming parsing, export,
  attachments, Markdown/math, reasoning options, and route guards.
- Image Studio API validation, SSE parsing, Gemini parsing, gallery behavior,
  and UI flows.
- Quota boost validation, transactions, idempotency, monthly reset semantics,
  effective daily limits, cache invalidation, handlers, and UI.
- Compact keepalive error framing.
- Payment tab restoration and API-key documentation changes.

## 13. Commit appendix

All Xie-authored commits in `origin/main..HEAD`, oldest first:

- `9df478af` (2026-07-03) Add OpenAI sticky failback controls
- `696c4ec7` (2026-07-04) Add Telegram alerts for OpenAI failover switches
- `821f8adc` (2026-07-04) Add OpenAI slow account escape
- `a31d5ac9` (2026-07-04) Add patched Docker image workflow
- `729ddc48` (2026-07-04) Fix unit test setting repo stubs
- `0e8df7d0` (2026-07-04) Fix OpenAI switch notifier lint
- `020029fd` (2026-07-04) Update GitHub Actions to Node 24 runners
- `6669923e` (2026-07-04) Pin GitHub Actions to latest Node 24 releases
- `5d2eb647` (2026-07-04) Use latest major GitHub Action tags
- `a35771dc` (2026-07-05) Probe slow OpenAI accounts before failback skip
- `381c7d93` (2026-07-05) Vary OpenAI failback probe identity and prompt
- `44ee0d59` (2026-07-05) Use Codex identity for OpenAI failback probes
- `e527771f` (2026-07-05) Update OpenAI Codex upstream identity
- `08a201c6` (2026-07-05) Require fast failback probe for slow account recovery
- `5591fefa` (2026-07-05) Require fast probe for all OpenAI failback
- `0ea4284d` (2026-07-05) Use gpt-5.5 for OpenAI failback probes
- `77f3e001` (2026-07-05) Update UseKeyModal
- `b48cc022` (2026-07-05) Document OpenAI scheduler env options
- `5d214849` (2026-07-06) Improve OpenAI failover Telegram alerts
- `a1bd82a8` (2026-07-06) Fix WebSocket failover notification outcomes
- `847ed7d4` (2026-07-06) Add OpenAI account User-Agent override UI
- `07c2eaed` (2026-07-06) Add smart OpenAI upstream User-Agent mode
- `8f508dcf` (2026-07-06) Improve API key action buttons
- `ae84c852` (2026-07-07) Notify on OpenAI highest priority failback
- `274b6e49` (2026-07-07) Merge branch `main` from upstream repository
- `85d4aad0` (2026-07-07) Improve payment defaults and switch notifications
- `66398c0c` (2026-07-08) Merge origin/main
- `60200ea3` (2026-07-08) Merge origin/main with conflict-resolution changes
- `6b3e6c24` (2026-07-08) Fix compilation error
- `1d164e92` (2026-07-08) Improve OpenAI failover notifications
- `1f21088e` (2026-07-08) Add OpenAI transient failure cooldown
- `a0e99ec0` (2026-07-09) Improve OpenAI failover notifications
- `e52b6406` (2026-07-09) Add user Chat page with streaming history
- `17f3c28f` (2026-07-09) Fix Chat streaming reasoning output
- `6f7fc9e7` (2026-07-09) Increase Chat content gutters
- `2be89c05` (2026-07-09) Gate Chat access per user
- `e3c25bee` (2026-07-09) Merge upstream/main
- `cc16f523` (2026-07-09) Fix sticky failback settings regressions
- `d4f75497` (2026-07-09) Fix frontend i18n split locale keys
- `68b773e8` (2026-07-10) Improve Chat composer input box
- `683b1ad8` (2026-07-10) Merge upstream/main
- `07b54ce8` (2026-07-10) Update use-key documentation
- `feeca4e4` (2026-07-10) Add subscription daily quota boosts
- `5090663d` (2026-07-10) Improve Chat math and reasoning options
- `6a91bd13` (2026-07-10) Merge origin/main
- `c07d4325` (2026-07-10) Add GPT Image Studio
- `af680836` (2026-07-10) Fix compact issue
- `1e4d2f41` (2026-07-10) Merge origin/main
- `e9a98563` (2026-07-10) Add SHA to patched Docker image
- `f5d69e2d` (2026-07-10) Persist Chat attachments in IndexedDB
- `9dabf2e6` (2026-07-10) Merge origin/main
- `6fb75f85` (2026-07-10) Improve Image Studio and Chat page
- `da093352` (2026-07-10) Stream Image Studio generations
- `32bfa6dc` (2026-07-10) Merge upstream/main
- `31990b20` (2026-07-11) Refine Chat and sidebar controls
- `acd62163` (2026-07-11) Support Gemini image models in Image Studio
- `26bef3ba` (2026-07-11) Fix regression in smart User-Agent
- `43485cc7` (2026-07-11) Fix Telegram notification bugs
