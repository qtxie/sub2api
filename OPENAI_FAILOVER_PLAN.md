# OpenAI First-Output Retry, Health, Failover, and Priority Plan

## Objective

Implement an OpenAI account-routing state machine with these properties:

1. A request that produces no semantic output before its configured deadline gets one same-account retry using a fresh session and hash.
2. A fast retry success does not mark the account slow. A retry timeout, slow success, or account-attributable error enters normal failover.
3. Account-attributable upstream 4xx/5xx and transport failures enter normal failover.
4. An account is marked slow only after three consecutive slow request outcomes.
5. Account selection is priority-dominant. Speed is an eligibility gate, not a ranking signal across priority tiers.
6. Switching back to a higher-priority account requires a real upstream probe that produces semantic output fast enough.
7. A recovered higher-priority account is restored as soon as it is healthy, fast enough, and has capacity.
8. A higher-priority account that relapses soon after failback receives a linearly increasing cooldown: add 5 minutes per relapse, capped at 30 minutes. Stable real traffic resets it to the default 5 minutes.

Lower numeric `account.Priority` values have higher priority.

## Terminology

- **Semantic output**: model output that is meaningful to the client. SSE comments, heartbeats, `response.created`, and `response.in_progress` are not semantic output.
- **Same-account retry**: a replay on the selected account with a fresh upstream session. It is not an account switch.
- **Normal failover**: exclude the failed account for the current request, then select another account.
- **Slow escape**: temporarily remove a slow account from normal selection after three consecutive slow outcomes.
- **Failback**: switch a sticky session from a lower-priority fallback account back to a recovered higher-priority account.
- **Fast enough**: semantic TTFT is less than or equal to the configured recovery threshold.
- **Relapse window**: the first 5 minutes after failback to a higher-priority account.

## Target State Machine

```text
SELECT highest-priority healthy, fast-enough, available tier
                         |
                         v
                    ATTEMPT 1
                         |
        +----------------+----------------+
        |                                 |
 semantic output                 first-output timeout
        |                                 |
 report request outcome          create fresh session/hash
        |                         retry same account once
        |                                 |
        |                 +---------------+---------------+
        |                 |                               |
        |          fast semantic output          timeout/error/slow output
        |                 |                               |
        |          report one success             report one bad outcome
        |          no slow strike                 exclude current account
        |                                                 |
        +-------------------------------------------------+
                                                          |
                                                   NORMAL FAILOVER
```

The original attempt and its same-account retry form one retry chain and produce exactly one scheduler-health outcome.

## 1. Same-Account First-Output Retry

### Trigger

Start the retry only when all of the following are true:

- First-output timeout is enabled.
- No semantic output was committed to the client.
- The failure is the typed `first_output_timeout` error.
- The client is still connected.
- The configured same-account retry count has not been exhausted.
- The request is replay-safe under the existing Responses replay checks.

Ordinary 4xx, 5xx, credential, and transport errors do not use this retry. They go directly to normal failover.

### Retry construction

For each same-account retry:

- Generate a cryptographically random retry seed.
- Derive a new internal session hash from that seed.
- Clone the upstream request body and replace `prompt_cache_key` with the retry seed.
- Override retry-scoped session metadata consumed by the upstream request builder, including applicable session and conversation identifiers.
- Reuse the selected account, its configured proxy, base URL, credentials, and normal transport policy.
- Reacquire the account concurrency slot. Do not reuse the first attempt's release callback.
- Use a fresh per-attempt context and the normal first-output deadline.
- Keep the client's original sticky-session binding unchanged.
- Do not restore retry-proxy routing, fresh-transport routing, or a request-wide pre-output budget.

The retry count should default to one. The implementation must log that duplicate upstream work or billing is possible because cancellation cannot prove the first upstream attempt stopped processing.

### Response isolation

Continue using the existing first-output staging guard:

- Buffer only pre-semantic frames.
- Discard the failed attempt's staged bytes before retrying.
- Commit bytes only after semantic output is detected.
- Never retry after semantic output reaches the client.

## 2. Retry-Chain Health Accounting

Do not call `ReportOpenAIAccountFirstOutputTimeout` immediately after the first timeout. Hold the provisional timeout until the retry chain finishes.

Record one final outcome:

| Retry-chain result | Scheduler health result |
|---|---|
| Retry semantic TTFT <= recovery threshold | One successful fast sample; discard the original timeout strike |
| Retry succeeds above the hard slow threshold | One slow outcome |
| Retry succeeds between recovery and hard slow thresholds | Successful sample for metrics; no hard-slow strike |
| Retry times out | One slow outcome, not two |
| Retry returns account-attributable error | One error outcome, then normal failover |
| Client disconnects | No account-health penalty |

