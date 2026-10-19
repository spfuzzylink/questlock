# Six tasks, one checked contract

Run this walkthrough from a private working directory with Questlock, Ollama and
an already installed model. It deliberately performs local inference. Start
with the [team guide](../../docs/local-agent-teams.md) for failure and trust
boundaries. Keep tasks, tokens, logs and receipts outside source control.

| Case | Class | What must be checked |
| --- | --- | --- |
| `ticket` | Simple | Copy the exact issue and deadline |
| `route` | Simple | Select only the permitted local provider |
| `queue` | Simple | Calculate the waiting count |
| `uncertain` | Bounded reasoning | Preserve an uncertain publication's identity |
| `resources` | Bounded reasoning | Respect both CPU and memory capacity |
| `rework` | Bounded reasoning | Remove rewritten work from acceptance; leave unknown cost null |

These are explicit extraction and policy cases, not a representative software
engineering benchmark. The JSON suite calls the latter three `complex` only to
distinguish them within this small set.

## Prepare and approve

```sh
umask 077
set -C
mkdir -p requests local baseline events
questlock team suite --model qwen3.5:4b --driver codex-oss > suite.json
python3 - <<'PY'
import json
from pathlib import Path
suite = json.loads(Path("suite.json").read_text())
for case in suite["cases"]:
    target = Path("requests") / (case["id"] + ".json")
    with target.open("x") as out:
        json.dump(case["request"], out, indent=2)
        out.write("\n")
PY
```

Inspect every request before running. Use `--driver ollama` for the direct local
route. `codex-oss` requires the compatible native client described in the guide.
`suite` reads local model metadata; it neither generates nor downloads a model.

Run each approved local case exactly once, preserving output even on failure:

```sh
for case in ticket route queue uncertain resources rework; do
  questlock team run --workspace requests --request "$case.json" \
    --client-bin /absolute/path/to/codex > "local/$case.json" \
    2> "local/$case.stderr"
done
```

The default managed broker keeps claims and results durable between invocations.
Do not add a retry loop. Retain failed reports and investigate them. For separate
nodes, use the role-by-role HTTPS commands in the guide and save all phase
receipts; then save `team status` as `local/CASE.json`.

## Obtain a deliberate Codex baseline

For each case, emit its exact approved baseline user prompt without a model call:

```sh
questlock team prompt --workspace requests --request ticket.json > ticket.prompt.json
```

Run the `prompt` value once through your explicitly approved native Codex model
using noninteractive JSONL output. Keep the supplied task and data unchanged,
use an empty workspace with tools disabled, and save the complete event stream
as `events/ticket.jsonl`. Select the model and authentication policy deliberately:
this step consumes hosted allowance if the chosen model is hosted. Questlock
does not start this baseline or access your account credentials.

The importer accepts bounded event files of at most 1 MiB. Keep the complete
original stream private; an oversized or invalid stream is a failed baseline,
not permission to remove inconvenient events or retry generation.

Import that observed run, even if the model failed:

```sh
questlock team baseline-record --request requests/ticket.json \
  --codex-events events/ticket.jsonl --model YOUR_CODEX_MODEL \
  > baseline/ticket.json
```

Repeat the capture/import for `route`, `queue`, `uncertain`, `resources` and
`rework`. Keep every original event file and every failure. The importer binds
the approved task and emitted prompt hashes to the supplied receipt; the
operator must verify that those are what was actually sent. This is not
cryptographic proof of the remote model or account used.

## Compare and replay

```sh
questlock team benchmark --suite suite.json --local-dir local \
  --baseline-dir baseline > comparison.json

# After a completed case, exact replay needs no generation.
questlock team run --workspace requests --request ticket.json \
  --client-bin /absolute/path/to/codex > local/ticket-repeat.json
```

`benchmark` writes the comparison and exits unsuccessfully when any case is
ineligible. Missing receipt files remain failed rows. Only a complete matched,
checked, known-usage suite can establish the observed hosted-token reduction.
Invoice savings and Codex credit reduction remain unknown. Local reviewers add
work that the single-answer baseline does not perform; report both workflows
and their quality limits. Human acceptance and later rework need separate
[outcome records](../../docs/outcomes.md).
