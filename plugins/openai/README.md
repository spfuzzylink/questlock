# Questlock for Codex and ChatGPT desktop

Repeating an approved text edit or rebuilding the same result adds work and can
change correct text. Questlock applies checked literal revisions, reuses exact
results, and runs bounded tasks through a configured local model.

Install with a current Codex client:

```sh
codex plugin marketplace add spfuzzylink/questlock
codex plugin add questlock@questlock
```

In ChatGPT or Codex desktop, select the Questlock source in Plugins and enable
Questlock. Start a fresh chat and ask:

> Call Questlock's status tool to check this worker's root and capabilities.
> Then run its first checked revision and repeat it to verify exact reuse
> without calling a model.

The skill calls `questlock_status` with `{}` first. Check the connected
`runtime_version`, metadata `schema`, `request_root`, `profile`,
`policy.allow_paid`, `request_schemas` and `checks.kinds`. Status makes no
network/model call, reads no task or client settings, and changes nothing.
Its opened root and ready/busy state describe the worker; broker/providers
remain `not_checked`.

The skill then calls `questlock_revise` on `first-task.json` beneath that root
twice. Expect checked synthetic JSON, durable publication,
`generation_called: false`, and `reused: true` on the repeat. No model or
provider account is needed for this task. An older worker without status can
keep its existing task tools; update/restart it deliberately when needed.

The Git package downloads the pinned v0.16.0 runtime archive and checksum over
HTTPS from the official Questlock GitHub release on first `serve` or `try`.
It verifies SHA-256 and does not use a binary from `PATH`. The platform
`questlock-plugin_0.16.0_OS_ARCH.zip` includes the runtime for an offline install;
verify its matching `checksums.txt` entry, extract it, and register that
marketplace root with `codex plugin marketplace add .` instead. macOS and Linux
AMD64/ARM64 are packaged; no Windows binary is provided. This is a desktop local
MCP package, not a hosted ChatGPT web connector.

From this plugin folder:

```sh
sh scripts/run.sh doctor codex
sh scripts/run.sh try codex
sh scripts/run.sh stop codex
```

`doctor` reads state without downloading or starting anything. `try` runs the
first task and exact repeat through MCP. `stop` stops the shared plugin broker
and keeps data. A native plugin-list entry, this protocol self-test, and a
successful tool call from a native chat establish different things; the
self-test does not claim a native chat connection.

Private defaults are `$HOME/.local/share/questlock/plugins/local` for broker
state and `$HOME/.local/share/questlock/plugins/requests` for request files.
Codex and Claude Code share these plugin paths; existing manual setup uses a
separate profile. Credentials are generated on the host. Optional
`QUESTLOCK_PLUGIN_STATE_DIR` and `QUESTLOCK_PLUGIN_WORKSPACE` overrides must be
absolute and used consistently for all launcher actions.

For your own text, use the root returned by `questlock_status`, including any
operator override, rather than guessing from these defaults. After the first
task, copy `examples/checked-json.json` into that root and call
`questlock_revise` with its saved absolute path. For your own edit,
replace the source, SHA-256, literal replacements, counts, and checks together.
`examples/local-extraction.json` shows a bounded local-model request; choose an
already installed model, review its complete input and limits, and call
`questlock_delegate`. The example model name does not install or select weights.
Keep request files private. The task tools read only the approved request;
they provide no file listing or project edits. Status is a read-only contract
lookup, not permission to change a working custom client configuration.

For a Git catalog update, run `codex plugin marketplace upgrade questlock` and
start a new chat. For a local ZIP catalog, replace its files with a reviewed new
release before refreshing through the client. On failure, preserve requests and
inspect stderr and `doctor`; use a verified offline package if downloading fails.
A changed `first-task.json` is preserved, not overwritten.

Close Questlock sessions, stop the broker, then remove it with:

```sh
codex plugin remove questlock@questlock
codex plugin marketplace remove questlock
```

The separate shared Questlock data remains for recovery. Stopping the default
broker affects both clients. Paid fallback is disabled. Failed checks never
trigger an automatic model rewrite. Checks are not human acceptance, and the
surrounding hosted assistant can still consume normal account allowances.

[Full guide, source builds, and public-directory prerequisites](https://github.com/spfuzzylink/questlock/blob/v0.16.0/docs/plugins.md)
· [Revision contract](https://github.com/spfuzzylink/questlock/blob/v0.16.0/docs/checked-revisions.md)
· [Local models](https://github.com/spfuzzylink/questlock/blob/v0.16.0/docs/local-models.md)
· [Official Codex commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli#codex-plugin)
