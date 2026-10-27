# Round-three unsupported-source rubric

Six cases cover the same six business families with two natural question paraphrases apiece. All 12 labels are false because the cached source answer contains a substantive factual defect. The new questions preserve the original business task and persistent output constraints; their natural wording does not preserve a fixed word order and does not introduce the rejection cause.

## Rejection rule

A source answer with a false authority relation, channel relationship, arithmetic result, inequality, inclusive-boundary test, or missing shutdown conjunct is unsuitable as a whole. Similar questions and surrounding correct facts do not repair it.

| Case | Source defect | Supported treatment |
|---|---|---|
| r3-bad-notices | Treats personal-email muting as pausing webhooks. | Email mute has no webhook effect; only workspace owners modify the separate webhook rule. |
| r3-bad-review | Allows sponsor Tessa Venn to self-approve and makes Hugo Marr optional. | Hugo's independent approval remains mandatory and absent, so access is not ready. |
| r3-bad-ratio | Calls 84/280 a 70% failure rate. | Failure rate is 30%; 70% is the success fraction. |
| r3-bad-cap | Calls 73 MiB three MiB below a 70 MiB cap and declares ready. | The package is three MiB above the cap and fails a mandatory gate. |
| r3-bad-deadline | Replaces inclusive at-least-72 hours with strictly-more-than-72. | Exactly 72 hours qualifies: 63 refundable + 7 excluded = 70 paid. |
| r3-bad-client-gate | Allows shutdown with four attached clients because writes are zero. | Both writes and client references must be zero; stopping is not yet permitted. |

## Authoring boundary

Round-three authoring used newly invented facts, entity names, IDs, numbers, questions, and answers. During this round the author did not open any previous suite or rubric, core verifier implementation, guard implementation, policy, report, evaluation output, result summary, or metric. Repository instructions had been read in the earlier authoring task: AGENTS.md, CONTRIBUTING.md, and SECURITY.md. The earlier schema read used the whole cmd/questlock/semantic_evaluate.go file and therefore exposed evaluator dispatch, reporting, harness logic, and protected-term call sites; that prior exposure is disclosed rather than claiming complete implementation blindness. No core verifier or guard implementation was opened then. No model call, network call, credential access, branch change, commit, publication, or edit to an older corpus was used for this population. Labels were fixed from the supplied facts and persistent instructions without evaluation feedback.

## Freeze and limits

Freeze both files before evaluation; external SHA-256 digests identify their exact bytes. No model or verifier result was consulted. Offline structural and arithmetic validation is not a quality measurement. This small purposive synthetic population cannot establish universal accuracy, real-user acceptance, workload representativeness, statistical independence, or production safety. Keep the frozen cases and all outcomes; document corrections in a distinct revision rather than tune the labels from feedback.
