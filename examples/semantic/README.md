# Observe related questions without serving a semantic answer

These requests describe a fictional support policy. **Questlock** still generates
and checks every fresh answer. Shadow mode records possible prior matches after
that work; it serves no candidate and avoids no generation. Existing exact reuse
continues to skip generation and embeddings.

| File | What changes | Expected boundary |
| --- | --- | --- |
| [support-faq.request.json](support-faq.request.json) | First approved question | Fresh checked generation, then query and index |
| [support-faq.paraphrase.request.json](support-faq.paraphrase.request.json) | Question wording only | Fresh checked generation; prior source may appear as a candidate |
| [support-faq.changed-evidence.request.json](support-faq.changed-evidence.request.json) | Policy changes from 24 to 48 hours | Different bound partition; earlier policy is ineligible |
| Exact repeat of the first file | Nothing | Existing exact reuse; `skipped_exact`, no embedding call |

A paraphrase is a proposed positive pair, not a promised similarity score. The
installed embedding model must be qualified on the operator's own task set.
The strict JSON and fact checks constrain these synthetic answers; passing them
does not establish human acceptance.

For an offline check with simulated embeddings and timings:

```sh
questlock semantic quest
```

To run the JSON files, first prepare a private scoped broker and its token using
the [worker setup](../../docs/delegation.md#run-or-expose-the-worker). Install Ollama and
the generation and embedding weights explicitly. The core binary contains no
weights; exact-only tasks need none. Init explicitly asks the local daemon for
embedding weights and may download them. Review its options before running it:

```sh
questlock semantic init --help
questlock semantic init --model all-minilm \
  --ollama-url http://127.0.0.1:11434
```

With `questlock` on `PATH`, run from the checkout:

```sh
questlock worker run --request examples/semantic/support-faq.request.json \
  --broker http://127.0.0.1:8080 --ollama-url http://127.0.0.1:11434
questlock worker run --request examples/semantic/support-faq.paraphrase.request.json \
  --broker http://127.0.0.1:8080 --ollama-url http://127.0.0.1:11434
questlock worker run --request examples/semantic/support-faq.changed-evidence.request.json \
  --broker http://127.0.0.1:8080 --ollama-url http://127.0.0.1:11434
```

These commands perform local inference for fresh tasks. The sample tags
`qwen3.5:4b` and `all-minilm` are examples, not performance recommendations. Use
the same runtime while comparing a pair; its pinned identity belongs to the
partition. Do not change revisions to escape pending state or increase caps
automatically after a stop.

For MCP, call `questlock_status`, copy each reviewed request into its discovered
`request_root`, then call `questlock_delegate` with, for example:

```json
{"request_path":"support-faq.request.json"}
```

Inspect the additive `semantic_shadow` observation separately from original
generation usage. A candidate score is not a validity proof.
`embedding_input_tokens: null` means unreported; `generation_avoided` stays false.
Embedding errors are diagnostic and do not trigger fallback or generation retry.

[Full boundaries, limits and qualification cases](../../docs/semantic-shadow.md).
