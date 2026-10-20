# Local teams: what passed, what did not

**The 99% hosted-usage target is not established.** The control path works; the
tested small model and this workstation did not complete the live team suite.
We kept the failures rather than counting zero hosted calls as successful work.

Observed on 2026-10-19, using Ollama 0.40.3, installed `qwen3.5:4b`, and native
Codex `0.163.0-alpha.10.2`. [Machine-readable observations](local-team-validation-v0.9.0.json)
contain all six cases for both local profiles. Raw requests, credentials and
event streams remain private. This is an operator observation, not remote
attestation or a representative coding benchmark.

| Experiment | Result |
| --- | --- |
| Codex subscription baseline, `gpt-6.1-sol`, `xhigh` | All six declared answer checks passed; 65,400 input + 400 output tokens reported. |
| Native Codex OSS local profile | Four generation invocations: one master passed, its reviewer exceeded the 512-output-token validation cap; two other masters failed answer checks. Three tasks were blocked before inference. No completed team case. |
| Direct Ollama local profile | All six tasks blocked by resource admission before inference. No completed team case. |
| `team quest` | Six scripted-provider cases passed using a real HTTPS broker and three distinct scoped clients on one physical host. Exact repeats added zero scripted calls. No actual model calls. |

The native local suite reported **6,362 input + 1,384 output local tokens**.
The baseline's 65,400 input tokens include 33,280 cache reads; its 400 output
tokens include 296 reasoning tokens. Cache categories and reasoning are not
added twice. The hosted and local native runs received the same emitted user
prompt and deterministic check contract. Their system context and harness
overhead differ; local reviewers add work absent from the single-answer baseline.
The direct Ollama profile uses its own provider framing.

All local team runs used **zero hosted tokens and $0 paid-provider API charges**.
That is not a measured reduction for equivalent accepted work: no local team
finished every gate. Reduction percentage, Codex credit reduction, invoice
savings and cost per human-accepted task therefore remain unknown. The six
deliberate baseline calls consumed the existing Codex subscription. The hosted
build coordinator and independent release review also consume normal allowance;
whole-build usage is excluded and unmeasured.

Before the suite, a separate local architecture-review request failed its JSON
check (172 input + 314 output tokens). An initial native attempt stopped on a
startup-warning parser defect with unknown usage. A separately approved
readiness probe succeeded (1,524 input + 100 output tokens), and its saved stream
was replayed offline to qualify the parser repair. The original uncertain
attempt remains stopped. No repeated inference repaired accounting or parsing.

The suite then found a second warning-path variant in saved hosted receipts.
Offline reimport recovered their actual counters without calling Codex again.
Default memory admission stayed conservative: one inspection found only
136,298,496 free bytes, below reserved headroom. Thresholds and checks were not
relaxed, models were not substituted automatically, and failed cases were not
dropped. Pressure refusals are retained alongside quality failures.

This qualifies broker coordination, strict checks, safe stopping, receipt import
and model-free reuse behavior. It does not qualify `qwen3.5:4b` as a general
engineering master/reviewer, a physical second-machine deployment, faster
inference, or replacement of the hosted conversation backend. A larger suitable
local model and adequate measured memory headroom need a separately approved,
matched run. [Reproduce the workflow](../examples/teams/README.md).