Use attempt TTFT for retry health. Do not use total request elapsed time, because the first timeout would make every successful retry appear slow.

## 3. Normal Error Failover

Normal failover should:

1. Classify the typed upstream failure.
2. Report account health only for account-attributable failures.
3. Add the account ID to the current request's excluded set.
4. Release its slot.
5. Re-run priority-dominant selection.

Fail over for:

- Account-scoped authentication or credential failures.
- Account/provider-scoped 4xx responses.
- Retryable 5xx responses.
- 429 and existing quota/rate-limit cases according to their current policy.
- DNS, proxy, TCP, TLS, and other upstream transport failures.

Do not fail over deterministic request-scoped failures such as malformed input or an unsupported request shape. Sending the same invalid request to every account cannot recover and may duplicate work. Preserve `NextAccountStop` for those cases.

Pool-mode `RetryableOnSameAccount` remains separate from the first-output retry and keeps its existing behavior.

## 4. Three-Strike Slow Detection

The current slow configuration exposes `slow_ttft_consecutive_count`, but marking is score-driven. Change the state transition so the configured consecutive count is authoritative.

Default behavior:

- Hard slow: semantic TTFT exceeds `slow_ttft_threshold_ms`, or a retry chain ends in first-output timeout.
- Fast: semantic TTFT is less than or equal to `slow_recovery_ttft_ms`.
- Neutral: TTFT is between the recovery and hard-slow thresholds.
- Hard slow increments `slowStreak` once per request.
- Fast resets `slowStreak` to zero.
- Neutral does not mark the account slow and should reset the consecutive hard-slow streak.
- Mark the account slow only when `slowStreak >= 3` and minimum sample requirements are satisfied.
- Soft scores may remain for metrics and same-tier tie-breaking, but cannot independently trigger slow escape.

Set the default `slow_ttft_consecutive_count` to `3`.

When an account is marked slow, do not search globally for the fastest account. Enter priority-dominant selection with the slow account temporarily ineligible.

## 5. Priority-Dominant Selection

Selection must be lexicographic, in this order:

1. Request compatibility: platform, group, model, endpoint capability, image capability, transport, compact support, and continuation safety.
2. Health: active, schedulable, not expired, not rate-limited, not overloaded, not runtime-blocked, not in transient-failure cooldown, and not marked slow.
3. Speed eligibility: recent production TTFT or a real probe proves the account is fast enough.
4. Global numeric `account.Priority`, ascending.
5. Capacity and load tie-breakers within the selected priority tier only.

The scheduler must never choose a lower-priority account merely because it is faster than a higher-priority account that is already fast enough.

Example:

| Account | Priority | Semantic TTFT | Decision |
|---|---:|---:|---|
| A | 1 | Marked slow | Skip |
| B | 2 | 8 seconds | Select |
| C | 3 | 2 seconds | Do not select; B is fast enough and higher priority |

### Tier selection

- Group candidates by `account.Priority` after compatibility and health filtering.
- Evaluate tiers in ascending priority order.
- In a tier, retain only fast-enough candidates.
- If speed evidence is missing or stale, run a singleflight real probe for candidates in that tier.
- Stop at the first tier containing a healthy, fast-enough candidate with an available slot.
- Within that tier, use capacity, load, queue length, and last-used time.
- If the whole tier is healthy and fast but currently saturated, move to the next tier rather than causing avoidable head-of-line blocking.
- If no immediate slot exists in any tier, build the wait plan from the highest-priority healthy and fast-enough tier.

Apply the same tier rule to classic scheduling, advanced scheduling, normal failover, slow escape, and failback. Advanced score weights and top-K selection may operate only within a priority tier.

`Subscription priority` must remain disabled when global numeric account priority is authoritative, because subscription-pool precedence can override `account.Priority`.

### Sticky and continuation rules

- A normal sticky session on a lower-priority account may fail back to a higher-priority eligible account.
- A replayable `previous_response_id` request may rebind under the existing replay checks.
- A non-replayable continuation or function-call-output chain remains on its required account. Protocol correctness overrides priority.

## 6. Real Semantic-TTFT Probe

Upgrade the existing failback probe rather than introducing a second probe system.

The probe must:

