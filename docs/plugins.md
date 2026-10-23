# Questlock plugins

Repeating an approved edit or rebuilding the same task result adds work and can
change text that was already correct. Questlock gives Codex, ChatGPT desktop,
and Claude Code a checked revision tool and a bounded local model tool, backed
by the same Go runtime.

![Questlock plugin route: a Git catalog or offline ZIP connects the native local plugin manager to a private worker and broker, then a checked revision and exact reuse with no new generation.](diagrams/plugins.svg)

The [editable draw.io source](diagrams/plugins.drawio) also shows the optional
local model path and the separate Claude Desktop extension route.

## Install and try the first task

Use a current native client with plugin support. For Codex:

```sh
codex plugin marketplace add spfuzzylink/questlock
codex plugin add questlock@questlock
```

For Claude Code 2.1.292 or later:

```sh
claude plugin install questlock --marketplace spfuzzylink/questlock
```

For an older Claude Code client with plugin support, register the catalog first:

```sh
claude plugin marketplace add spfuzzylink/questlock
claude plugin install questlock@questlock
```

For ChatGPT or Codex desktop, open Plugins, choose the Questlock marketplace,
and install or enable Questlock. Local plugins require a supported desktop
executor; this package is not a hosted ChatGPT web connector. Start a new chat
with the plugin enabled and ask:

> Run Questlock's first checked revision, then repeat the same request to show
> exact reuse without calling a model.

The packaged skill calls `questlock_revise` with
`{"request_path":"first-task.json"}` twice. The first task changes synthetic
JSON from `pending` to `ready`; the repeat returns the same immutable artifact.
Check successful completion, passing checks, `durable: true`,
`generation_called: false`, zero this-run generation usage, and `reused: true`
on the repeat. This needs no model weights or provider account.

