# Round-four unsupported-answer controls

This separate control suite uses the same six newly authored synthetic contexts as the main round-four suite, but deliberately replaces each cached answer with an unsupported or incomplete answer. It is not a new independent context population. Each case supplies the original complete-overview question and one faithful paraphrase. Both must reject the bad answer, even though question equivalence is strong.

## Exposure and label rule

The author received the schema and assignment constraints and read only repository `AGENTS.md`, `CONTRIBUTING.md`, and `SECURITY.md` before authoring. No evaluator, verifier/guard implementation, preexisting corpus, results, private reports, or Git diff was read. Only these newly authored JSON files were subsequently read for validation. No live provider/model call, paid API, or evaluator run was used.

Reuse requires that the unchanged cached answer be fully supported by the approved evidence and fully satisfy the requested overview. These negative labels concern answer validity, not a change of user intent. Neither supplied question alters the evidence or authorizes the fabricated facts. An unchanged original question cannot repair an unsupported answer.

## Defects that make reuse invalid

| Case | Deliberate cached-answer defect | Evidence or instruction that rules it out | Labels |
| --- | --- | --- | --- |
| r4-unsupported-support | Changes the download-link lifetime to 61 minutes | The evidence explicitly says 19 minutes | 2 false |
| r4-unsupported-onboarding | Requires a perfect 21/21 lab score | The stated pass threshold is at least 18/21 | 2 false |
| r4-unsupported-incident | Invents a confirmed expired routing certificate as root cause | The cause is explicitly unconfirmed; no certificate fact is supplied | 2 false |
| r4-unsupported-release | Keeps a short no-go note but drops most mandatory overview coverage | Instructions require all owners, gates, rollback, schedule, and pilot/public scope as well as the test blocker | 2 false |
| r4-unsupported-refund | Makes engraving refundable and approves the full $137.40 | The evidence excludes $34.80 engraving and approves $102.60 | 2 false |
| r4-unsupported-broker | Invents zero provider usage and permits release without a receipt | The outcome is unknown and Ola must reconcile a provider receipt before recording usage and releasing unused units | 2 false |

There are exactly 6 cases x 2 variants = 12 labels: 0 true and 12 false. The original-question metadata is not an additional scored variant. The first scored variant intentionally repeats that original question, and the second is a faithful full-overview paraphrase. No question supplies a new fact that could support the deliberately bad answer. This tests why question equivalence alone is insufficient.

## Verification and limits

A standalone offline check verified schema keys, schema version, six required families, nonempty strings, Boolean labels, unique identifiers, two distinct questions per case, and all-false labels. It compared each control to the main suite and verified identical instructions, evidence, original question, and intent, with a different cached answer. The author manually checked the precise defect and confirmed that the question does not excuse it. Shared arithmetic was recomputed offline: $89.70 + $12.90 = $102.60 and $102.60 + $34.80 = $137.40; 6,713 - 4,899 = 1,814 in the main suite's receipt variant. No unsupported control's invented number is presented as a correct result.

The main and control suites together contain 132 scored variants: 60 true and 72 false. These controls intentionally include a direct contradiction, an unjustified stronger claim, and an incomplete answer. Their failure modes may be easier than subtle production errors; success on them does not establish general factual verification. The whole-overview coverage requirement is explicit and is the interpretive boundary for the incomplete release answer. These are synthetic business policies, not real-world financial or operational advice. The author's self-audit is not an independent human adjudication.

## Metadata-only preflight repair

Before any round-four live call, offline preflight identified an additional intent-label requirement: nonblank, at most 128 UTF-8 bytes, and no whitespace. Only the six `intent` string values in each JSON suite were changed to stable hyphenated labels. Every question, instruction, evidence string, answer, variant, label, identifier, and all JSON text outside the intent-string tokens was preserved byte for byte. No guard or implementation was read or relaxed. An offline schema-only check verified all twelve labels, unique case/variant identifiers across both suites, unchanged label counts, and identical corresponding intent labels. The frozen JSON hashes below supersede the initial hashes; the original hashes are retained privately by the coordinating agent for audit.

## Freeze

`unsupported-round4-evaluation.json` SHA-256: `37b519c2e7927c8afa2fec38a60da44fbe4a7d1509b4591a30704a573260b195`.

Main companion `independent-round4-evaluation.json` SHA-256: `4137899993b1ea6eb9650babe865b83cc564147dae4b32a166c48f7fae52cb3a`.

Final hashes for all four deliverables are supplied at handoff. The rubric omits its own hash to avoid self-reference. Only the four requested artifact files are part of this author's changes.
