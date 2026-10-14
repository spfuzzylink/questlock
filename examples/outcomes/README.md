# Synthetic human feedback examples

These files contain invented task IDs, digests, usage, cost estimates, and human
effort. They test the **v0.4.0 experimental source candidate** without calling a
model. Manual `outcome record` input is explicitly caller-reported accounting;
these fixtures are not provider receipts, invoices, or measured customer savings.

For a self-contained demonstration with temporary state:

```sh
go build -trimpath -o bin/questlock ./cmd/questlock
./bin/questlock outcome quest
```

The quest runs three simple and three complex scenarios using the authenticated
broker and deterministic metadata. It does not need provider keys or installed
model weights. The scenarios exercise explicit acceptance, acceptance removal
after rewrite, receipt recovery, cohort cost coverage, stale feedback conflicts, and durable restart.

## Walk through a rewrite

Run the commands below from the repository root against an existing dedicated
broker scope. Follow [broker setup](../../docs/model-workflows.md) first. Give this
demo an unused scope so its expected versions start at one; use the protected
scope-token file rather than storing its value in an example.

```sh
export QUESTLOCK_TOKEN_FILE="/absolute/path/to/private/outcome-demo.token"
./bin/questlock outcome record --broker http://127.0.0.1:8080 \
  --task feedback-demo --revision synthetic-1 \
  --run examples/outcomes/completed-run.json
./bin/questlock outcome show --broker http://127.0.0.1:8080 \
  --task feedback-demo --revision synthetic-1
```

The new outcome is at version 1 and is `unreviewed` even though its attempt
passed its checks. Record the synthetic reviewer's initial acceptance:

```sh
./bin/questlock outcome feedback --broker http://127.0.0.1:8080 \
  --task feedback-demo --revision synthetic-1 --expected-version 1 \
  --event examples/outcomes/accept.json
```

The outcome is now `accepted` at version 2. Its final artifact digest matches the
synthetic first draft. Record a later correctness rewrite:

```sh
./bin/questlock outcome feedback --broker http://127.0.0.1:8080 \
  --task feedback-demo --revision synthetic-1 --expected-version 2 \
  --event examples/outcomes/rewrite.json
./bin/questlock outcome report --broker http://127.0.0.1:8080 \
  --task feedback-demo --revision synthetic-1 --hourly-rate-usd 60
```

The state is `rewritten` at version 3. There are no currently accepted outcomes,
so the unit-cost fields are `null`. The original attempt cost and recorded review
time remain in the numerator. Questlock does not silently discard that work.

Explicitly reaccept the corrected final artifact:

```sh
./bin/questlock outcome feedback --broker http://127.0.0.1:8080 \
  --task feedback-demo --revision synthetic-1 --expected-version 3 \
  --event examples/outcomes/reaccept.json
./bin/questlock outcome report --broker http://127.0.0.1:8080 \
  --task feedback-demo --revision synthetic-1 --hourly-rate-usd 60
```

The outcome is `accepted` at version 4 with a different final digest. The
example has one $0.0015 generation estimate and 2.75 recorded incremental review
minutes. At the explicitly supplied $60/hour rate, its recorded work cost is
$2.7515 per currently accepted outcome. These invented values explain the
arithmetic; they demonstrate no actual cost saving.

An exact retry of the reacceptance command recovers its original version-4
receipt. It does not add effort or increment the version. Changing that event's
payload while reusing its ID is a conflict. A new event based on an old expected
version must be reconciled against `outcome show` rather than blindly published.

## Include the work that failed

```sh
./bin/questlock outcome record --broker http://127.0.0.1:8080 \
  --task failed-demo --revision synthetic-1 --run examples/outcomes/failed-run.json
./bin/questlock outcome feedback --broker http://127.0.0.1:8080 \
  --task failed-demo --revision synthetic-1 --expected-version 1 \
  --event examples/outcomes/reject.json
./bin/questlock outcome report --broker http://127.0.0.1:8080 \
  --cohort examples/outcomes/cohort.json --hourly-rate-usd 60
```

The cohort contains two task revisions, with one currently accepted outcome.
Both generation estimates count: $0.0015 for the rewritten/reaccepted task and
$0.004 for the rejected task. Review time totals 3.25 minutes. The combined
recorded-work cost is therefore $3.2555 per accepted outcome. Reporting only the
accepted task would hide the rejected task's work.

The cohort file is an explicit array of `{ "id", "revision" }` task identities.
It is not a query that silently changes when another task is recorded. Each
selected task is read at its reported version; a report is not one atomic
cross-task snapshot while feedback continues to arrive.

## Record reuse without charging generation twice

`reuse-run.json` has a separate run ID, `reused: true`,
`generation_called: false`, known zero current usage, and zero current
generation cost with basis `reuse_zero`. Import it into the same example task:

```sh
./bin/questlock outcome record --broker http://127.0.0.1:8080 \
  --task feedback-demo --revision synthetic-1 --run examples/outcomes/reuse-run.json
```

The recorded original generation cost remains unchanged. Human acceptance stays
linked to its explicitly accepted run and final artifact; reuse does not imply
the reviewer accepted a new invocation. See the actual current version before
sending further feedback.

## Keep unknowns visible

```sh
./bin/questlock outcome record --broker http://127.0.0.1:8080 \
  --task unknown-demo --revision synthetic-1 --run examples/outcomes/unknown-run.json
./bin/questlock outcome feedback --broker http://127.0.0.1:8080 \
  --task unknown-demo --revision synthetic-1 --expected-version 1 \
  --event examples/outcomes/unknown-effort.json
./bin/questlock outcome report --broker http://127.0.0.1:8080 \
  --task unknown-demo --revision synthetic-1 --hourly-rate-usd 60
```

This stopped attempt has unknown usage/cost, and its event has unknown effort.
The report exposes both missing inputs rather than treating them as zero. An
omitted hourly rate also prevents a combined work-cost result, even when
generation estimates and recorded minutes are known. A task with no feedback
has unknown human effort and prevents a complete total-cost result until a
reviewer explicitly reports it. Supply zero minutes when zero is the intended
known value; omission is not zero.

Outcome records are content-free metadata. Identifiers and hashes are still
retained scope data, and a scope credential does not prove a human supplied an
event. The caller must obtain an intentional review decision. See the
[outcome guide](../../docs/outcomes.md) for accounting, access, and failure limits.