These commands use the clients' native plugin managers. Questlock supplies no
hooks and does not patch client configuration or change the client's model.
See the official [Codex plugin commands](https://learn.chatgpt.com/docs/developer-commands?surface=cli#codex-plugin)
and [Claude plugin commands](https://code.claude.com/docs/en/plugins/cli-reference).

## What is downloaded

The Git catalog contains manifests, a skill, examples, and a launcher. On its
first `serve` or `try`, the launcher downloads the pinned **v0.11.0** Questlock
archive and `checksums.txt` over HTTPS from this repository's GitHub release.
It verifies the archive's SHA-256 before caching the executable privately.
It does not use a `questlock` executable found on `PATH`. Checksums detect a
mismatched download; they are not an independent publisher signature.

The download needs `curl`, `tar`, and `sha256sum` or `shasum`. No Go installation
is needed to run a release binary. The packaged runtime targets macOS and Linux,
on AMD64 and ARM64. No Windows plugin binary is provided. See the
[installation guide](installation.md) for OS requirements and release limits.

If the client times out during its first runtime download, run the packaged
`sh scripts/run.sh try codex` (or `claude-code`) from that plugin folder, then
reconnect its MCP server or start a fresh chat. This completes the same verified
download outside the client's startup deadline. The offline ZIP avoids that
download entirely.

The [validation record](plugin-validation-v0.11.0.md) separates native install
and connection checks from direct protocol execution and public download tests.

To install without a runtime download, get the matching
`questlock-plugin_0.11.0_OS_ARCH.zip` and `checksums.txt` from
[Releases](https://github.com/spfuzzylink/questlock/releases/tag/v0.11.0).
Compare the ZIP's SHA-256 with its matching checksum entry before extracting;
use `shasum -a 256` on macOS or `sha256sum` on Linux. The ZIP includes both
catalogs, both plugin folders, and the matching binary in each plugin.
From the extracted marketplace root, choose your client. For Codex:

```sh
codex plugin marketplace add .
codex plugin add questlock@questlock
```

For Claude Code:

```sh
claude plugin marketplace add .
claude plugin install questlock@questlock
```

Keep that directory for local catalog updates. A catalog already registered as
`questlock` must be removed through the native client before registering a different
source.

For an offline source qualification, build the checked-out source and copy the
binary into both plugin roots before installing the local catalog:

```sh
go build -trimpath -o bin/questlock ./cmd/questlock
mkdir -p plugins/openai/bin plugins/claude/bin
cp bin/questlock plugins/openai/bin/questlock
cp bin/questlock plugins/claude/bin/questlock
sh plugins/openai/scripts/run.sh try codex
sh plugins/claude/scripts/run.sh try claude-code
```

The initial Go build may download modules; subsequent plugin execution uses the
bundled binary. Build and package checks are separate from connecting a native
chat to the tools.

## Use your own text

Both clients share a private plugin broker and request workspace by default:

| Purpose | Default location |
| --- | --- |
| Broker state and credentials | `$HOME/.local/share/questlock/plugins/local` |
| Reviewed request files | `$HOME/.local/share/questlock/plugins/requests` |

This profile is separate from existing `questlock setup` or manually configured
workers. Credentials are created locally, not included in the plugin. Requests,
results, and broker data stay on the host unless the calling assistant or an
explicitly selected model service sends them elsewhere.

For another deterministic example, copy the packaged
`examples/checked-json.json` into the request workspace and call
`questlock_revise` with its filename. It is a synthetic fixture, including its
historical version string. For your own edit, replace the complete inline
source, recompute its SHA-256, declare literal replacements and match counts,
and review the source and output checks. See the
[revision contract](checked-revisions.md) and
[source examples](../examples/revisions/README.md).

For local generation, adapt the packaged `examples/local-extraction.json`.
It contains a bounded ticket-extraction task and one local Ollama tier; its
model name is an example, not an installed model or an automatic selection.
Choose a suitable model you already operate, review every supplied fact, limit,
reuse TTL, and completion check, save the request privately, then call
`questlock_delegate` with that filename. The plugin does not download weights.
[Local models](local-models.md) and the [delegation contract](delegation.md)
explain the prerequisites. Hosted escalation is disabled in this plugin.

## Diagnose, update, or remove

From the installed plugin folder, the launcher accepts these commands; use
`claude-code` in place of `codex` for the Claude package:

```sh
sh scripts/run.sh doctor codex
sh scripts/run.sh try codex
sh scripts/run.sh stop codex
```

`doctor` is read-only: it neither downloads the runtime nor starts the broker.
Before the first launch, an attention-required result is expected. `try` runs
the synthetic revision and exact repeat through MCP, starting the private
runtime if needed. `stop` stops that profile's managed broker and retains its
data. Because both clients share the default profile, stopping it interrupts
both; close their Questlock sessions first.

The underlying binary also accepts
`questlock plugin serve|try|doctor|stop --client codex|claude-code`, with optional
absolute `--state-dir` and `--workspace` paths. For launcher overrides, use
`QUESTLOCK_PLUGIN_STATE_DIR` and `QUESTLOCK_PLUGIN_WORKSPACE` consistently for
every action. Do not point them at another worker's profile.

If startup fails, inspect the launcher's stderr and `doctor` output. Preserve an
existing request that differs from `first-task.json`; choose a separate private
workspace instead of overwriting it. For checksum or download failures, use a
verified offline ZIP or an approved source build. A failed revision never
silently falls back to a model.

For a Git-backed Codex catalog, refresh it and start a new chat:

```sh
codex plugin marketplace upgrade questlock
```

For Claude Code:

```sh
claude plugin update questlock@questlock
```

A local ZIP catalog uses the files you extracted; replace it with a reviewed new
release before refreshing or reinstalling through the client. Keep the prior
package to return to it through the same native plugin controls. Plugin versions
pin their runtime; updating a catalog does not rewrite old broker results.

To remove the plugin, stop its broker, then use the native manager:

```sh
codex plugin remove questlock@questlock
codex plugin marketplace remove questlock
```

Or for Claude Code:

```sh
claude plugin uninstall questlock@questlock
claude plugin marketplace remove questlock
```

The shared Questlock profile is outside the host's plugin cache and remains
after removal. Keep it for recovery or deliberately remove it after backing up
anything needed. Neither uninstall command is a request to delete task history.

## What the checks establish

Native `plugin list` or manifest validation establishes metadata and installation
state. A successful `try` establishes MCP startup, checked publication, and exact
reuse through the packaged runtime. Confirming both Questlock tools and running
the first task in a fresh native chat establishes that chat's connection. These
are separate checks; the command-line self-test reports native connection as
unverified.

The tools accept only reviewed request files within their workspace. They have
no shell, file-editing, or file-discovery capability. The calling assistant
selects context, prepares requests, and decides how to apply the checked result.
Checks do not establish human acceptance. Failed or uncertain work is withheld;
do not change task identifiers to force a retry.

Checked revisions and exact reuse make no new model call. A local model task
uses local compute. Preparing and reviewing work in Codex, ChatGPT, or Claude
can still consume that client's normal allowance. Installing Questlock does not
intercept every chat, change account credits, or promise a fixed saving.

## Public directory status

The repository catalogs are a distribution route, not an OpenAI or Anthropic
public-directory listing. OpenAI's local MCP submission requires a submitting
organization allowlisted through an OpenAI representative, then portal
validation, required review materials, approval, and publication. Public MCP
review also needs real support, privacy, and terms links and representative
tests and a demo; local checks cannot grant eligibility. See the official
[desktop-only submission requirements](https://developers.openai.com/plugins/deploy/desktop-only)
and [submission metadata](https://developers.openai.com/plugins/deploy/submission).

Anthropic directory submission requires an eligible paid claude.ai account and
portal review. Component support differs across Claude Code, Cowork, and
claude.ai; this package's native stdio workflow targets Claude Code. An
Anthropic official-marketplace listing is a separate partner route. See
[Anthropic's publication guide](https://code.claude.com/docs/en/plugins/publish).
