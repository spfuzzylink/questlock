# Explicit approved questions

This runnable example measures a trusted application's exact question forms. The
application declares that three fixed strings request the same answer; Questlock
maps them to one canonical request before normal execution. It performs no
semantic search, embedding or model verification. Unknown wording stays unchanged.
These application-authored forms are explicit contracts, not an independent test
of natural-language equivalence or general model accuracy.

`cases.json` contains six synthetic read-only records: support, onboarding,
incident, release, refund and broker status. Each requests 3–5 primitive JSON
fields, with `json_valid` and whole-object `json_equals` checks for facts, roles,
conditions or arithmetic. Expected checks are not inserted into the prompt.
Every case retains full instructions and evidence. `policy.json` is a separate
small policy example; this driver uses the six embedded case policies.

From the repository root, this prints usage before reading any file or contacting
a provider:

```sh
go run ./examples/approved-questions
```

For live measurement, prepare an **existing private directory**, a token for a
**fresh dedicated broker scope**, a running literal-loopback broker, and an
already-installed `qwen3.5:4b` in Ollama. The example creates no scope, starts no
service, downloads no model and has no paid fallback. Keep the token, state and
report outside the repository. The token must be a private regular file; its
parent and both output parents must be private directories (for example mode
0700). State and report must be new absolute paths and are created mode 0600
with exclusive creation. Existing paths are never overwritten.

```sh
go run ./examples/approved-questions --live --fresh-baseline \
  --broker http://127.0.0.1:8080 \
  --ollama http://127.0.0.1:11434 \
  --token-file /absolute/private/run/scoped.token \
  --state /absolute/private/run/progress.jsonl \
  --output /absolute/private/run/report.jsonl \
  --max-total-tokens 152064
```

The optional `--fresh-baseline` runs twelve separate fresh requests using the two
approved alternate forms per case, unchanged evidence/checks/provider/limits and
fixed separate task IDs. Without that flag, those calls are absent. The daily
scope caps are 49152 input, 1536 output and 50688 total tokens without baselines;
with baselines they are 147456 input, 4608 output and 152064 total. The total flag
can only lower its selected cap. Existing scope quota settings must match. Every
source/baseline is capped at 8192 input tokens, 256 output tokens, 16KiB selected
context and a 60-second generation timeout. Ollama uses JSON primitive output
contracts, temperature zero, thinking disabled and `keep_alive=0`.

For each case, the driver prepares and runs the canonical source through ordinary
`Router.Run`, then runs its two approved alternate forms only if the source was
fresh, complete, durable and passed every check. A known source quality failure
retains its output, checks, usage and receipt and withholds both replays; optional
fresh baselines still run. Any incomplete/unknown call stops the whole run. There
is no automatic retry, revision randomization, resume, claim clearing or budget
release. Preserve partial files and inspect broker receipts before deliberate
operator recovery. Re-running with new file paths does not change task identity.

The JSONL report labels `seed`, `replay` and `baseline` separately and retains the
real generation measurements and exact fingerprints. Summary tokens count this
run's known generation only, including failed sources/baselines. A successful
replay must have the source's exact fingerprint, no generation call and zero new
usage. Historical avoided usage remains a proxy in ordinary results; it is not
added to measured work. With a complete, quality-passing fresh baseline, compare
replay tokens with baseline tokens for incremental cost, and seed-plus-replay
tokens with baseline tokens for cost including the seed. Failed or partial runs
must remain visible. No price, invoice saving or human acceptance is inferred.

Offline tests use synthetic providers and a private SQLite broker fixture:

```sh
go test -race ./examples/approved-questions
```
