# Your preferences, your policy

These are opt-in examples. Questlock does not load them from a clone or impose
the maintainer's personal settings.

`local-only.request.json` is a real bounded-worker request: installed Ollama
model, explicit token/context limits, a one-hour exact-snapshot reuse window,
checks and outcome tracking. It has no escalation tier. The two USD fields are
explicitly zero; local execution also requires a local provider and does not
grant the process permission to run a paid tier.

Copy it into private storage, choose your installed model, and review the task,
revision, input and checks together. Execute it with `questlock worker run
--request /absolute/private/local-only.request.json --broker YOUR_BROKER_URL`.
Provide the scoped token through `QUESTLOCK_TOKEN_FILE` or the environment, never
in the request. Do not pass `--allow-paid`. Setup and endpoints are covered by
[bounded delegation](../../docs/delegation.md).

A model call is not needed for a literal edit: use `questlock revise` instead.
Exact worker reuse requires the same still-valid task snapshot. A changed fact
needs a new revision; an incomplete or unknown outcome is a reason to inspect
state, not silently retry.

`personal-instructions.example.md` is inert sample text for your own client
preferences. It is not a new settings loader. CPU/memory admission has its own
explicit runtime configuration: `questlock resources config > /absolute/private/admission.json`
emits the complete policy, and `--config PATH` applies it explicitly. See [one local control loop](../../docs/local-coordination.md).

$0 here means no provider token charge on the selected local path. It does not
remove hosted subscription usage or promise local model quality. You supply
hardware, power and acceptance criteria; other users can select other explicit
budgets and providers through the existing APIs.
