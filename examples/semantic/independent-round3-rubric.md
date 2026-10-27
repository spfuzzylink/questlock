# Round-three independent author population rubric

This targeted synthetic suite has six business-family cases, each with 10 suitable natural paraphrases and 10 materially unsuitable variants: 120 pairs, 60 true and 60 false. Each source answer is intended to be correct and comprehensive for its original task. Names, records, numbers, policies, and IDs are fictional.

## Label contract

Evaluate the whole cached answer under the question and the persistent case instructions. Those instructions deliberately carry a complete business handoff and its output structure so natural questions need not repeat a checklist or preserve word order. All true questions still request that same complete handoff. An explicit format change in a question overrides the default format for that variant.

True requires factual support, correct arithmetic and ordering, the right actor/authority relation, every required uncertainty, and the same read-only task scope. False requires a material mismatch, not mere lexical novelty. A changed numerical detail can invalidate an otherwise unchanged high-level decision. A newly requested absent fact must be stated unknown; a different unknown does not answer it. Cached text cannot execute or attest a new action.

## Original-answer completion criteria

| Family / case | Required completion |
|---|---|
| support-faq / r3-digest | Four bullets. Birchwave at 09:17 UTC has 2 alerts for the 09:30 boundary, 13 minutes away. Editor Rina Sol cannot change the 45-minute interval; an administrator can. Personal email may be muted without stopping webhooks. Actual delivery time is unknown; no settings changed. |
| onboarding / r3-contractor | Three numbered steps. Identity/course complete. Device validity is 72 hours from August 10 noon to August 13 noon, leaving 2 hours at the 10:00 check; refresh if expired before grant. Mira Quill filed but cannot self-approve; Oren Hale approves before IT creates account. Project owner then grants read-only FallowViewer without secret export. Contract end date unknown; nothing provisioned. |
| incident-handoff / r3-failover | Five bullets. QL-86 is SEV3 monitoring at 15:59 with 2/3 checks passed; third at 16:04 and commander approval still needed. 72/240 jobs failed = 30%; 9/60 tenants = 15%; impact lasted 8 minutes. Timeline is 15:41 start, 15:46 east-to-west routing, 15:49 baseline. Devika Rao commands/approves; Pavel Nix communicates and cannot close. Root cause/reconciliation unknown; no actions. |
| release-readiness / r3-package | Four labeled lines decision/passed/remaining/unknowns. Internal-preview 0.9.8 not ready. 192/192 load tests; 56 of 64 MiB leaves 8 MiB; 3/4 canary checks = 75%, fourth pending. Fourth check precedes Arun Voss independent review, then changelog approval, then captain sign-off. Author Nell Sato cannot self-approve. Publication time unknown; no review/approval/signing/publication action. |
| refund-eligibility / r3-seat-refund | Four numbered lines. Request is 8 days after purchase within inclusive 10-day window. Five never-activated seats at 18 give 90 credits refundable. Two activated seats cost 36 plus fee 9, excluded 45; 90 + 45 = 135 total. Route only to original payer Celia Dune; settlement unknown; no refund issued. |
| shared-broker-lifecycle / r3-maintenance | Five numbered steps. Priya Gale authorizes, Jalen Orr only reports. PID 9842, 127.0.0.1:5526, atlas-drafts, 3 clients, 5 queued writes. After authorization quiesce and drain before reconciling unknown outline-6; J-341 is only a reservation. No forced clear/retry from elapsed time. Reconcile, detach, stop; same-host replacement with existing state after exit and listener free. ETA unknown; no operations. |

## Negative coverage

Per-variant reasons are frozen in JSON. Changes include actor/role inversion, source/destination reversal, ordering reversal, conjunction or threshold changes, changed snapshot counts and times, unchanged-decision arithmetic differences, unsupported probabilities/latency/throughput, newly required unknowns, format and audience changes, and requested actions. The suite is intentionally targeted; there is no claim that its class balance or vocabulary represents operational traffic.

## Authoring boundary

Round-three authoring used newly invented facts, entity names, IDs, numbers, questions, and answers. During this round the author did not open any previous suite or rubric, core verifier implementation, guard implementation, policy, report, evaluation output, result summary, or metric. Repository instructions had been read in the earlier authoring task: AGENTS.md, CONTRIBUTING.md, and SECURITY.md. The earlier schema read used the whole cmd/questlock/semantic_evaluate.go file and therefore exposed evaluator dispatch, reporting, harness logic, and protected-term call sites; that prior exposure is disclosed rather than claiming complete implementation blindness. No core verifier or guard implementation was opened then. No model call, network call, credential access, branch change, commit, publication, or edit to an older corpus was used for this population. Labels were fixed from the supplied facts and persistent instructions without evaluation feedback.

## Freeze and limits

Both this rubric and its JSON must be frozen before model evaluation. SHA-256 digests are supplied in the handoff instead of embedded in the files, avoiding a self-referential rubric hash. Structural validation is offline and bounded; it checks schema, duplicate keys, text/label bounds, counts, IDs, source formats, and author-side arithmetic. It does not run the verifier or establish model accuracy.

This is an independently authored challenge population relative to prior corpus files, subject to the evaluator-file exposure disclosed above. It does not establish universal accuracy, real-user acceptance, statistical independence, representative-workload performance, production safety, or generation savings. Preserve false acceptances, abstentions, and failures. Do not relabel or remove a case after outcomes are known; any factual correction belongs in a separately identified revision with the original retained.
