# Durable edge coordination fixture

Downloaded release binaries can run:

```sh
questlock edge quest
```

No Go installation, model, active client or remote service is required. The
command uses real loopback HTTP and temporary private SQLite files, then cleans
up. Its JSON lists three simple and three complex passing checks: durable queue
before network, authenticated receipt-agent pin, current pull state, lost
response with restart/exact retry, concurrent stale writers, and snapshot
pagination with restart and catch-up. It does not validate two actual hosts.

From a full source checkout, `go run ./examples/edge` invokes the same fixture.
See [SecondGate](../../docs/second-gate.md) for the reusable `edge` SDK, hard
storage bounds, explicit uncertainty/conflict decisions and HTTPS requirements.
