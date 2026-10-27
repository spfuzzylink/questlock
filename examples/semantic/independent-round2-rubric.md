# Round-two independent author population rubric

This suite contains six cases, one per business family, with 10 suitable paraphrases and 10 materially unsuitable variants per case: 120 labeled pairs, 60 true and 60 false. The source answer in every case is intended to be fully supported and comprehensive for its original question.

## Decision rule

Judge the entire cached answer, not similarity. True requires supported facts, correct arithmetic and chronology, all required unknowns, the same task scope, and compliance with the new question's explicit output structure. False requires a material failure; a harmless wording change alone is never a negative. A changed fact can invalidate a cached detail even when the overall decision stays the same. An absent fact requested by the new question must be explicitly acknowledged as unknown; a different unknown is not a substitute. Guidance does not execute an action or attest completion.

## Family-specific original completion criteria

| Family | Required supported completion |
|---|---|
| support-faq | Four bullets: 14:06 failed state, retry no earlier than 14:07 and 1-minute wait; 164 active rows with 9 archived excluded; no current link and 72-hour lifetime from successful completion; completion time unknown and no retry performed. |
| onboarding | Four numbered steps: sponsorship and agreement complete; 82 is below 85 and retake first; identity after passing, then security sign-off, then workspace-owner LedgerReader grant; reading only, no payment exports; hardware ship date unknown and no access change. |
| incident-handoff | Two-column Markdown table with six body rows: SEV2 and incomplete observation at 09:31; 20% request failures, 40% affected partitions, 12-minute impact; 09:14/09:23/09:26 event order, rollback not run; conditional 09:36 closure after 10 stable minutes; Imani Cole; cause and permanent loss unknown; read-only/no state change. |
| release-readiness | Four bullets: 3.6.2 not ready at 16:40; missing rollback drill, provenance attestation and captain approval; 412/412 tests and docs passed; 7 of 8 minutes leaves 1 minute; 0 critical/0 high/2 medium accepted by Ravi Sen; drill then attestation then captain approval before publication; ETA unknown; no performed gate or publication. |
| refund-eligibility | Four numbered lines: monthly, 20 elapsed days within inclusive 21 and 148 units within 150; eligible base 84; setup 16 plus prepaid 20 excludes 36; 84 + 36 = 120; settlement unknown and no transaction performed. |
| shared-broker-lifecycle | Five numbered steps: broker 7314, listener 127.0.0.1:4517, chart-build, 2 clients, 3 receipts, unknown card-17; Leah Moss pauses intake then reconciles; J-88 is a receipt rather than completion proof; never clear/retry by timeout; drain to zero then detach then stop; replacement after exit and listener free; recovery ETA unknown and no operations. |

## Negative coverage

The 60 negatives are fixed at authoring time and span subtle snapshot changes, changed prerequisites and ordering, arithmetic with unchanged top-level decisions, newly required unknowns, unsupported guarantees or permissions, exact output-format changes, changed entity/version/owner scope, and requests for actions. Each JSON variant has its own concrete reason; these reasons define the expected failure rather than any implementation guard or retrieval score.

## Authoring boundary and limits

These are newly authored synthetic fixtures, created from invented names, facts, numbers, policies, questions, and answers. The author read only repository AGENTS.md, CONTRIBUTING.md, SECURITY.md, and cmd/questlock/semantic_evaluate.go. A whole-file read of that schema-bearing evaluator also displayed evaluator dispatch, reporting, harness logic, and protected-term call sites; this disclosure limits any claim of complete implementation blindness. No core verifier implementation, guard implementation, policy file, prior corpus, report, evaluation output, or result summary was opened. No model call, external call, credential access, branch change, commit, or publication was used in authoring. Labels were chosen from the supplied facts and task requirements, with no feedback from execution results.

This is an independently authored test population relative to prior corpora, with the evaluator-file exposure disclosed above. It does not establish real-user acceptance, a statistically independent representative workload, model quality in general, production safety, or benefit from answer generation. Do not tune labels or remove failures after seeing results. Future corrections must preserve the frozen original, document a factual error, and create a separately identified revision. Validation below is structural and author-side only; it makes no reuse-performance claim.

## Freeze

Freeze the JSON and this rubric before any verifier/model run. SHA-256 digests are supplied in the author handoff rather than embedded here to avoid a self-referential rubric hash. The source JSON is canonical UTF-8 with two-space indentation and a final newline. There were no model or network calls. An offline bounded structural validation checks the declared schema and counts; no semantic result was used to author or revise labels.
