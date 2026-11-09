# One busy worker. One ready worker. One generation.

Run from the root of a full source checkout (the binary archive alone is not a
standalone Go source checkout):

```sh
go run ./examples/capacity-dispatch
```

No server setup, provider key or model download. This example starts a temporary
HTTPS broker, uses two scoped clients and three independent scripted provider
objects. It makes **zero real model calls**.

The first node reports critical memory pressure. It publishes no inference
claim and generates nothing. The ready node handles the unchanged task once.
Another client then asks for the exact snapshot under critical pressure and
reads the completed answer with zero new input or output tokens.

Output distinguishes `scripted_generations: 1` from `real_model_calls: 0`.
It also exposes the selected node, per-node counters and exact-reuse result.
Temporary state is removed after a successful demonstration; failures retain it
for inspection. This is a behavior check on one physical host, not an inference
speed or answer-quality benchmark.

For real workers, replace the scripted providers with your explicitly configured
self-hosted runtime and measured memory policy. Give each machine its own token
in one broker scope; keep SQLite on the broker host. Read
[waiting, routing and quality choices](../../docs/capacity-routing.md) and the
[existing master/reviewer deployment](../../docs/local-agent-teams.md#share-work-across-nodes).
