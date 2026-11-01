# Check the answer once. Check each new question separately.

**Release gate: not qualified.** The previous question-only candidate reused 37 of 72 eligible
paraphrases and incorrectly accepted two of 72 changed requests in a 144-pair
regression run. It withheld all 12 deliberately unsupported source answers in a
separate check. These are development results, not fresh blind qualification.
The false acceptances involved a changed refund price basis and a new fee-refund
condition. The revised answer-aware candidate still needs fresh qualification.
Keep it out of production; the published policy is unchanged.

Someone asks the same thing in different words. We want the saved answer back,
without quietly borrowing an answer to a different question.

The experimental `factorized-v1` policy splits that decision into two local
checks. First, a source check reads the original question, evidence and saved
answer. It must confirm that the answer is supported **and complete**. Then an
applicability check reads that exact answer, the new request and the same evidence.
It asks whether the unchanged answer actually satisfies the request. Different
wording, or a narrower question, can be fine; changed facts or missing detail
are not. A prose answer does not become JSON just because the facts match.

No question list is required. Both checks keep the full approved background;
a context hash or a generated summary cannot replace it. The second check sees
the original question and full saved answer, including its keys, values and layout. That costs input
tokens, but a coarse label such as “JSON object” cannot tell us whether the keys
match. The source check can be cached; net savings still need measurement.
The receipt stage named `equivalence` records this applicability check. Intent
labels still partition work and bind its identity; they are omitted from the
model input so a source label cannot define what the new question means.
The verifier uses native system/user separation rather than wrapping its data
record in another serialized request. All evidence and decoder-schema bytes
still count against preflight and runtime limits.

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
the combined decision receipt is not another inference call. An unconfirmed
inference outcome stops the target without automatic retry or paid fallback. A complete
rejection or `unknown` verdict with confirmed usage is a known abstention; the
approved workflow may continue to fresh bounded local generation.

## What gets reused

The source certificate binds the original job version, content and output hashes,
original request, approved evidence, verifier policy and installed runtime.
Its task result can be reused for up to one hour. Reading it does not extend that
window. Each question decision also binds its source certificate, the exact answer and
current request.
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

## Where FreeToken fits

[FreeToken](https://github.com/FlashML-org/FreeToken) serves open-weight models
on local hardware. Its [current installation requirements](https://github.com/FlashML-org/FreeToken/blob/main/docs/install.md)
are Linux x86-64 with an NVIDIA GPU; this is not an Apple Silicon backend.
Its [OpenAI and Anthropic APIs](https://github.com/FlashML-org/FreeToken/blob/main/docs/quickstart.md)
make it worth assessing as a separate provider. This verifier currently uses
Ollama. API compatibility alone does not validate token preflight, bounded output,
usage accounting, runtime identity or uncertain-call recovery. We have not added
a FreeToken adapter or measured its speed or reuse quality. A faster runtime
still needs the same answer-validity tests.
