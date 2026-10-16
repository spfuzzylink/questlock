# SGLang profiles and qualification

These profiles describe SGLang **v0.5.20**, source commit
[`94602c9c2b7cbdb8efd5c52802dac6a1c180089e`](https://github.com/sgl-project/sglang/tree/94602c9c2b7cbdb8efd5c52802dac6a1c180089e).
They contain argument arrays, not model weights, credentials, an installer, or
benchmark results. Questlock supports native dense/HiCache generation;
HiSparse remains inspection-only pending PD ingress orchestration.

## Before launch

Use an isolated Linux GPU environment with the pinned SGLang source, supported
CUDA/ROCm drivers and kernels, sufficient GPU/host memory, and approved model
and tokenizer files at an absolute local path. Pin and record their digests.
Model licenses and hardware requirements remain the operator's responsibility.
For a CUDA source environment, the pinned
[Python package manifest](https://github.com/sgl-project/sglang/blob/94602c9c2b7cbdb8efd5c52802dac6a1c180089e/python/pyproject.toml)
specifies the dependency stack; use the corresponding upstream platform setup
for ROCm. Do not install this GPU dependency stack into your coding client's
environment or an existing Apple MLX environment.

```sh
git clone --filter=blob:none https://github.com/sgl-project/sglang.git sglang-v0520
git -C sglang-v0520 checkout --detach 94602c9c2b7cbdb8efd5c52802dac6a1c180089e
# In a fresh, activated GPU virtual environment:
python3 -m pip install -e ./sglang-v0520/python
```

Confirm `/server_info` reports exactly `0.5.20`; an unversioned source install
can report a development fallback and is rejected by this adapter. Preserve
the release tag metadata or use the corresponding official versioned package.

Keep the environment's dependency lock, GPU driver/kernel versions, source
revision, launch argv and model/tokenizer digests with the experiment. The source
pin alone does not freeze unpinned transitive dependencies. These setup commands
are instructions for a suitable host; they were not executed as a GPU benchmark
for this release.

## Launch dense or HiCache

From this repository, after activating that GPU environment:

```sh
python3 - deploy/sglang/dense.json /absolute/path/to/approved-model <<'PY'
import json, pathlib, subprocess, sys
profile = json.loads(pathlib.Path(sys.argv[1]).read_text())
model = pathlib.Path(sys.argv[2])
if not model.is_absolute() or not model.is_dir():
    raise SystemExit("provide an existing absolute model directory")
argv = [str(model) if arg == "MODEL_DIRECTORY" else arg for arg in profile["argv"]]
subprocess.run(argv, check=True)
PY
```

The argument array avoids shell evaluation. For HiCache, substitute
`deploy/sglang/hicache.json`. Size the example host/device pools for the actual
machine before launch; the sample ratio is not a universal memory setting.
Both profiles bind loopback and use `questlock-example` as their served model
name. [HiCache's upstream guide](https://github.com/sgl-project/sglang/blob/94602c9c2b7cbdb8efd5c52802dac6a1c180089e/docs/docs/advanced_features/hicache_best_practices.mdx)
explains compatible layouts and optional L3 backends. This sample enables L1/L2
HiCache only. Add L3 after deliberately configuring backend namespace,
credentials and storage permissions; the sample is not shared storage setup.

In another terminal, copy `task.json` outside the repository and replace
`deployment_id` with your pinned deployment identity. Set its `profile` to match
the selected server; its nested and top-level input caps must match. With an
existing broker and private `QUESTLOCK_TOKEN_FILE` configured:

```sh
questlock model plan --spec /absolute/path/to/my-task.json
questlock model runtime-inspect --spec /absolute/path/to/my-task.json \
  --api-base-url http://127.0.0.1:30000
questlock model run --spec /absolute/path/to/my-task.json \
  --api-base-url http://127.0.0.1:30000 --broker http://127.0.0.1:8080 --timeout 2m
```

Use the served model alias for `model`; the observed model path and tokenizer
also enter runtime identity. Runtime endpoints and any credentials stay in the
worker process. A worker server can select this endpoint with `--sglang-url`.
Do not expose the example unauthenticated loopback service directly to a public
network. See [shared broker setup](../../docs/deployment.md).

## HiSparse: inspect now, qualify PD separately

`hisparse.json` is a decode profile; `hisparse-prefill.json` is its prefill
prerequisite. They intentionally set `questlock_generation: false`. Use a
supported DSA or DeepSeek V4 model, compatible kernels, adequate pinned host
memory, and a working upstream PD router/transfer deployment. Select TP/DP,
network devices, transfer backend and host addresses for that deployment; these
minimal argv templates do not configure the RDMA fabric or multi-node topology.

Use the same array launcher above only after completing those prerequisites.
Then set a copied task's profile to `hisparse` and inspect port 30001.
`model run` rejects this direct decode target: token IDs alone lack the PD
bootstrap/transfer metadata. Do not point an ordinary completion request at it.
The pinned [HiSparse deployment guide](https://github.com/sgl-project/sglang/blob/94602c9c2b7cbdb8efd5c52802dac6a1c180089e/docs/docs/advanced_features/hisparse_guide.mdx)
is the reference for a full prefill/decode topology. Passing inspection is not
evidence that a PD pair generated an answer.

## Reproduce a runtime comparison

1. Save exact source, environment, weights, tokenizer, hardware, prompt IDs,
   sampling, output cap and concurrency. Select tasks with declared checks and
   record final acceptance, rewrites and reported human effort.
2. Set Questlock `reuse_for_seconds: 0` so each sample really calls the runtime.
   Start a fresh dedicated runtime for each cold sample. If testing L3, use an
   isolated empty backend namespace too; a process restart alone may leave L3
   warm. Never clear a shared production cache for this experiment.
3. Run the same prepared prompt again for a warm sample. Keep the stable prefix
   and token IDs identical; retain every result, failure and runtime counter.
   Alternate dense and HiCache profile order over repeated independent pairs.
4. Compare elapsed-time distributions, known input/output tokens, cache tiers,
   peak device/host memory, and accepted/reworked tasks. Record model loading
   separately. This nonstreaming adapter reports whole-call timing, not TTFT or
   per-token decode speed; collect those through a suitable runtime benchmark.
5. For HiSparse, benchmark a working external PD ingress with the same model and
   quality checks, with and without HiSparse under matched memory/concurrency.
   Keep those results separate until Questlock supports that ingress directly.

## Optional upstream CPU simulation

The pinned [simulator README](https://github.com/sgl-project/sglang/blob/94602c9c2b7cbdb8efd5c52802dac6a1c180089e/tools/sglang-simulator/README.md)
documents a predictor-based scheduler/cache simulator. From the pinned SGLang
checkout, in a separate compatible CPU environment:

```sh
python3 -m pip install -e tools/sglang-simulator
export PYTHONPATH="$PWD/tools/sglang-simulator/src:$PWD/python"
export SGLANG_USE_CPU_ENGINE=1
export CUDA_VISIBLE_DEVICES=""
export SGLANG_SIMULATOR_OUTPUT_MODE=OFFLINE
export SGLANG_SIMULATOR_OUTPUT_DIR=/tmp/questlock-sim-NEW-RUN-001
test ! -e "$SGLANG_SIMULATOR_OUTPUT_DIR"
python3 -m sglang_simulator.simulation.sglang.launch_server \
  --model-path /absolute/path/to/model-config-and-tokenizer \
  --sim-config-path /absolute/path/to/calibrated-simulator.json --port 30000
```

In a second terminal at the same checkout, set the same `PYTHONPATH` and output
directory, then send an approved timestamped workload:

```sh
python3 benchmark/simulator/bench_serving.py --simulator-mode offline \
  --backend sglang --base-url http://127.0.0.1:30000 \
  --model /absolute/path/to/model-config-and-tokenizer \
  --dataset-name autobench --dataset-path /absolute/path/to/trace.jsonl \
  --use-trace-timestamps --num-prompts 100 --profile \
  --output-file "$SGLANG_SIMULATOR_OUTPUT_DIR/benchmark.json"
```

Supply predictor/calibration data for the chosen replay, ML or AIConfigurator
mode and the real target's cache capacity/topology. Use the current run's
server-side logical metrics, not CPU wall-clock time. The pinned README lists
validated v0.5.16–v0.5.18/main compatibility and single-worker runtime scope;
it does not explicitly certify every v0.5.20 path or HiSparse PD. Run its
compatibility tests before using a new environment. These simulator commands
are provided for exploration and were not executed for this release.

Questlock's `model runtime-quest` is a different, lightweight HTTP-fixture test
and needs neither SGLang nor a GPU. Linux/AMD64 emulation, native Apple MLX,
simulation and real GPU inference establish different evidence; see
[the evidence table](../../docs/runtime-cache.md#evidence-levels-and-the-next-qualification).
