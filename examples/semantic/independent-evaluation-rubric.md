# Independent synthetic answer-reuse rubric

Frozen corpus: `independent-evaluation.json`
SHA-256: `36d9e0e1f6fef4a1985aa7632bdd99968384274792ca53a287b4540c584ab40a`
Labels: 120 total; 60 reuse, 60 withhold; six cases with 10 of each label.

This is a declared synthetic challenge workload, authored before live validation.
It does not establish general held-out representativeness, independence between
pairs, real-user acceptance, or production safety. The cases are independently
invented; previous corpora and results were not inspected or compared for novelty.
The author read repository instructions and `semantic_evaluate.go` for schema and
bounds, and did not inspect verifier prompts, verifier implementations, or lexical
guard implementations. No live models or paid calls were used in authorship.

## Decision rule

Serve the cached answer unchanged only when it fully answers the variant using
the supplied evidence and instructions. Preserve facts, entity, time, uncertainty,
scope, language, output form, and read-only limits. A paraphrase may reorder or
reframe the request without requiring a different answer. Withhold when answering
correctly requires changed facts, omitted or additional material, another language
or representation, an explicit unknown, or an external action response.

The labels assess unchanged-answer suitability, not whether the new question is
answerable at all. A refusal, correction, unknown statement, reformatted answer,
or fresh supported explanation may be the appropriate new answer for a negative.
Use the per-variant reason to identify the material difference; topical similarity
alone does not override it. Do not change labels after inspecting runtime results.

## Factual answer review

For every source or newly generated answer, check that each factual assertion is
supported by that case's evidence and that all requested facts and constraints
are met. Record unsupported claims, omissions, wrong calculations, lost uncertainty,
wrong time or entity, output-format violations, and claimed actions individually.
A factually correct excerpt can still fail if it omits required information or
violates an explicit answer constraint. Do not score shorter text as inherently
better. Assess generated answers against the actual variant, not the original
question alone.

| Family | Required original-answer facts |
| --- | --- |
| supportFAQ | Beacon portal/email, no telephone, Monday 00:00-Friday 23:59 UTC staffing, four staffed hours to first human response rather than resolution, Critical ticket and workspace/start/request identifiers without secrets. |
| onboarding | Ren completes training and uploads certificate; Nomi verifies and records clearance; Maya enables only after clearance and assigns Cerulean/vault-viewer; completed prerequisites and no production/vault-editor approval. |
| incident | 19:36 UTC Monitoring/open, European read delays 19:10-19:26, accepted/no reported lost writes, standby shift 19:18, normal latency 19:26 and errors 19:34, unconfirmed hypothesis, checkpoint 19:49, no user action. |
| release-readiness | Production Hold/no signoff; completed checks; Inez's successful Cedar rehearsal with rollback, Remy's passing peak-load report, both accepted before Sal's signoff; staging remains permitted. |
| refund | Twenty-day request within thirty-day window, unopened inspection/acceptance pending, USD 84-6-7 = 71, USD 9 shipping excluded, original card within five business days after acceptance, no approval or issue yet. |
| brokerlifecycle | Pending worker-rill at 11:09/deadline 11:15, version 4 not reusable for this task/no receipt, checked expected-4 success atomically creates version 5/receipt/Done, failure or expiry/unknown preserves Pending, operator reconciliation/no automatic takeover or fresh generation. |

## Reporting limits

Report the corpus SHA, all six family counts, false acceptances, missed positives,
abstentions, execution failures, and unattempted pairs. Separate retrieval misses
from verifier outcomes where available. Inspect every served negative. Keep the
frozen source answers and labels visible; any later revision is a new suite with
a new SHA and a stated reason, not a silent correction of the measured sample.

This suite measures cached-answer decision quality with fixed source answers.
Actual answer generation and factual review are separate evidence; fixed-answer
runs alone establish no answer-generation savings or user acceptance.
