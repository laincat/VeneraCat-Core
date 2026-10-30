# Magpie routing decisions

English | [简体中文](MAGPIE-ROUTING.md)

Current role: this is the earlier hard-coded routing prototype, not yet connected to iOS. Under the new [standard API and plugin boundaries](AI-CORE-BOUNDARY.en.md), Core retains candidate validation and strategy execution while plugins supply policies. This page records prototype behaviour; it does not establish Magpie routing-plugin compatibility or make this scheduler mandatory for standard calls.

Updated 2026-10-31. `magpierouting` decides which provider account and model answers a request. It is a pure function: it opens no connection and reads no clock, so identical inputs always produce an identical plan.

## Why it lives in Core

Upstream Magpie runs a local gateway, and routing happens inside that process. iOS embeds the plugin runtime instead and has no gateway, so the choice has to exist once in the shared layer rather than be written twice. The desktop client initially talks to a locally installed Magpie and does not use this package; iOS does.

## Order of decisions

1. **Hard requirements**: vision, tools, reasoning, context length, wire dialect. A candidate that fails is **removed**, not ranked lower — a model without vision cannot "try a bit harder" at an image.
2. **Task tier**: the difficulty (simple/normal/complex) maps to allowed models and providers. An explicit model pick ignores the tier and is never replaced.
3. **Availability**: ended subscriptions are out; an exhausted window removes that model; a cooling-down account stays usable but sorts last; a sign-in that needs attention is excluded outright.
4. **Policy ordering**: optional "use what expires soonest", "prefer cheaper", and session affinity. A stable provider/account/model tiebreak makes the plan reproducible.

## Where implementations usually go wrong

| Rule | Why |
|---|---|
| An unreported allowance is not zero | "Unknown" and "used up" read differently on a quota card, and treating unknown as zero spends the wrong account |
| An auto-renewing plan is not "expiring soon" | It has no allowance to waste; ordering it by end date misreads it as about to lapse |
| A cooling-down account may still answer | With nothing healthy left, a cooling account beats failing the request; ordering keeps it last |
| An explicit pick is never substituted | Silently answering with another model is worse than failing: the user asked for A and would get B |
| A model that says nothing about dialects is not excluded | Silence is not refusal; only a declared different dialect excludes it |
| A per-model window outranks a general one | When a vendor meters per model, a general window cannot represent that model being spent |

## Verification

`go test ./magpierouting` covers every rule above and additionally proves the plan is reproducible (the same input 20 times in a row keeps the order). Coverage is 92.8% today. Commands and output stay in CI and local logs; no conclusion beyond the numbers is restated here.

Not done yet: `magpierouting` is not wired into a client. The difficulty classifier on iOS, real usage collection, and handing a plan to `magpieplugin` to send the request are all still missing. The package now provides reliable behaviour for one layer only: given a catalogue and account state, choose the candidates.
