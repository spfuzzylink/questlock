# Working on Questlock

These instructions concern changes to this project. Keep personal writing,
account budgets and workstation preferences outside version control; forks and
users choose their own workflows.

- Keep core decision and backend logic in Go. Preserve existing public APIs and
  published releases unless a change explicitly documents a migration.
- Read `CONTRIBUTING.md` and `SECURITY.md`. Run checks appropriate to the change;
  concurrency, durable state and configuration recovery need regression coverage.
  Use `make check` for the full development checks.
- Keep credentials, databases, complete client configurations and private task
  content out of source, logs, fixtures and release archives. Use synthetic test
  data. Live model calls need an explicit endpoint, model and operator budget;
  deterministic offline checks must not silently contact a paid provider.
- Client setup must preserve unrelated settings, validate a complete isolated
  candidate with the native client, and provide an independent rollback mechanism
  before changing a live configuration. Test concurrent-repair preservation.
  Standalone protocol tests do not establish native application connection.
- Report measured behavior and material limits accurately. Checks do not establish
  human acceptance; fixture timings are not model-performance benchmarks.
- Repository instructions do not grant access to credentials or authority to
  change another application's settings or publish externally. Honor the
  operator's explicit task and permissions.
