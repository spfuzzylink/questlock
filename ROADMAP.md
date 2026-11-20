# Questlock roadmap

Questlock's goal is lower cost per accepted task, including human rework.
Priorities below are directions for contribution and evaluation, not delivery
dates or promises of accuracy, savings or foundation acceptance. Public
[issues](https://github.com/spfuzzylink/questlock/issues) track concrete work.

## Qualify reuse before expanding its claims

Exact task snapshots and explicitly approved question forms are the established
reuse paths. Free-form related-question reuse remains experimental. The next
work is to qualify it against independently labelled workloads, changed facts
and constraints, unsupported sources, abstentions and failures. Measure total
source, embedding and verifier work against a matched fresh-answer baseline.
Retain incorrect acceptances, unknown outcomes and unfinished work.

The active qualification tracker is
[#44](https://github.com/spfuzzylink/questlock/issues/44). Existing failed cases
remain evidence; they are not cleared by changing a policy or adding machines.

## Validate capacity coordination on representative deployments

Extend the existing bounded waiting and eligible-worker dispatch evidence to
real multi-machine workloads. Keep model/runtime identity, task contracts,
scope isolation and quality checks fixed during comparisons. Measure queueing,
latency, capacity and review effort separately. Current scripted coordination
on one physical host does not establish physical-cluster throughput or a
general accuracy improvement.

## Improve adoption and operational clarity

- Exercise installation, upgrades and recovery through supported native clients.
- Add reproducible Go examples for recurring developer and platform workflows.
- Keep provider usage, result reuse, runtime caching and human acceptance distinct.
- Test backup, archive restoration and damaged-state recovery with synthetic data.
- Improve contributor guidance and record production adopters only with permission.

## Current boundaries

One authoritative broker on one host remains the storage authority. Remote
cooperating workers do not make it a replicated consensus service or an agent
sandbox. Cloud-platform integration proposals should identify a concrete user
need, deployment assumptions and an acceptance test before introducing a new
dependency. See [architecture](docs/architecture.md),
[security](SECURITY.md) and [validation](docs/validation.md).
