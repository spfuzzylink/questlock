# Six tasks, one checked contract

Start from the supplied snapshots. Keep the original facts, checks, task revision
and master prompt; choose execution settings explicitly. The
[validation report](../../docs/local-team-validation-v0.9.1.md) records every
policy, failure and unknown counter. The original v0.9.0 report is unchanged.

| Case | What must be checked |
| --- | --- |
| `ticket` | Copy the exact issue and deadline |
| `route` | Select only the permitted local provider |
| `queue` | Calculate the waiting count |
| `uncertain` | Preserve an uncertain publication's identity |
| `resources` | Respect CPU, memory and every reserve; preserve queued work |
| `rework` | Remove rewritten work from acceptance; leave unknown cost null |

These are three simple extraction cases and three bounded reasoning cases.
They are not a representative coding benchmark or evidence of human acceptance.

## Inspect the saved comparison without a model

From the repository or extracted archive root:

```sh
questlock team benchmark \
  --suite examples/teams/qualified-suite-v0.9.1.json \
  --local-dir examples/teams/local-observations-v0.9.1 \
  --baseline-dir examples/teams/codex-baseline-v0.9.0
```

This reads historical operator-provided receipts. It performs no inference,
download or account access. The baseline is the same six saved Codex runs,
reporting 65,800 input-plus-output tokens. Their original JSONL streams remain
private. These receipts are importable observations, not remote attestation.
Final status reads have zero fresh usage; the report separately totals the
originating master/reviewer generations and every failed development policy.

## Run your own local experiment

The saved suite pins its model digests and per-role settings. Review it before
using it. Install the listed weights deliberately and check hardware needs and
model licenses; Questlock never downloads models automatically. The
[team guide](../../docs/local-agent-teams.md) starts with one actually completed
9B task and explains the full failure, timeout and trust boundaries.

Keep task files and receipts private. The following creates a new working copy
without overwriting previous evidence:

```sh
umask 077
set -C
mkdir -p .questlock-demo/requests .questlock-demo/local .questlock-demo/plans
cat examples/teams/qualified-suite-v0.9.1.json > .questlock-demo/suite.json
cat examples/teams/reasoning-resources.json > .questlock-demo/resources.json
python3 - <<'PYTHON'
import json
from pathlib import Path
suite = json.loads(Path(".questlock-demo/suite.json").read_text())
for case in suite["cases"]:
    target = Path(".questlock-demo/requests") / (case["id"] + ".json")
    with target.open("x") as out:
        json.dump(case["request"], out, indent=2)
        out.write("\n")
PYTHON
for case in ticket route queue uncertain resources rework; do
  questlock team plan --workspace .questlock-demo/requests \
    --request "$case.json" > ".questlock-demo/plans/$case.json"
done
```

Review every request and plan before these intentional model calls:

```sh
for case in ticket route queue uncertain resources rework; do
  questlock team run --workspace .questlock-demo/requests --request "$case.json" \
    --config .questlock-demo/resources.json --timeout 8m \
    > ".questlock-demo/local/$case.json" 2> ".questlock-demo/local/$case.stderr"
done
questlock team benchmark --suite .questlock-demo/suite.json \
  --local-dir .questlock-demo/local \
  --baseline-dir examples/teams/codex-baseline-v0.9.0 \
  > .questlock-demo/comparison.json
```

There is no retry loop. Retain failures and stderr; an uncertain claim requires
inspection. A fresh broker needs real master and reviewer generations. A
matching completed snapshot in the same scope may instead be reused. Changed
or expired snapshots do not become permission to regenerate automatically.

The supplied configuration extends only the admitted task deadline to five
minutes. The normal default is 45 seconds; `--timeout` alone does not change it.
Each role also declares its generation HTTP deadline. Memory headroom, pressure
thresholds and queue bounds keep their defaults. `keep_alive_seconds: 0` asks the
shared daemon to release the model after each response, which can add cold starts.

For three workers, use the role-by-role authenticated HTTPS commands in the
[guide](../../docs/local-agent-teams.md#share-work-across-nodes). Save all phase
receipts, then save `team status` as `local/CASE.json`. Workers keep private files;
only the broker owns SQLite. The published topology used three scoped identities
on one physical Mac. Physical second-machine qualification remains separate.

## Read execution contracts honestly

The saved suite uses mixed models, framing and runtime policies. Exact completed
results remain reusable; failed or uncertain policies remain in the history.
`policy_revision` changes execution identity while preserving the same approved
task/data/check contract. Changing facts, checks, task revision or canonical
master instructions invalidates a saved baseline binding.

Optional `output_types` and `field_roles` are manually declared operator input,
not annotations inferred from expected answers. They add a decoder contract and
system guidance; reviewers see the declarations as reference data. The original
semantic checks and every reviewer approval still have to pass. Comparison rows
show the extra assistance and explicitly reject identical-information model
parity. The six-case policies were tuned on these same fixtures.

Two independently designed contracts are saved separately in
`independent-validation-v0.9.1.json`. They were frozen before their first local
inference, with no later task/policy changes and no Codex baseline. Their results,
including failures, do not enter the six-case percentage.

## Use another baseline only deliberately

Prefer the supplied matching baseline; this walkthrough makes no new hosted
call. For another approved task, `team prompt` emits its exact canonical user
prompt without inference. An independently authorized Codex run can save bounded
native JSONL output, which `team baseline-record` imports:

```sh
questlock team prompt --workspace .questlock-demo/requests --request ticket.json
questlock team baseline-record --request .questlock-demo/requests/ticket.json \
  --codex-events /private/approved-ticket.events.jsonl --model YOUR_CODEX_MODEL
```

The operator must verify the actual model, authentication, prompt and complete
event stream. A hosted run consumes its own allowance; it is a separate
experiment, never a fallback. Invalid/oversized streams stay failed evidence.

`benchmark` retains all six rows and exits unsuccessfully if any result is
missing, failed, mismatched or has unknown hosted usage. It then leaves the
reduction percentage null. Repeating a completed exact request reports no new
generation. Sum originating phase receipts as well as repeats; local reasoning
tokens and failed work still count as computation.

The metric covers observed hosted input plus output for these bounded tasks.
Subscription-credit reduction, whole-build hosted usage, hypothetical USD
invoices and cost per human-accepted task remain unmeasured. Human acceptance
and later rework need separate [outcome records](../../docs/outcomes.md).
