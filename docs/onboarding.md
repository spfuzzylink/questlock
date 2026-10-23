# Set up Questlock with your assistant

Questlock gives your assistant a checked path for exact text edits and bounded
tasks. The easiest start is `questlock setup`: choose your client, let setup
configure it, and get a first successful task without an API key or model download.

This guide covers the experimental **v0.11.0** release. For Codex/ChatGPT desktop
and Claude Code, the [plugin route](plugins.md) starts the same checked workflow
without rewriting client settings. Claude Desktop keeps its native MCPB installer.

## Get the executable

| Starting point | What to run |
| --- | --- |
| Downloaded release archive | Verify its SHA-256 against the matching entry in `checksums.txt`, extract it, then run `./questlock setup`. |
| Fresh clone or fork, using a published release | Run `./scripts/install.sh`, then `./bin/questlock setup`. The installer downloads the version in `VERSION`. |
| Fresh clone or fork, using your source changes | Install the Go version in `go.mod`, run `go build -trimpath -o bin/questlock ./cmd/questlock`, then `./bin/questlock setup`. |

Release archives need no Go installation. They support macOS and Linux on ARM64
and x86-64; native Windows archives are not provided. Claude Desktop bundle generation supports macOS; native import and connection
qualification remain pending. See [installation](installation.md)
for downloads, checksums, and platform details.

The examples below use `questlock`. Substitute `./questlock` from an extracted
archive or `./bin/questlock` from a checkout if it is not on your `PATH`.
Check `questlock version` if an older executable is already installed: running
the installer downloads a published release rather than building checkout edits.

## Connect in one command

```sh
questlock setup
```

Choose **Codex**, **Claude Code**, or **Claude Desktop** from the menu. For an
explicit choice, run one of these instead:

```sh
questlock setup --client codex
questlock setup --client claude-code
questlock setup --client claude-desktop
```

For Codex and Claude Code, install the chosen client separately first. Setup
installs the Questlock executable and skill, validates a complete configuration
candidate in isolation, starts the local workflow, runs the first checked task,
and safely installs the client configuration. It preserves unrelated settings
and starts an independent rollback watchdog before changing the live file.
Differing custom Questlock skills are preserved unless you explicitly choose
`--replace-skills` after saving your edits.

