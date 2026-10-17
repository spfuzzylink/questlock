# Questlock development preferences

- Use `spfuzzylink/` for every new Git branch unless the user requests a
  different exact branch name.
- Default to Go for new libraries, command-line tools, backend services, and
  infrastructure tooling. Keep core decision logic in Go. Respect existing
  project constraints; browser presentation may use HTML, CSS, and JavaScript.
- Make routine implementation and naming decisions autonomously. Use Questlock
  consistently for this repository, library, CLI, and documentation.
- Preserve published releases, working features, and historical artwork.
  Coordinate useful independent subtasks in parallel for substantive shipping
  work, and obtain an independent review of concrete changes before publication.

## Permanent brand

Reuse `assets/questlock-brand.svg` unchanged. Its frozen SHA-256 is
`e8121dd023da8f1d76aa33e633ae03c44fffcc5f7794377e3b2ccb89b145ab29`.
The navy, mint and purple hexagon-lock and its timeless mission copy are the
canonical identity. Do not generate a replacement, introduce a release-themed
variant as the main brand, or update the checksum to permit an unsolicited
redesign. Only an explicit user request authorizes a canonical identity change.
Follow `BRANDING.md` and retain older images as historical material.

## Client configuration changes

- Validate the complete candidate in an isolated home with the native client
  before touching active Codex or Claude settings. Preserve unrelated values and
  command arrays such as `notify`.
- Save a private complete known-good backup. Before the swap, install an
  independent automatic rollback watchdog that does not rely on the active
  client to recover itself.
- Verify the live native client can read the candidate and test every changed
  setting before committing. Recovery must check the installed SHA-256 and
  preserve concurrent human repairs.
- Honor any workstation-specific guard workflow provided by the user. Public
  installers and source must work without private workstation helpers or paths.
- For Claude Desktop, use the native extension installer. Manifest validation,
  JSON parsing, and a standalone MCP protocol test do not establish that Desktop
  imported or connected the extension. Keep native verification pending until
  the app provides connection and tool evidence.
- Keep tokens and complete configuration copies private. Guided onboarding
  must not enable paid escalation or silently fall back to a paid provider.

For LinkedIn launch copy, begin with a problem statement. If copy-and-paste
emphasis is requested, emphasize the whole problem-statement sentence with
Unicode bold italic characters that survive pasting.