- Use the candidate account's real token, proxy, base URL, model mapping, headers, and transport policy.
- Send a lightweight `/responses` request with one of the existing deterministic prompts.
- Parse SSE using the same semantic-event classifier as production forwarding.
- Ignore comments, heartbeats, `response.created`, and `response.in_progress`.
- Measure elapsed time until the first semantic event, not until response EOF.
- Close the probe response immediately after the first semantic event.
- Require a successful HTTP status and semantic TTFT <= `slow_recovery_ttft_ms`.
- Treat timeout, no semantic output, malformed protocol, or an error event as probe failure.
- Use singleflight by account, route, model, and capability to prevent probe storms.

A probe is a pass/fail eligibility check. Its TTFT must not allow a lower-priority account to outrank a qualifying higher-priority account.

Production TTFT evidence and probe results need a bounded freshness TTL. Stale or unknown evidence requires another probe before failback.

## 7. Prompt Higher-Priority Failback

When a session is using a lower-priority account:

1. Enumerate higher-priority candidates in ascending numeric priority order.
2. Skip candidates in health, slow, transient-failure, or adaptive failback cooldown.
3. Use recent fast production evidence or run the real semantic probe.
4. Stop at the first priority tier that passes.
5. Acquire a slot in that tier.
6. Rebind the sticky session and record `lastFailbackAt`.

Recommended initial settings:

```yaml
gateway:
  openai_scheduler:
    sticky_prefer_higher_priority_enabled: true
    sticky_prefer_higher_priority_min_interval_seconds: 5
    sticky_failback_probe_enabled: true
    sticky_failback_probe_timeout_seconds: 15
    sticky_failback_probe_success_ttl_seconds: 10
    sticky_failback_probe_failure_ttl_seconds: 60
    slow_ttft_consecutive_count: 3
```

Failback probing remains independent of the OpenAI advanced scheduler.

## 8. Linear Adaptive Failback Cooldown

Maintain this per-account state:

```text
currentCooldown
cooldownUntil
lastFailbackAt
consecutiveFastSuccesses
```

Defaults:

- Base cooldown: 5 minutes.
- Relapse window: 5 minutes after failback.
- Increment: 5 minutes.
- Maximum cooldown: 30 minutes.
- Stable recovery requirement: 3 consecutive fast production requests.

### Escalation

If an account fails with an account-attributable error during the relapse window:

```text
currentCooldown = min(currentCooldown + 5 minutes, 30 minutes)
cooldownUntil = now + currentCooldown
```

If the account reaches the three-strike slow mark during the relapse window, apply the same increment and escape from it.

The sequence is:

```text
5m -> 10m -> 15m -> 20m -> 25m -> 30m
```

Further relapses remain capped at 30 minutes.

### Non-escalating cases

- A probe failure before switching does not increase `currentCooldown`. It may defer the next probe using the current cooldown and failure-probe TTL.
- Client cancellation does not increase cooldown.
- Request-scoped deterministic 4xx does not increase cooldown.
- A failure after the relapse window is treated as ordinary degradation and uses the default cooldown unless another active slow/error policy requires more.

### Reset

- Probe success permits failback but does not reset escalation.
- Each fast real production response after failback increments `consecutiveFastSuccesses`.
- Any account-attributable error or hard-slow result resets `consecutiveFastSuccesses` to zero.
- After three consecutive fast production responses, set `currentCooldown` back to the default 5 minutes and clear escalation state.

Store adaptive cooldown state in Redis so multiple application instances make consistent decisions. Use process-local state only as a fallback when Redis is unavailable. Redis updates should be atomic and expire after a bounded inactivity period.

## 9. Configuration and Admin UI

Retain:

- `openai_first_output_timeout_seconds`
- `openai_high_effort_first_output_timeout_seconds`
- Existing slow thresholds and probe settings

Add or revise:

```yaml
gateway:
  openai_first_output_same_account_retries: 1
  openai_scheduler:
    priority_dominant_enabled: true
    slow_ttft_consecutive_count: 3
    sticky_failback_relapse_window_seconds: 300
    sticky_failback_cooldown_increment_seconds: 300
    sticky_failback_cooldown_max_seconds: 1800
    sticky_failback_recovery_fast_count: 3
    production_ttft_freshness_seconds: 300
```

Admin UI descriptions must state:

- Lower numeric account priority wins.
- Speed is a qualification threshold, not a cross-priority ranking weight.
- Same-account first-output retry may duplicate upstream work or billing.
- Subscription priority overrides numeric priority and should not be combined with priority-dominant mode.

Validate incompatible settings when saving. In particular, reject or automatically disable `subscription_priority` when `priority_dominant_enabled` is active.

## 10. Observability

