# Round-two unsupported-source rubric

This suite has six cases, one for each business family, with two semantically suitable question paraphrases each. All 12 labels are false because the complete cached answer is already substantively false or unsupported for its own original task. The questions intentionally preserve scope, requested format, and read-only intent; they do not repair the bad source answer.

## Decision rule

Reject reuse of an entire answer containing a substantive unsupported assertion, a false gate state, incorrect money or credit arithmetic, an invented investigation result, or an invented lifecycle exception. Other correct lines and a very similar question do not offset that error. False here means the whole answer is unsuitable, not that question paraphrasing itself changed the task.

| Case | Substantive source defect | Required factual treatment |
|---|---|---|
| r2-bad-faq | Invents customer-owned encryption at rest. | The link lasts 48 hours and transit uses TLS; at-rest method and key ownership are unknown. |
| r2-bad-onboarding | Says pending mentor approval is complete and declares access ready. | Course and identity passed; mentor approval remains pending, so access is not yet permitted. |
| r2-bad-incident | Claims an unstarted loss assessment confirmed no permanent loss. | Preserve the mitigation times and owner; root cause and permanent-loss result remain unknown. |
| r2-bad-release | Invents passed license audit and captain approval. | The required audit is pending and approval absent, so release is not ready. |
| r2-bad-refund | Refunds the excluded activation fee. | Refundable base is 78 credits, excluded activation fee 12, total paid 90; no refund performed. |
| r2-bad-broker | Invents a six-minute exception allowing uncertain-claim clear and retry. | Owner reconciliation is mandatory regardless of elapsed time; preserve drain/detach/stop/replacement gates. |

## Authoring boundary and limits

These are newly authored synthetic fixtures, created from invented names, facts, numbers, policies, questions, and answers. The author read only repository AGENTS.md, CONTRIBUTING.md, SECURITY.md, and cmd/questlock/semantic_evaluate.go. A whole-file read of that schema-bearing evaluator also displayed evaluator dispatch, reporting, harness logic, and protected-term call sites; this disclosure limits any claim of complete implementation blindness. No core verifier implementation, guard implementation, policy file, prior corpus, report, evaluation output, or result summary was opened. No model call, external call, credential access, branch change, commit, or publication was used in authoring. Labels were chosen from the supplied facts and task requirements, with no feedback from execution results.

These independently authored synthetic source-defect challenges are separate from prior corpora; the evaluator-file exposure above limits broader blindness claims. This small purposive suite does not establish real-user acceptance, representative traffic, statistical independence, or production safety. A valid qualification run must retain false acceptances and abstentions rather than alter this frozen population after feedback.

## Freeze

Freeze the JSON and rubric before any verifier/model run. The handoff supplies SHA-256 digests externally so this rubric has no self-referential hash. JSON is UTF-8, two-space indented with a final newline. Structural/count validation is offline and bounded; it does not use verifier outcomes or model output and does not measure quality.
