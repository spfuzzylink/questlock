# Go execution validation — v0.10.0

Same work, more workers, one broker. Does reuse still hold? Does the shared token
quota add overhead? We ran both, including the less flattering case where every
task is new.

The **CPU control-plane benchmark** uses one Mac: real authenticated HTTPS,
SQLite, claims, result lookup and token-ledger CAS. Its providers return scripted
answers and usage; resource samples are synthetic. No model, GPU, Langfuse export
or second physical host is involved in those benchmarks. The separate live local
model check appears below. Do not use the benchmark numbers as model throughput,
quality, time-to-first-token, invoice savings or whole-chat credit evidence.

## What ran

- Go 1.27.1, darwin/arm64, Apple M5; benchmark parallelism 10.
- 1, 2 or 4 independent coordinators and scoped clients share one broker. Each
  coordinator owns a local admission queue; two fixture credentials share a scope.
- 1,000 submissions per sample, three samples per case. Cases run in a fixed
  order, with no concurrent build or test jobs. Startup readiness is outside timing.
- Repeated workload: one common snapshot plus 100 distinct revisions. Unique
  workload: 1,000 distinct snapshots. Pending contention is reported, never retried.
- Quota-on uses the real scope/day ledger, with caps large enough for all declared
  reservations. Quota-off still checks for an existing policy on fresh work.

All 36 final samples passed result accounting and declared response checks, with
zero unexpected errors. Repeated samples made **101 scripted generations and
reported 2,727 synthetic tokens** each; unique samples made 1,000 and reported
27,000. Remaining repeated submissions either reused the exact answer or returned
pending. A pending submission is not a completed task or a measured saving.

## Final observations

Each time below is the median of three **batch means per submission**, followed by
the smallest and largest sample mean. It is not individual request latency or a
percentile. MB means cumulative bytes allocated divided by submissions, in decimal
millions; it is not peak memory or resident memory. Completed and pending columns
show counts out of 1,000 across the three samples.

| Coordinators | Workload | Shared quota | Mean ms/attempt: median (range) | Allocated MB/attempt: median | Completed range | Pending range |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Repeated | Off | 0.143 (0.138–0.170) | 0.037 | 1000–1000 | 0–0 |
| 1 | Repeated | On | 0.248 (0.248–0.251) | 0.119 | 1000–1000 | 0–0 |
| 1 | Unique | Off | 0.520 (0.520–0.525) | 0.098 | 1000–1000 | 0–0 |
| 1 | Unique | On | 4.973 (4.939–4.977) | 6.315 | 1000–1000 | 0–0 |
| 2 | Repeated | Off | 0.085 (0.085–0.089) | 0.038 | 998–1000 | 0–2 |
| 2 | Repeated | On | 0.179 (0.179–0.179) | 0.124 | 1000–1000 | 0–0 |
| 2 | Unique | Off | 0.388 (0.385–0.392) | 0.103 | 1000–1000 | 0–0 |
| 2 | Unique | On | 4.855 (4.846–5.023) | 11.325 | 1000–1000 | 0–0 |
| 4 | Repeated | Off | 0.090 (0.087–0.090) | 0.038 | 996–1000 | 0–4 |
| 4 | Repeated | On | 0.193 (0.191–0.196) | 0.153 | 995–997 | 3–5 |
| 4 | Unique | Off | 0.399 (0.399–0.410) | 0.103 | 1000–1000 | 0–0 |
| 4 | Unique | On | 6.210 (6.140–6.329) | 20.904 | 1000–1000 | 0–0 |

The quota has a cost. Its aggregate JSON ledger grows with reservations and
contenders can reread it. In the four-coordinator unique case, the final mean was
6.210 ms/attempt with 20.90 MB allocated/attempt; the
quota-off mean was 0.399 ms/attempt. More clients did not produce linear scaling.
The ledger remains bounded at 1,000 entries/day and 32 CAS attempts/operation;
useful production capacity needs measurement on your workload.