Add structured events and counters for:

- `openai.first_output_same_account_retry_started`
- `openai.first_output_same_account_retry_completed`
- Retry-chain final outcome and attempt TTFT
- Slow strike count and slow-state transition
- Priority tiers considered and selected
- Probe HTTP status, semantic TTFT, and rejection reason
- Failback completed or skipped
- Adaptive cooldown increment, cap, expiry, and reset

Do not log raw session seeds, prompt content, credentials, tokens, or proxy URLs. Log only account IDs, retry numbers, hashed correlation IDs, durations, status codes, priority, and state transitions.

## 11. Test Plan

### Same-account retry

- First timeout retries the same account exactly once.
- Retry uses a different internal session hash and upstream `prompt_cache_key`.
- Retry uses the same configured proxy and normal transport policy.
- Previous concurrency slot is released and a new slot is acquired.
- Staged pre-semantic bytes from attempt one never reach the client.
- No retry occurs after semantic output or client disconnect.

### Health accounting

- Timeout followed by fast retry success records one fast success and zero slow strikes.
- Timeout followed by second timeout records one slow strike.
- Slow retry success records one slow strike.
- Client cancellation records no account penalty.
- A retry chain cannot consume two of the three slow strikes.

### Error failover

- Account-scoped 4xx, retryable 5xx, 429, and transport errors exclude the account and reselect.
- Request-scoped deterministic 4xx stops without trying other accounts.
- Pool-mode same-account retry remains independent.

### Slow detection

- Two consecutive hard-slow outcomes do not mark the account slow.
- The third consecutive hard-slow outcome marks it slow.
- A fast or neutral outcome resets the hard-slow streak.
- Soft score alone cannot trigger slow escape.

### Priority dominance

- Priority 1 fast account beats priority 10 faster account.
- Priority 1 slow or unhealthy account allows priority 2.
- Priority 2 fast account beats priority 3 faster account after priority 1 is excluded.
- Same-tier accounts use load and capacity tie-breakers.
- A saturated tier permits the next tier; fallback wait returns to the highest eligible tier.
- Advanced top-K and weighted selection never cross priority tiers.
- Non-replayable continuation remains pinned.

### Probe and failback

- HTTP 2xx without semantic output fails the probe.
- Preamble events and heartbeats do not satisfy the probe.
- Fast semantic output passes.
- Slow semantic output fails even with HTTP 2xx.
- Higher-priority candidates are probed in priority order.
- The first qualifying priority tier wins, not the globally fastest account.
- Probe singleflight prevents duplicate concurrent probes.

### Adaptive cooldown

- First quick relapse moves cooldown from 5 to 10 minutes.
- Repeated quick relapses progress linearly and stop at 30 minutes.
- Probe failure before failback does not increment cooldown.
- Failure outside the relapse window uses the default cooldown.
- Three consecutive fast real responses reset cooldown to 5 minutes.
- Redis state is shared across instances and expires after inactivity.

Use a fake clock for cooldown, freshness, and retry tests. Run race tests for scheduler stats, retry-chain completion, probe singleflight, and Redis/in-memory fallback transitions.

## 12. Delivery Sequence

1. Add typed retry-chain outcome APIs and tests without changing routing.
2. Add the single fresh-session same-account retry behind a disabled feature flag.
3. Defer timeout health accounting and enforce one outcome per retry chain.
4. Make the three-strike slow threshold authoritative.
5. Introduce priority tiers in classic and advanced selection.
6. Upgrade the real probe to semantic TTFT measurement.
7. Add prompt failback and linear adaptive cooldown.
8. Add settings, admin UI, metrics, and operational documentation.
9. Enable shadow-decision logging to compare old and new account choices.
10. Roll out same-account retry, priority dominance, and adaptive cooldown independently, then enable them together after metrics are stable.

## Acceptance Criteria

The work is complete when all of the following hold:

- A first timeout receives at most one fresh-session same-account retry by default.
- A fast retry success produces no slow strike.
- Each client request contributes at most one slow/error health outcome per account.
- An account escapes only after three consecutive slow request outcomes.
- Every account choice uses health and fast-enough checks before numeric priority.
- No lower-priority account outranks a qualifying higher-priority account because it is faster.
- Every failback is backed by a real semantic-TTFT probe.
- Recovered higher-priority accounts are reconsidered at the configured short interval.
- Quick relapses add 5 minutes of cooldown up to 30 minutes.
- Three fast production successes restore the 5-minute default cooldown.
- No failover or retry corrupts non-replayable continuation semantics.
