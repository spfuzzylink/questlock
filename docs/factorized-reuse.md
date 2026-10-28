# Check the answer once. Check each new question separately.

**Release gate: not qualified.** The current candidate reused 37 of 72 eligible
paraphrases and incorrectly accepted two of 72 changed requests in a 144-pair
regression run. It withheld all 12 deliberately unsupported source answers in a
separate check. These are development results, not fresh blind qualification.
The false acceptances involved a changed refund price basis and a new fee-refund
condition. Keep this candidate out of production; the published policy is unchanged.

Someone asks the same thing in different words. We want the saved answer back,
without quietly borrowing an answer to a different question.

The experimental `factorized-v1` policy splits that decision into two local
checks. First, a source check reads the original question, evidence and saved
answer. It must confirm that the answer is supported **and complete**. Then a
question check compares the original and new requests under the same evidence.
It requires equivalent meaning in both directions and the same output contract.
A prose answer does not become a JSON response just because the facts match.

No question list is required. Both checks keep the full approved background;
a context hash or a generated summary cannot replace it. The second check omits
the answer itself and receives a coarse format category: JSON object, array,
scalar or non-JSON text. That category contains no keys, nested schema, language
or text layout and cannot prove output compatibility. Omitting the answer can
reduce repeated verifier input after the source check is cached; net savings
still need measurement.

This is still an experimental verifier. Qualify it on your own work before
serving its results. A valid JSON verdict or high embedding similarity does not
prove that a model understood the question.

## Opt in explicitly

Add these settings to an otherwise complete [controlled reuse request](controlled-reuse.md):

```json
{
  "verifier_policy": "factorized-v1",
  "verifier_model": "qwen3.5:9b",
  "verifier_max_input_tokens": 16384,
  "verifier_max_output_tokens": 128,
  "verifier_timeout_ms": 60000
}
```

The model is an example, not a quality recommendation. Install weights separately.
The worker selects the requested policy. Go callers supply
`delegation.NewFactorizedVerifier` from their `ControlledReuser.VerifierFactory`
and set its `DailyTokenBudget` to the approved task's shared budget, just as for
`NewLocalVerifier`. Omitting `verifier_policy` keeps the standalone verifier.
Older workers must reject unsupported settings rather than silently ignoring them.

The two stages each get half the declared input and output allowance, under one
verifier deadline. Their actual usage and durable receipts appear separately in
`verification.work`. Outcome accounting flattens those stages into verifier work;
the combined decision receipt is not another inference call. An unknown or
unconfirmed stage stops the target without automatic retry or paid fallback.

## What gets reused

The source certificate binds the original job version, content and output hashes,
original request, approved evidence, verifier policy and installed runtime.
Its task result can be reused for up to one hour. Reading it does not extend that
window. Each question decision also binds its source certificate and both requests.
The final answer alias still inherits the original answer's expiry, even when the
certificate remains available longer.

Exact task lookup stays first. An exact hit skips embeddings and both verifier
stages. Pending and uncertain claims retain their existing fences. This option
adds no action replay, source promotion, weight training or automatic cancellation
of another agent.

The default verifier retains its conservative literal guards. This policy uses
text-syntax guards and requires the contextual verifier to detect changed numbers,
identifiers, roles, conditions and output requirements. That makes independent
hard-negative tests essential; a spelling comparison cannot make this decision.

## Measure the whole attempt

The first new wording pays for source certification and question comparison.
Later new wordings may reuse the certificate. Exact repeats of a resolved task
need neither. Count all three paths separately, including embeddings, misses,
failed answers and uncertain work. For short answers, checking can still cost more
than answering fresh. There is no automatic token-saving claim for this option.

The [six-case measurement](../examples/paraphrase-reuse/README.md) runs fresh
sources, two new wordings per source, exact repeats and optional fresh baselines.
Those examples exercise the integration; they do not establish general language
accuracy. Freeze a separate population before evaluation, test changed facts and
constraints, and report precision, recall and coverage by use case.