We used the first complete run to find avoidable decoding work. Replacing nested
maps and a second JSON parse with a strict one-pass decoder changed that same
four-coordinator unique/quota-on mean from 7.836 to 6.210 ms/attempt
and median allocation from 26.83 to 20.90 MB/attempt. Both complete runs,
all samples, zero-error counters and pending deferrals are in the
[raw report](execution-validation-v0.10.0.json). Runs were sequential, not randomized;
this is an observation, not a statistical significance claim. Earlier pilots do
not supply these timings.

## Live local model check — 2026-10-22

The earlier preflight correctly stopped under high memory pressure with about
215 MiB available. Later, pressure returned to normal. No code fix, weaker
threshold, app termination or daemon change was needed.

We ran the published v0.10.0 Go example against installed `qwen3.5:4b` weights
through Ollama 0.40.3. Only the sample's model and validation revision changed.
A private broker started empty. A loopback counting relay forwarded real runtime
requests and permitted at most one generation; it supplied no answers itself.
The default admission policy and single inference worker stayed unchanged.

| Invocation | New input tokens | New output tokens | Durable | Result |
| --- | --- | --- | --- | --- |
| Fresh support task | 57 | 20 | Yes | One real generation |
| Exact same snapshot | 0 | 0 | Yes | Identical stored answer |

There was exactly one `/api/generate` request. Metadata reads still checked
runtime identity. One token reservation went from 2,048 input + 128 output to
the known 57 + 20 usage; the quota stayed unfrozen. Paid fallback was disabled
and provider API spend was $0. Hardware, electricity and time still count.

Native pressure was normal in three preflight readings, five during-run
readings and the final reading. The minimum sampled available memory during
execution was about 3.71 GiB. This is sampled headroom, not peak model-memory
usage or proof against every OOM. `keep_alive_seconds: 0` requested model release;
no model was resident afterward. The owned broker and relay were stopped.

The answer covered the invoice fix and request to try again, using customer-facing
wording rather than an internal handoff. That is one factual review, not a general
quality score or human acceptance. This check establishes live local generation,
exact result reuse and quota settlement; it does not measure GPU acceleration,
CPU-only inference, physical multi-host throughput or whole-chat credit savings.
The [raw report](execution-validation-v0.10.0.json) retains the original benchmark
samples and adds this live record, including the approved spec and model digest.

## Reproduce it

The before source is
[`93ac39e`](https://github.com/spfuzzylink/questlock/commit/93ac39ec0f9fd6edc21282392375d586b025b189).
The final decoder source is
[`d1b86a2`](https://github.com/spfuzzylink/questlock/commit/d1b86a289fe32ba3d79dfa528e9be24c7ef2e3a1).
Those implementation commits are retained by [PR #23](https://github.com/spfuzzylink/questlock/pull/23).
In a clone, fetch `refs/pull/23/head` before checking out either source hash:

```sh
git fetch origin refs/pull/23/head
```

Use separate checkouts and run each without other test/build jobs:

```sh
go test ./execution -run '^$' -bench BenchmarkCoordinatorCPUControlPlane \
  -benchtime=1000x -count=3 -benchmem -timeout=15m
```

The [raw report](execution-validation-v0.10.0.json) includes source trees, raw-output
hashes and every metric. Do not increase the fixed operation count past 1,000;
that exceeds this benchmark's daily reservation bound.

## Safety and regression checks

The final source passed `make check`: 74 Python checks, the public-source/history
guard, Go vet and race tests across all packages. Focused cases cover two HTTPS
clients, reuse, input/output/total quota contention, policy omission, UTC rollover,
unknown generation, settlement/rollback failure, resource pressure, cancellation,
telemetry failure and strict task/ledger parsing. These cover actual state changes
with scripted endpoints; they do not establish live GPU compatibility.

An independent release reviewer ran focused race cases three times and compared
the old and new decoder on 16,004 seeded valid and byte-mutated inputs. Acceptance
and decoded values matched. That corpus supplements the explicit malformed-field,
overflow and reservation tests; it is not a proof for every possible input.

All benchmark generation is scripted: **0 model calls and $0 provider API spend**.
Development-assistant usage is excluded and unmeasured. [The Go guide](go-integration.md)
explains deployment, trusted-client quota boundaries and CPU/GPU responsibilities.