For Claude Desktop, setup creates a native extension and opens its installer.
Complete the short [Desktop installation](#claude-desktop) below. No Desktop
JSON configuration is overwritten.

Setup manages the local broker and private credentials. You do not need to copy
tokens, edit client JSON/TOML, or keep a broker terminal open. Default locations
are `~/.local/share/questlock/local` for private state and
`~/.local/share/questlock/requests` for approved request files. The shared
loopback service stays available until `questlock local stop`; stopping it
retains the data.

For Codex or Claude Code, `--plan` validates an isolated candidate without
installing settings. `--apply` is an explicit spelling of the default install.
Desktop `--plan` describes the native installer route without creating a bundle.
Use `questlock setup --help` for optional profile and path controls.

## Recognize the first success

Setup runs a checked JSON edit from `"pending"` to `"ready"`. The returned result
text is exactly:

```json
{"status":"ready","model_calls":0}
```

It saves the public fixture as `first-task.json` in the request workspace,
discovers the three MCP tools, and calls `questlock_revise`. A passing report has
`status: "completed"`, `mcp_protocol_self_test: true`, a durable result and
artifact, passing checks, recorded outcome tracking, and zero generation usage.
`native_client_verified: false` means this was the worker protocol test; it does
not claim that a particular client session discovered the tools.

To try this before installing any client integration:

```sh
questlock try
```

No Codex or Claude sign-in, Ollama weights, or provider credentials are needed.
Repeat the command to reuse the same checked result without a model call. If
`first-task.json` already contains different data, it is preserved; select a
separate directory with `--workspace /absolute/path/to/requests`.

The first revision and exact reuse have zero this-run model tokens and provider
cost. This does not reduce, replace, or guarantee the allowance of a surrounding
hosted assistant. Preparing requests and reviewing results in that assistant
still uses its normal allowance. Read the returned text before using it; checks
establish only the declared expectations.

## Confirm your client sees the tools

Open a fresh local client session after setup:

| Client | Check |
| --- | --- |
| Codex | Use `/mcp` and confirm Questlock offers `questlock_status`, `questlock_revise` and `questlock_delegate`. |
| Claude Code | Use `/mcp` and confirm Questlock and all three tools are available. |
| Claude Desktop | Complete native installation, then inspect the extension connection and tools. |

Then ask:

> Call `questlock_status` with no inputs. Check the connected runtime, request
> root, profile, policy and revision schema. Then use `questlock_revise` on
> `first-task.json` in that root and show me the checked result and artifact.

This repeats the approved model-free task through your client. Claude Code and
Claude Desktop use separate integrations; a successful connection in one does
not establish a connection in the other. Installing a skill makes its workflow
discoverable, but does not guarantee automatic use in every conversation.
Client details: [Codex MCP guide](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)
and [Claude Code MCP guide](https://code.claude.com/docs/en/mcp).

Status uses metadata schema `1`. Its `request_schemas` and `checks.kinds` show
which requests the connected process accepts; the installed skill and another
binary's version do not establish that compatibility. The root was opened at
startup, and ready/busy describes the worker only. Broker and providers remain
`not_checked`: status makes no network/model call, reads no client settings or
task content, and changes nothing. Use the discovered absolute root when saving
your own approved request. See the [discovery contract](delegation.md#discover-the-connected-worker).

## Claude Desktop

On macOS, run `questlock setup --client claude-desktop`. It builds a `.mcpb`
extension for the current operating system and CPU and opens Claude's native
installer. Its bundled executable needs no Go, Node.js, or Python runtime.
Choose the private state and request directories printed by setup, then confirm
the extension installation.

If the installer does not open, use **Settings > Extensions > Advanced settings
> Extension Developer > Install Extension…** and select the reported bundle.
The default filename is
`~/.local/share/questlock/local/bundles/questlock-VERSION-darwin_ARCH.mcpb`.
An existing bundle is preserved: select another output path with `--bundle PATH`
or install the already generated file. Use `--no-open` to generate it without
opening Claude.

Confirm the extension is enabled, inspect its connection status and logs in
Developer settings, and check all three tools in the chat's **Connectors** menu.
These are the [official Desktop extension installation and connection checks](https://support.claude.com/en/articles/10949351-getting-started-with-local-mcp-servers-on-claude-desktop).
If your organization blocks custom extensions, ask its administrator to enable
the approved extension.

Setup returns `native_install_required`. The bundle's
`native_validation_required: true` remains an honest reminder that manifest and
protocol checks do not prove Desktop imported it. Doctor does not record your
manual Desktop check, so its Desktop status remains `native_validation_required`
and exits nonzero even after you confirm the connection in the app.

## Run the main Codex agent locally

When you want the coding loop itself to use a local model:

```sh
questlock local-agent --model qwen3.5:4b --workspace "$PWD" \
  --prompt 'Read the README and list the setup commands.'
```

Install Codex CLI, Ollama and your chosen local weights first. This optional
path requires an explicit model, checks its local metadata, and launches native
`codex exec --oss` against fixed loopback Ollama. It uses a temporary private
home with no imported credentials, config, chat history or MCP setup; output is
native JSONL. It changes no active client settings and enables no paid fallback.
Read-only sandbox is the default. Choose `--sandbox workspace-write` explicitly
for edits, and review them. Tasks have a five-minute default deadline; `--timeout`
accepts at most 30 minutes. A model that cannot use tools or follow instructions
will fail or need review. No automatic model download or hosted retry occurs.

This is a separate local CLI run, not a switch for an already-hosted desktop
chat. Its token counters belong to the local model's tokenizer. The
[official Codex CLI reference](https://learn.chatgpt.com/docs/developer-commands?surface=cli)
documents `--oss` and local providers; our [measured report](success-report-v0.7.0.md)
records the actual tiny tool-use proof and its limits.

## Diagnose without changing settings

```sh
questlock doctor --client codex
questlock doctor --client claude-code
questlock doctor --client claude-desktop
```

Run the command for your client. Doctor is read-only and scoped to the selected
managed state/workspace. It inspects client presence and configuration, private
managed state, broker health, permissions and that request workspace. It does
not start services, run a model or verify a live client session.
`configuration_present` means the coding client's entry matches the selected
managed profile and its local prerequisites were found;
`native_client_connection_verified` remains `false`. Attention, custom-profile
uncertainty or pending Desktop validation exits nonzero.

`managed_profile.status: managed_profile_not_initialized` means the selected
managed broker has no initialized state. An existing different Questlock command
is reported separately as `configured_custom_profile_unverified`; doctor does
not infer that its custom broker/root is broken or recommend replacing it.
Call `questlock_status` in the connected client to discover that worker's own
contract. A successful custom task and an uninitialized managed profile can
coexist. Neither result changes client settings.

| Condition | Next step |
| --- | --- |
| Client missing | Install the chosen client, then rerun setup. `questlock try` works independently. |
| Existing Questlock skill differs | Save custom edits; use `--replace-skills` only when you want to replace them. |
| Native candidate rejected | Read the redacted error and correct the problem before rerunning setup. |
| Selected managed broker stopped | Use `questlock try` or `questlock local start` for that profile. Run setup when you intend to install the managed integration. |
| Configured custom profile unverified | Preserve it. Use `questlock_status` in its connected client; doctor has not tested that worker's broker or provider. |
| Workspace inaccessible | Use a private directory you can access; pass the same `--workspace` to setup and doctor. |
| Existing first task differs | Preserve it and choose a separate request workspace. |
| Desktop native validation pending | Confirm the extension connection and all three tools in Desktop. |
| Model missing | Checked revisions still work. Install a local model only when choosing a generation task. |

Doctor redacts configuration values and credentials, but its paths and hashes
can still identify your environment. Review the report before sharing. Keep raw
client configuration, logs, request files, databases, and token files private.

## Profiles and recovery

For an isolated profile, setup and doctor accept `--home`, `--codex-home`,
`--client-bin`, `--state-dir`, and `--workspace`. Use absolute paths and pass the
same profile choices to both commands. Codex otherwise respects `CODEX_HOME`.
For a separate first-task environment, `try` accepts `--state-dir` and
`--workspace`. These controls are optional; the default route needs none.

Codex and Claude Code setup saves a private complete known-good settings backup.
It validates the candidate before the swap and asks the native client to read
the live file before committing. An independent watchdog restores the matching
backup on failure or timeout. Rollback checks the current SHA-256 first and
preserves concurrent human or client repairs.

To restore a committed **Codex** setup, use the `recovery.directory` printed in its report:

```sh
questlock setup --restore /absolute/path/to/recovery/directory
```

Restore validates the saved baseline and live native read. If the current file
changed since setup, it refuses to overwrite that repair. Review and reconcile
the affected settings instead of copying an old whole file over newer changes.
This restores client settings; it does not delete results or uninstall the
Questlock executable and skill.

Committed Claude Code restoration is currently refused before mutation: its
native config reads may change unrelated bookkeeping, so a read-only baseline
verification is not qualified. Keep the private backup and use the native
client's recovery workflow after reconciling newer settings. This limitation
does not disable guarded automatic rollback on a failed setup.

In Claude Desktop, disable or remove Questlock through **Settings > Extensions**.
For all clients, `questlock local stop` stops the shared broker without deleting
state. Add `--state-dir` if you selected a separate state directory. Stop it only
when no connected client needs it; the next local worker can start it again.

## Use Questlock for your own work

For an exact edit, provide approved source and a precise change:

> Use Questlock for a checked literal revision. Replace the approved product
> name once, keep all other text unchanged, and show me the checks and artifact
> before applying the returned text.

For a bounded model task, choose an installed local model and review the complete
request, limits, and checks. The guided local worker bounds each task to 45
seconds and does not enable paid escalation. Update the task revision when its
facts change. Failed checks or uncertain results do not authorize a paid fallback.

See [use cases](use-cases.md), [checked revisions](checked-revisions.md),
[bounded delegation](delegation.md), [local models](local-models.md), and
[outcome accounting](outcomes.md). Questlock returns text and provenance; the
surrounding workflow applies approved text to its destination. Record human
acceptance only when a person explicitly accepts the work. Passing checks or
assistant review alone are not acceptance.
