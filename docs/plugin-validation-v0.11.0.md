# Plugin validation — v0.11.0

The first task is deliberately small: a checked JSON edit from `pending` to
`ready`, then the exact same request again. We want a working connection and a
reusable durable result before asking a new user for model weights or keys.

## What ran on 2026-10-22

| Check | Observed result |
| --- | --- |
| Codex CLI `0.163.0-alpha.10.2` | Empty-profile marketplace add, plugin add/list, loaded skill, both real MCP tools discovered through the native app server. |
| Claude Code `2.1.308` | Strict package/catalog validation, empty-profile install/list, skill and MCP component inventory, native MCP health reported connected. |
| Both packaged launchers | MCP initialize and tool discovery, checked revision, then exact result reuse; source/output checks and artifact SHA-256 verified. |
| Provider work | Zero model calls, zero this-run generation usage, $0 provider API charge, paid fallback disabled. |
| Existing client settings | Unchanged. Profiles imported no credentials, project instructions or client configuration. |

Native client checks ran on macOS ARM64. Claude's pinned executable was fetched
from its official release manifest and matched its SHA-256; strict code-signature
verification identified Anthropic PBC. No model turn, API key or sign-in was used.
The [machine-readable record](plugin-validation-v0.11.0.json) contains the redacted
observations. The verifier exposes no inference RPC: native loader/connection
checks and direct MCP task execution remain separate evidence.

## Published Git installs

The public release is now tested too. Fresh profiles installed from both the
`v0.11.0` Git tag and default `main`, with no bundled runtime or imported keys.
Both clients downloaded the pinned runtime over HTTPS, checked its checksum,
and connected without a startup override or manual warmup.

| Default Git install | One observed cold MCP connection |
| --- | --- |
| Codex: marketplace add, then plugin add | 1.183 seconds; skill and both tools discovered. |
| Claude Code: `claude plugin install questlock --marketplace spfuzzylink/questlock` | 1.378 seconds; native health reported connected. |

These are single runs on this Mac and connection, not a latency guarantee.
Each downloaded binary matched the reviewed runtime. Both direct MCP workflows
then passed the checked revision and exact reuse with zero model calls and
$0 provider API charge. Claude's one-command syntax needs 2.1.292 or later;
the native qualification used 2.1.308.

All 11 [release assets](https://github.com/spfuzzylink/questlock/releases/tag/v0.11.0)
were downloaded anonymously and matched their published checksums. All four
BUILDINFO records pin the clean release commit. The
[tag CI](https://github.com/spfuzzylink/questlock/actions/runs/39891419861)
and [container publication](https://github.com/spfuzzylink/questlock/actions/runs/39891872036)
passed. Both native container architectures were published; the prior v0.10.0
container digest stayed unchanged.

## Regression coverage

The platform ZIP has an explicit public-file allowlist, normalized metadata,
legal notices and both matching native runtimes. ZIP extraction rejects path
escapes, duplicate entries, links and excessive sizes. Offline bootstrap tests
mock downloads and exercise the same release checksum/member verification,
wrong versions, symlink paths, concurrent startup and failure cleanup.

The first-task file is synced before atomic non-replacing publication. A
64-writer cold-start race passes without exposing an incomplete request. A
changed operator fixture is preserved and doctor reports attention required.
Doctor neither starts a service nor downloads a runtime. Existing onboarding,
broker, inference and configuration-recovery checks remain in `make check`.

From a source checkout, verify a native platform ZIP:

```sh
python3 scripts/verify_plugins.py --archive /absolute/path/to/questlock-plugin_0.11.0_OS_ARCH.zip
```

From a checkout, pass explicit native executables with `--codex-bin` and
`--claude-bin` to add the native client checks. The verifier creates private
homes, copies only public package inputs, and stops its own broker processes.
It never changes the active clients or downloads a client/model implicitly.

## What this does not establish

A native chat model turn was not run. The ChatGPT desktop Plugins UI and a new
Claude Desktop MCPB import were not exercised by this CLI qualification.
The existing Desktop extension route stays separate. Public-directory approval
is also separate from Git marketplace distribution; see the
[submission prerequisites](plugins.md#public-directory-status).

The initial native tests used bundled runtimes; the public Git runs above
separately exercised cold downloads. Other networks can still exceed a client's
startup deadline. If that happens, use the documented warmup and reconnect route
or the offline ZIP.

These synthetic checked edits establish no general task-quality percentage,
GPU speedup, token counterfactual or subscription-credit saving. The surrounding
hosted development chat and review consume their normal allowance. Questlock's
two release-version stamps, a portable attribution-link edit and three install
documentation edits used its checked deterministic revision path with zero model
calls and API charge; no
matched hosted baseline was measured.
