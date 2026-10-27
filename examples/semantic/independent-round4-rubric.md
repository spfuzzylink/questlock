# Independent round-four answer-reuse rubric

This is an independently authored synthetic challenge, frozen before observing any evaluator output. Its target is whether a supplied answer can be served **unchanged**, not whether a model could produce a new answer. All names, organizations, service names, identifiers, events, monetary amounts, and policies are invented. The `.example` address is synthetic.

## Exposure boundary

The author received the JSON schema and task constraints, then read only `AGENTS.md`, `CONTRIBUTING.md`, and `SECURITY.md` in the assigned checkout before writing these fixtures. No verifier or guard implementation, existing corpus, evaluator, result, private report, or Git diff was inspected. No evaluator-facing details beyond the supplied schema were available. The author subsequently read only these newly authored JSON files for offline validation. The repository's security guidance describes high-level reuse boundaries; it was not used to tune cases to an implementation. No live provider/model calls, paid APIs, or evaluator runs were used.

## Decision rule

A true label means the exact cached answer fully satisfies the variant's question under the approved instructions and evidence. The six answers are complete for their original overview tasks. Each overview instruction explicitly requires its full set of operational facts, even for a short request. All positives request that same full overview, leave the underlying facts unchanged, and permit the paragraph answer. Names and literal numbers are retained in some questions, while others use contextual references; no shared anchor phrase is required across all questions.

A false label has an explicit incompatibility. Updates and hypotheticals stated by a question replace conflicting snapshot facts for that variant. This is stated in the instructions, rather than inferred from a changed number alone. Additional facts, calculations, unknown-value disclosures, cited source material, external actions, and exact serialization requests are requirements only where the question explicitly asks for them. A cached answer that only supplies the old overview cannot satisfy those additions. For unavailable information, a fresh answer could acknowledge the gap; the cached answer still lacks the requested explicit disclosure. Action requests are judged as requests for actions and receipts, without pretending that a text answer can perform them.

## Case-level audit

| Case | Complete original-answer coverage | Positive / negative |
| --- | --- | --- |
| r4-support | Finalized versus draft eligibility; 37,000 cap; navigation; UTC start/end relation; 19-minute link; 43-hour copy; split ranges; private escalation identifiers; Beacon manual versus Cobalt schedule | 10 / 10 |
| r4-onboarding | Nema request; Ivo verification; IT training workspace; 29-minute lab and 18/21 threshold; pass plus signoff live gate; Day 4 15:35 UTC deadline; 11-hour retry; support privacy; contractor restriction | 10 / 10 |
| r4-incident | P2 and first signal; commander/database owner; deployment freeze versus ongoing writes; 35% reads south-6 to west-4; recovery points and clearance; 17-minute updates; reconciled writes; unconfirmed cause; 62-hour traces | 10 / 10 |
| r4-release | Candidate/environment; 52/53 and named blocker; patch/security/product owners; all gates and outstanding signoff; failed rollback and target; no-go/unknown ETA; slot/freeze; internal pilot/public gate | 10 / 10 |
| r4-refund | Eligibility and dates; total, approved components, excluded engraving; photo approval; carrier-claim owner; no return; original-card restriction; posting window; authorized but unsubmitted; no replacement | 10 / 10 |
| r4-broker | Queue/claim times and ownership; source binding and review boundary; stale rejection/no publication; held units and unknown outcome; reconciliation authority/order; fresh attempt/review; no retry/expiry/takeover; current state/retention | 10 / 10 |

## Negative-variant audit

The `n01` through `n10` columns below are descriptions of the explicit incompatibility, not similarity-score predictions. Each full question and its label rationale are in the JSON.

| Case | n01 | n02 | n03 | n04 | n05 | n06 | n07 | n08 | n09 | n10 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| support | New row cap | Draft/finalized reversal | Cobalt setup only | JSON only | Missing encryption/region fields | Generate and email file | Attestation quote and section | New retention | Date-boundary reversal | Minimum job calculation |
| onboarding | New pass threshold | Manager/steward role reversal | Contractor plan and missing end date | Table only | Missing payroll/capacity fields | Submit request | Handbook quotation/page | New signoff deadline | Training/lab order reversal | Earliest retry timestamp |
| incident | New traffic share | Route direction reversal | Write-routing drill and validation | CSV only | Missing impact/liability/vendor fields | Page and post | Board-minutes quotation/section | New clearance condition | Corrected alarm time | Percentage backlog reduction |
| release | New all-pass evidence | Patch/security role reversal | Different candidate | JSON only | Missing publication/digest fields | Public publication | Audit quote and identifier | Revised rollback threshold | External pilot audience | Test-pass percentage |
| refund | Engraving now approved | Buyer/specialist claim-role reversal | Different order | JSON only | Missing card/processor fields | Submit payment | Exception-letter quote and ID | Late corrected report date | Store-credit remedy | Refund percentage |
| broker | New source revision | Owner/reviewer reversal | Different ticket | YAML only | Missing receipt fields | Release and publish | Provider receipt quote/ID | Reconciled closed state | Automatic takeover policy | Updated receipt/unused units |

