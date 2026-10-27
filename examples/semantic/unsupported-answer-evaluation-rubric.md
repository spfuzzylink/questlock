# Unsupported-source-answer control extension

Frozen control suite: `unsupported-answer-evaluation.json`
SHA-256: `1699cb7c7a6a4fea915abacb5e443808e608d52b5d843a7660aa37339e594def`
Labels: 12 withhold, zero reuse; six families with two fair paraphrases each.

This is a separate extension to `independent-evaluation-rubric.md`, not a revision
of its 120-pair suite. That original file remains frozen at SHA-256
`36d9e0e1f6fef4a1985aa7632bdd99968384274792ca53a287b4540c584ab40a`.

These public synthetic records introduce new fictional names, IDs, and facts.
Each cached source answer deliberately contains a material grounding error.
Both variants per case preserve the original question's facts, scope, and prose
form. Every label is false because serving the answer unchanged would preserve
the planted error. No changed-question cue is needed to reject these controls.

## Required factual corrections

| Family | Planted source-answer defect | Evidence-grounded result |
| --- | --- | --- |
| supportFAQ | Claims a two-hour first-human-response target. | Ridge's target is six staffed hours; coverage, email channel, ticket fields, and lack of resolution guarantee otherwise remain as recorded. |
| onboarding | Allows immediate account enablement and defers security clearance. | Oren must record security clearance before Lila may enable Eda's account; manager approval alone is insufficient. |
| incident | Calls an index-refresh backlog the confirmed root cause. | The backlog is only a working hypothesis; no root cause is confirmed. |
| release-readiness | Says 45 milliseconds is at most 40, so the gate passes and a new report is unnecessary. | 45 exceeds 40; the gate fails, Davi owes a passing report, and Mira cannot yet sign off. |
| refund | Claims 105 minus 8 is 102. | The estimated refund is USD 97; USD 11 outbound shipping stays excluded, and inspection/acceptance remain pending. |
| brokerlifecycle | Invents `bgctl task recover ... --receipt-auto`, automatic receipt reconstruction, and reconciliation without external evidence. | Only `bgctl task show TK-6738-NF` and `bgctl receipt list --task TK-6738-NF` are documented read-only inspection commands; preserve Pending/version 9 until external-evidence reconciliation. |

Inspect each served answer against the supplied evidence. An answer that repeats
any planted error fails factual grounding even when it matches the question's
wording. A fresh answer should supply the correction while meeting the actual
question's remaining requirements. A refusal or abstention is not proof of a
correct replacement answer, so review any generated replacement separately.

Report every served control as a false acceptance and retain its raw output.
Report abstentions, errors, retrieval misses, verifier decisions, and unattempted
pairs separately where available. This deliberately negative sample does not
estimate natural error prevalence, production precision, broad held-out quality,
or answer-generation savings. Do not combine it silently with the balanced
120-pair sample; report it as a separate grounding-control suite.

The author stayed blind to new verifier policy, guard implementations, old
corpora, and runtime results. No live model or paid calls were used to author or
validate these files. The SHA and all labels were frozen before live validation;
any later change requires a newly identified suite and new SHA.
