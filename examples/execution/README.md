# Put your app behind one execution coordinator

Same ticket, same facts, same model? Reuse the completed handoff. Different
facts? Give it a new revision. If the machine is busy, pause new work before
calling the model.

This example uses your running broker and an installed Ollama model, or a pinned
standalone SGLang server. It never pulls weights, changes a daemon or falls back
to a paid provider. You choose the runtime up front. Ollama can run on CPU when
supported by its model and runtime; Questlock does not choose the device.

From a source checkout, start a private broker in one terminal:

```sh
make build
mkdir -p .questlock
chmod 700 .questlock
(umask 077; set -C; ./bin/questlock token create --agent execution-demo --scope execution-demo > .questlock/execution-demo.token)
./bin/questlock serve
```

In another terminal, copy the local task and change `YOUR_INSTALLED_MODEL` to
weights you already installed. Keep the task's facts and caps in that one file.

```sh
cp examples/execution/local-task.json .questlock/task.json
# Edit .questlock/task.json: set your installed model and approved task facts.
go run ./examples/execution \
  -token-file .questlock/execution-demo.token \
  -spec .questlock/task.json \
  -admission examples/execution/admission.json
```

`-spec` uses the strict `execution.LoadSpec` API. `-admission` uses the existing
complete resource-policy format. Endpoint and credentials stay separate. There
is no flag that silently changes the approved model, prompt or budget. Omit
`-admission` to use the same defaults.

The first successful run generates a handoff. The second invocation inside the
example reuses it and reports `new_input=0 new_output=0`. Running the command
again while the same snapshot is valid can reuse both invocations. Change the
file's `revision` when the facts change; that allows a fresh generation.
Complete and durable describe stored protocol state. Check the answer before
using it with a customer.

For a running **SGLang v0.5.20** dense server:

```sh
cp examples/execution/sglang-task.json .questlock/sglang-task.json
# Edit its served model, deployment revision and approved task facts.
go run ./examples/execution \
  -token-file .questlock/execution-demo.token \
  -spec .questlock/sglang-task.json \
  -endpoint http://127.0.0.1:30000
```

Set the file's `sglang.profile` to `hicache` only when that server is configured
for HiCache. The
adapter checks the native runtime contract. A generic SGLang router or PD
ingress needs separate qualification; it is not a drop-in endpoint for this
example. [Runtime setup and GPU validation](../../deploy/sglang/README.md)
cover that boundary.

To use a broker on another machine, add
`-broker https://broker.example.test` and, for a private CA, `-ca-file broker-ca.pem`.
Give each trusted worker its own token in the same broker scope. Keep the broker
database on its own host. Every process keeps its own local resource budget;
the broker shares task claims and completed results, not a cluster-wide GPU
allocation. The optional `daily_token_budget` is a shared scope/UTC-day usage
quota; CPU/memory admission remains local. All participating workers need the
same caps and v0.10.0+ clients, configured before any work is admitted. Use a
dedicated scope; earlier unbudgeted runs are not covered by a new quota.

The [hosted budget example](hosted-budget.example.json) also shows per-call and
shared daily USD limits. Its model, route and prices are illustrative. Replace
them with an approved route and current prices before any hosted use. This
local-only executable rejects that hosted spec. Nothing here makes a paid call
just because a config file exists.

Optional Langfuse export reads `LANGFUSE_PUBLIC_KEY` and `LANGFUSE_SECRET_KEY`
from your process environment. Add
`-langfuse http://127.0.0.1:3001/api/public/otel/v1/traces` for a local instance,
or your own HTTPS traces URL. Remote SGLang also needs `SGLANG_API_KEY`.
Keys stay out of task specs and output. A telemetry failure prints a separate
notice and keeps the completed result; it does not run the model again.

For model-free HTTPS, reuse, pressure and telemetry-failure checks, run:

```sh
go test ./execution -run 'TestHTTPS|TestAdmissionAndValidation|TestLangfuseFailure|TestUnknownGeneration'
```

Those tests use scripted providers on one machine. They do not benchmark model
quality, two physical hosts or GPU throughput. The [Go integration guide](../../docs/go-integration.md)
shows how to wire the same interfaces into your application.