Each case has ten natural paraphrases and ten invalid variants: 6 x (10 + 10) = 120 labels, comprising 60 true and 60 false. Original questions are metadata and are not included in these totals. The separate unsupported-answer suite adds 12 false labels, producing 132 variants overall: 60 true and 72 false. The unsupported suite deliberately shares contexts and is a control set, not an additional independent population.

## Offline verification and arithmetic

A standalone structural check, without importing an evaluator or repository code, verified exact schema keys, schema version 1, six distinct required families, nonempty strings, Boolean labels, unique case/variant identifiers, unique questions within each case, exactly 20 variants per case, and the 10/10 split. It also verified that the separate unsupported suite preserves each context, original question, and intent while changing its cached answer.

The author manually matched each original answer to its evidence and reviewed every variant against the unchanged-answer rule. Arithmetic and dates were independently recomputed by a small offline Python check using `Decimal` and date/time arithmetic, rather than trusting the figures written into the reasons:

- 92,501 invoices require 3 jobs: 2 x 37,000 = 74,000 is insufficient and 3 x 37,000 = 111,000 is sufficient under the question's stated partition assumption.
- A failure at 2037-05-14 21:25 UTC plus 11 hours permits retry at 2037-05-15 08:25 UTC.
- The backlog reduction is (8,700 - 1,261) / 8,700 x 100 = 85.51% to two decimals. Thirteen minutes after 07:09 is 07:22.
- The rollback exceeds its original gate by 21 seconds. The pass rate is 52/53 x 100 = 98.11% to two decimals. The freeze precedes the release slot by 35 minutes.
- $89.70 + $12.90 = $102.60; plus $34.80 = $137.40. August 9 plus six calendar days is August 15; August 13 is four days after receipt and August 17 is late. The approved share is 74.7% to one decimal.
- 6,713 reserved units minus 4,899 used units leaves 1,814 unused units.

## Limits and remaining interpretation choices

This is a deliberately balanced challenge set, not a sample of production frequency. It supports neither a measured model accuracy nor an inference about cost savings, human acceptance, or provider behavior. The author's self-audit is not an independent human adjudication.

The labels depend on the explicit complete-overview instructions and the explicit rule that variant updates apply. A system that instead refuses to adopt a hypothetical, summarizes a serialized task without honoring its exact format, or treats source quotations as optional is evaluating a different contract. Those policy differences are the remaining interpretation boundary, not hidden facts. Unknown-source requests require acknowledging that the source is absent; they never license fabrication. Real-world business calendars, enrollment practices, incident policy, refund law, and broker behavior are outside these synthetic snapshots. No business-day arrival date is inferred from a posting window. Automatic actions cannot be assessed as executed by this text-only corpus.

## Metadata-only preflight repair

Before any round-four live call, offline preflight identified an additional intent-label requirement: nonblank, at most 128 UTF-8 bytes, and no whitespace. Only the six `intent` string values in each JSON suite were changed to stable hyphenated labels. Every question, instruction, evidence string, answer, variant, label, identifier, and all JSON text outside the intent-string tokens was preserved byte for byte. No guard or implementation was read or relaxed. An offline schema-only check verified all twelve labels, unique case/variant identifiers across both suites, unchanged label counts, and identical corresponding intent labels. The frozen JSON hashes below supersede the initial hashes; the original hashes are retained privately by the coordinating agent for audit.

## Freeze

`independent-round4-evaluation.json` SHA-256: `4137899993b1ea6eb9650babe865b83cc564147dae4b32a166c48f7fae52cb3a`.

The JSON hash binds this rubric to the authored suite. Final hashes of all four deliverables are reported with the handoff; the rubric intentionally does not contain its own self-referential hash. No implementation or evaluator changes accompany these four files.
