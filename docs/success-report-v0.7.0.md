# A first task before a first token

**Questlock 0.7.0 release evidence — 2026-10-17.**

The goal was practical: install it, connect a coding client, and finish something
useful before setting up a provider account. We also used Questlock while
building the release. This report separates that measured work from the hosted
assistant that coordinated development.

## What worked

| Check | Observed result |
| --- | --- |
| Fresh first task through the actual MCP process | Both tools discovered; checked JSON edit published durably; every declared check passed. |
| Exact first-task repeat | Same immutable result, `reused: true`, zero new generation. |
| Native Codex setup in a disposable home | Full candidate and live native read passed; notification array, other MCP server and unrelated settings preserved. |
| Native Claude Code 2.1.302 in a disposable home | Setup, native MCP connection, first task, doctor and shutdown passed; unrelated settings preserved. |
| Recovery | Automated process-death, timeout, validation failure, absent-file restore and concurrent-repair tests passed. |
| Claude Desktop | Binary MCPB construction, archive/manifest contracts and standalone MCP protocol tested. **Native Desktop import and connection remain unverified.** |
| Full local Codex loop | Codex CLI used installed `qwen3.5:4b` through Ollama, read a fixture with a tool, returned the correct value, and exited successfully. |

The native local Codex run reported **9,245 input and 265 output tokens**, all
from the local model. Its isolated environment had no provider credentials.
It also emitted an unknown-model-metadata warning. One file-reading exercise
establishes a working path, not broad coding quality or parity with hosted Codex.

A separate trial through the new `local-agent` wrapper read the fixture through
a tool and exited zero, but emitted **no final answer**. It reported 9,127 local
input and 123 output tokens. We retain that as a quality limitation, not a
successful accepted task. Process success alone does not prove useful output.
The two local harness trials, the bounded drafting attempt and the two local
checks below total 19,244 local tokens in their native counters. They are not
hosted tokens saved.

The fresh checked edit took 1.047 ms and its repeat 0.891 ms inside the revision
operation. These are single observations, exclude service/client startup, and
are not an inference-speed benchmark.

## We used it to build it

Eight tracked checked-revision publications updated onboarding documentation,
the fixed brand asset, setup links, the profile introduction and archive examples. Their source hashes, counted edits,
output checks and durable immutable artifacts all passed. Each publication
recorded **0 new input tokens, 0 output tokens and $0 provider charge**.

| Development revision | Output SHA-256 |
| --- | --- |
| `onboarding-contract-1` | `8cf442551102dec442665bf931422cf6a13c6864450b433d5b700e2114a7aae9` |
| `onboarding-final-contract-2` | `f22789276114291b3af1fc045eb5a48877dc66bd0f86c38b614c9e741f0cad0d` |
| `timeless-canonical-1` | `e8121dd023da8f1d76aa33e633ae03c44fffcc5f7794377e3b2ccb89b145ab29` |
| `guided-links-1` | `970d50eb81692e4f8cee3a0202b243f44aefffddd9e1744e066054d1559bfe54` |
| `guided-links-2` | `71dc4de63c5c05b62ea6a5c11dd6443ce39a544c1b8d570d87819253e9d4fa3f` |
| `guided-links-3` | `0e12e0839a7f35731f97db190477fd400ffc4d5e1665ff31c66b24280de2767c` |
| `easy-start-2` | `08d26487a3c54454e08f3974f0f8f95c4498d54c245aceca66411d8b2fa29095` |
| `archive-examples-1` | `dd0b7064473b2694d75a801def7933803d191f1626ebaeb1bc1aa5c41e1d181f` |

These are eight successful publications, including one that applied six literal
replacements. They are not eight avoided hosted calls or a measured hosted-token
counterfactual.
The latter documents may receive later reviewed edits; the hashes identify
those intermediate checked outputs, not all final file bytes.

We also tried one bounded local README drafting task. It used **179 input and
44 output tokens**, failed its required JSON check and withheld the answer.
That failure remains in the accounting. There was no paid fallback or retry to
force a favorable result. Provider charge was $0; local hardware and time remain
costs. Checks and assistant review do not establish human acceptance.

Two bounded local fact checks ran against approved compact snapshots. The first
reported 107 input / 28 output tokens, the second 104 / 22. Both passed their
strict declared JSON checks. Exact repeats reused the same immutable results,
with **0 new generation tokens**. That avoided **211 input + 50 output = 261
local model tokens** on those two repeats. The original local requests also had
$0 provider charge, so this establishes repeated computation avoided, not a
hosted invoice reduction or broader coding quality.

The accounting cutoff at 2026-10-17 02:19:37 UTC contains **11 tasks, 14 runs,
14 attempts, $0 known provider inference charge and no human acceptance events**.
It includes a checked revision stopped by an incorrect default broker endpoint;
its exact stopped record was recovered without inference before the endpoint
was corrected. One oversized profile-edit request was rejected before execution
and remains a separate preflight failure. CLI protocol fixtures and the two
native Codex trials are outside this development cohort. Human effort remains
unknown and cost per accepted task is null, not zero. Later release work is
outside this cutoff.

## What can we honestly say about savings?

- Added paid provider API charges for these recorded development tasks: **$0**.
- New model usage for the eight deterministic publications and first-task/repeat:
  **0 tokens**.
- Hosted tokens avoided by the entire build: **unknown**. We did not perform a
  matched build without Questlock; the coordinating hosted assistant still used
  its normal allowance. Provider prompt caching is not a Questlock saving.
- USD that the whole build would have cost without Questlock: **unknown**. A
  subscription allowance is not an API invoice. Local and hosted tokenizers
  differ, so pricing the local token count at a hosted rate would not establish
  actual avoided charges.
- Human acceptance and rework cost: **not measured for this build**; cost per
  accepted task remains unknown.

This release does not establish a 99% reduction in hosted Codex usage. A valid
comparison needs the same starting work and
acceptance criteria, all failed attempts, comparable quality, native hosted
usage counters and explicit human rework. Local agents and shared results are
useful ingredients; replication alone does not produce a stronger model.

[Try it](onboarding.md) · [Engineering boundaries](architecture.md) ·
[Full validation record](validation.md)
