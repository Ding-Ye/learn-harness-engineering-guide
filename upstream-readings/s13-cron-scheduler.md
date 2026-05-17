# s13 upstream excerpt: scheduling-and-automation.md L78-L160 (Cron implementation patterns)

Source: `guide/scheduling-and-automation.md` L78-L160 in `nexu-io/harness-engineering-guide`
Permalink: <https://github.com/nexu-io/harness-engineering-guide/blob/86fec9bea430cecb29ff10afaae36b96496a8f8e/guide/scheduling-and-automation.md#L78-L160>
License: MIT (© 2026 Nexu)

```markdown
## Cron Implementation Patterns

Crons are the most structured scheduling primitive. Getting them right
requires attention to four dimensions: schedule definition, session targeting,
payload type, and delivery.

### Schedule Definition

**Cron expressions** use the standard five-field format:

    ┌───────── minute (0–59)
    │ ┌─────── hour (0–23)
    │ │ ┌───── day of month (1–31)
    │ │ │ ┌─── month (1–12)
    │ │ │ │ ┌─ day of week (0–7, 0 and 7 = Sunday)
    │ │ │ │ │
    * * * * *

**Timezone handling** is the single most common source of Cron bugs. The rule
is simple:

> **Store UTC. Display local.**

Internally, the Harness evaluates all Cron expressions against UTC. When
showing the user their schedule, convert to their local timezone. When the
user says "every day at 8 AM," the Harness must know *which* 8 AM — ask for
timezone if unknown, store the UTC equivalent, and confirm back in local time.

    User: "Run my digest every morning at 8am"
    Agent: "Got it — daily digest at 8:00 AM Asia/Shanghai (0:00 UTC). ✓"

This avoids ambiguity and survives daylight saving transitions (for timezones
that observe them).

### Session Targeting

When a Cron fires, *where* does the Agent run? Two models:

**Isolated session** — the Cron spawns an independent Agent turn with a fresh
Context window. No conversation history. No prior messages. The Agent starts
clean, executes its task, and terminates.

Benefits:
- No history pollution — the main session stays clean
- Predictable context — the Agent sees only what the Cron provides
- Parallelizable — multiple Cron jobs can run concurrently without interference
- Configurable — each job can use a different model or thinking level

**Main session** — the Cron injects content into the user's current active
session. The Agent sees the full conversation history and can reference recent
messages.

Use main session injection sparingly. Every injection adds to the context
window, and too many will bloat it.

### Payload Types

**agentTurn** — triggers a full Agent execution cycle. The Agent receives a
prompt, has access to all its Tools and Skills, can make decisions, call APIs,
read files, and produce output.

**systemEvent** — injects a text message into the session without triggering a
full Agent turn. Think of it as dropping a note into the conversation. The
Agent will see it on its next turn (or Heartbeat) but doesn't immediately act.

### Delivery

- **Announce to channel** — the Agent's output is pushed to a specific
  messaging channel (Slack, Discord, Feishu group, etc.).
- **Webhook** — results are POSTed to an HTTP endpoint for integration with
  external systems.
- **None (silent)** — the Cron runs but produces no external output. Useful
  for background maintenance tasks like memory cleanup or data pre-fetching.

Delivery configuration should be set at Cron creation time and pinned to the
originating session. If a user creates a Cron in a Feishu group, results
should deliver to that group — not follow the user to wherever they chatted
most recently.
```

## Reading notes

1. **The "Store UTC. Display local." line is the most important sentence in this excerpt.** It looks like advice but it's actually a *type-system* claim: the schedule's authoritative representation is a UTC-anchored `time.Time` plus an IANA name; everything else is rendering. In our Go port, `CronSchedule.Timezone` is the rendering hint, `loc *time.Location` is the parsed authority, and `ShouldRun`/`NextRun` always convert incoming `now` into `loc` before doing field comparisons. Skip that conversion and the test `TestNextRun_TimezoneConversion` fails on the first run.

2. **The five-field grammar at L84-L94 is intentionally minimal.** No `@daily`, no seconds, no `L` (last day of month), no `#` (nth weekday), no name aliases like `MON`. We follow the upstream's restraint: implementing only `*`, `*/N`, `A-B`, `A,B,C`, and absolute integers gives 90% of the real-world coverage for ~200 lines of code. The Vixie extensions and the modern systemd-style aliases would each add ~50 lines plus their own edge-case tests; explicitly out of scope for a teaching chapter.

3. **L91's "0 and 7 = Sunday" is the field's single design-by-committee scar.** Old `cron` accepted only 1-7 with 1=Sunday; BSD `cron` flipped it to 0-6 with 0=Sunday; Vixie unified them by accepting *both* 0 and 7 for Sunday. We model that by parsing into an 8-element bitmap and folding index 7 onto index 0 at the end of `parseField`. The test `TestShouldRun_DowSundayAcceptsBoth0And7` is the canary.

4. **L111-L129's "isolated session vs main session" is not a cron concern at our layer.** That's a delivery-side concern that `Scheduler.Tick()` callers handle by reading the schedule's `Payload` (an opaque `json.RawMessage` we pass through verbatim). The cron primitive itself only knows about *when* to fire and *what name* to return; *what to do with it* is the consumer's problem. This is the same separation as s10's event log: the primitive holds the data, the policy lives outside.

5. **The "delivery pinned to originating session" rule at L152-L153 is a no-`SetDelivery`-here invariant.** A common bug shape is to let the user re-target the delivery channel after creation ("oh, send these to #ops instead of #general"). The fix is to make `Payload` immutable at the schedule level — if you want a new target, you create a new schedule. We bake that in by making `Payload` a `json.RawMessage` set at construction time, with no setter.

## Reading map

| Topic | Upstream file | Lines | Mapped chapter |
|-------|---------------|-------|----------------|
| Why scheduling matters (mental model) | `guide/scheduling-and-automation.md` | L9-L75 | s13 cross-ref |
| Five-field grammar | `guide/scheduling-and-automation.md` | L84-L94 | s13 (this) |
| "Store UTC. Display local." rule | `guide/scheduling-and-automation.md` | L96-L107 | s13 (this) |
| Session targeting / payload / delivery | `guide/scheduling-and-automation.md` | L111-L153 | s13 consumer concerns |
| Heartbeat vs Cron table | `guide/scheduling-and-automation.md` | L250-L272 | Appendix A cross-ref |
| Anti-patterns (polling, pollution, no timeout) | `guide/scheduling-and-automation.md` | L278-L319 | s13 + s12 cross-ref |
