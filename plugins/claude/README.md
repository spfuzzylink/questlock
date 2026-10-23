# Questlock for Claude Code

Repeating an approved text edit or rebuilding the same result adds work and can
change correct text. Questlock applies checked literal revisions, reuses exact
results, and runs bounded tasks through a configured local model.

Install with a current Claude Code client:

```sh
claude plugin marketplace add spfuzzylink/questlock
claude plugin install questlock@questlock
```

Start a fresh session and run `/questlock:questlock`, or ask:

> Run Questlock's first checked revision, then repeat it to verify exact reuse
> without calling a model.

The skill calls `questlock_revise` with
`{"request_path":"first-task.json"}` twice. Expect checked synthetic JSON,
durable publication, `generation_called: false`, and `reused: true` on the
repeat. No model or provider account is needed for this task.

The Git package downloads the pinned v0.11.0 runtime archive and checksum over
HTTPS from the official Questlock GitHub release on first `serve` or `try`.
It verifies SHA-256 and does not use a binary from `PATH`. The platform
`questlock-plugin_0.11.0_OS_ARCH.zip` includes the runtime for an offline install;
verify its matching `checksums.txt` entry, extract it, and register that
marketplace root with `claude plugin marketplace add .` instead. macOS and Linux
AMD64/ARM64 are packaged; no Windows binary is provided. This native stdio
workflow targets Claude Code. It is separate from the Claude Desktop `.mcpb`
extension route.

From this plugin folder:

```sh
sh scripts/run.sh doctor claude-code
sh scripts/run.sh try claude-code
sh scripts/run.sh stop claude-code
```

`doctor` reads state without downloading or starting anything. `try` runs the
first task and exact repeat through MCP. `stop` stops the shared plugin broker
and keeps data. A native plugin-list entry, this protocol self-test, and a
successful tool call from a native session establish different things; the
self-test does not claim a native chat connection.

Private defaults are `$HOME/.local/share/questlock/plugins/local` for broker
state and `$HOME/.local/share/questlock/plugins/requests` for request files.
Claude Code and Codex share these plugin paths; existing manual setup uses a
separate profile. Credentials are generated on the host. Optional
`QUESTLOCK_PLUGIN_STATE_DIR` and `QUESTLOCK_PLUGIN_WORKSPACE` overrides must be
absolute and used consistently for all launcher actions.

After the first task, copy `examples/checked-json.json` into the request
workspace and call `questlock_revise` with that filename. For your own edit,
replace the source, SHA-256, literal replacements, counts, and checks together.
`examples/local-extraction.json` shows a bounded local-model request; choose an
already installed model, review its complete input and limits, and call
`questlock_delegate`. The example model name does not install or select weights.
Keep request files private. Neither tool discovers files or edits your project.

Update with `claude plugin update questlock@questlock`, then start a new session
or run `/reload-plugins`. For a local ZIP catalog, replace its files with a
reviewed new release before refreshing through the client. On failure, preserve
requests and inspect stderr and `doctor`; use a verified offline package if
downloading fails. A changed `first-task.json` is preserved, not overwritten.

Close Questlock sessions, stop the broker, then remove it with:

```sh
claude plugin uninstall questlock@questlock
claude plugin marketplace remove questlock
```

The separate shared Questlock data remains for recovery. Stopping the default
broker affects both clients. Paid fallback is disabled. Failed checks never
trigger an automatic model rewrite. Checks are not human acceptance, and the
surrounding hosted assistant can still consume normal account allowances.

[Full guide, source builds, and public-directory prerequisites](https://github.com/spfuzzylink/questlock/blob/v0.11.0/docs/plugins.md)
· [Revision contract](https://github.com/spfuzzylink/questlock/blob/v0.11.0/docs/checked-revisions.md)
· [Local models](https://github.com/spfuzzylink/questlock/blob/v0.11.0/docs/local-models.md)
· [Official Claude plugin commands](https://code.claude.com/docs/en/plugins/cli-reference)
