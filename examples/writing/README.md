# A writing task you can carry into another chat

Use an approved reference for facts and style. Add the evidence for today's
task. Keep both explicit; nobody needs to dig through an old conversation.
These files describe a fictional support team. Put your real reference outside
your checkout and review it before use.

First ask your connected worker for `questlock_status` with `{}`. Use its
`request_root`; do not assume the managed setup's default paths or broker port.
If the tool is missing, update the worker runtime and reconnect your client.

From a checkout, with `questlock` on your path:

```sh
# Use an existing worker root. Init does not change any client configuration.
questlock writing init --workspace /absolute/approved-requests \
  --reference "$PWD/examples/writing/reference.json" --model YOUR_INSTALLED_MODEL
questlock writing plan --workspace /absolute/approved-requests \
  --evidence "$PWD/examples/writing/evidence.json" --request support-one.json
```

Both commands are offline. Init creates a private `writing-workflow.json` that
pins the reference hash. Plan creates a private request plus a provenance
manifest. Review the files, then explicitly call `questlock_delegate` with
`{"request_path":"support-one.json"}`. It permits local Ollama only; paid
fallback is absent. Installed weights and enough memory are still required.

The default contract is plain text, 130–165 words, two paragraphs, and two
process-phrase exclusions. Edit the private recipe deliberately for your task.
Do not raise token caps automatically after a budget stop. The checks catch
length and formatting mistakes; you still review the facts and suitability.

Save the worker's `structuredContent` object directly to a private JSON file.
After review:

```sh
questlock writing record --workspace /absolute/approved-requests \
  --manifest support-one.json.writing.json --result /absolute/worker-result.json \
  --review assistant_reviewed --broker http://127.0.0.1:YOUR_BROKER_PORT
questlock writing show --reference synthetic-support-team \
  --broker http://127.0.0.1:YOUR_BROKER_PORT
```

Those commands use the worker's broker scope via `QUESTLOCK_TOKEN_FILE` or
`QUESTLOCK_TOKEN`. They make no model calls. The index shows profile/evidence
versions and an answer artifact reference; it does not claim human acceptance.
Use the outcome workflow to record a person's acceptance, rewrite and effort.

Repeat the same approved request to test exact reuse. Then plan
`changed-evidence.json` under a different filename and compare request hashes;
the changed fact gets a different snapshot. An older reviewed answer remains a
historical reference after the inference reuse window expires.

[Rules, failure handling and limits](../../docs/writing-workflows.md).
