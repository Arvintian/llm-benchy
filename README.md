# llm-benchy (Go)

`llm-benchy` is a Go port of [llama-benchy](https://github.com/eugr/llama-benchy), a `llama-bench`
style benchmarking tool for all OpenAI-compatible LLM backends. It benchmarks
`/v1/chat/completions` endpoints and generates statistics similar to
`llama-bench`: prompt processing (pp) and token generation (tg) speeds at
different context depths, peak throughput, TTFR, estimated prompt processing
time, end-to-end TTFT, and per-request throughput under concurrency.

## Features

- Measures pp/tg speeds at different context depths.
- Optional prefix-caching benchmarking (`--enable-prefix-caching`): context
  prefill phase (`ctx_pp` / `ctx_tg`) followed by inference over cached context.
- Reports TTFR, est_ppt and e2e_ttft.
- Multiple pp / tg / depth / concurrency combinations in one run.
- Multiple runs per test with mean ± std.
- GPT-2 byte-level BPE tokenizer for prompt construction (approximation of
  the model tokenizer, same fallback strategy as the Python version).
- Correctly handles multi-token prediction (MTP) chunks via `token_ids`
  (with local-tokenization fallback).
- Downloads a Project Gutenberg book as prompt source text (cached in
  `~/.cache/llama-benchy/`).
- Configurable latency measurement (`api` / `generation` / `none`).
- Coherence test after warmup (skippable).
- Auto-detects the HF model name from the endpoint's `/models` endpoint.
- Saves results to Markdown, JSON or CSV; optional throughput time series.
- `--exit-on-first-fail` / `--no-results-on-fail` controls.

## Build

Requires Go 1.21+ (tested with Go 1.25).

```bash
cd llm-benchy
go build -o llm-benchy .
```

The version can be overridden at build time:

```bash
go build -ldflags "-X main.Version=0.1.0" -o llm-benchy .
```

## Usage

```bash
./llm-benchy --base-url <ENDPOINT_URL> --model <HF/NAMESPACE/MODEL> --pp 2048 --tg 32
```

Example:

```bash
./llm-benchy \
  --base-url http://spark:8888/v1 \
  --model openai/gpt-oss-120b \
  --depth 0 4096 8192 \
  --latency-mode generation
```

List style flags accept repeated or comma-separated values:

```bash
./llm-benchy --base-url http://localhost:8000/v1 --model org/model \
  --pp 128 256 --tg 32 64 --depth 0 1024 --concurrency 1 2
```

### Arguments

- `--base-url`: OpenAI compatible endpoint URL (required).
- `--api-key`: API key (default: `EMPTY`).
- `--model`: Model name (HF format). Auto-detected from `/models` if omitted.
- `--served-model-name`: Model name used in API calls (defaults to `--model`).
- `--tokenizer`: Hint for the tokenizer (the Go port uses gpt2 BPE as an approximation).
- `--pp`: Prompt processing token counts (default: `2048`).
- `--tg`: Token generation counts (default: `32`).
- `--depth`: Context depths (default: `0`).
- `--runs`: Number of runs per test (default: `3`).
- `--no-cache`: Add random UUID noise to prompts and send `cache_prompt=false`.
- `--post-run-cmd`: Shell command executed after each run.
- `--book-url`: Book URL for prompt text (default: Sherlock Holmes).
- `--latency-mode`: `api` (default) | `generation` | `none`.
- `--no-warmup`: Skip warmup phase.
- `--skip-coherence`: Skip coherence test after warmup.
- `--adapt-prompt` / `--no-adapt-prompt`: Adapt prompt size based on warmup delta (default: on).
- `--enable-prefix-caching`: Two-phase prefix-caching benchmarking.
- `--concurrency`: Concurrency levels (default: `1`).
- `--save-result`: Output file.
- `--format`: `md` (default) | `json` | `csv`.
- `--save-total-throughput-timeseries`: Save 1s-window total throughput series (JSON).
- `--save-all-throughput-timeseries`: Save per-request throughput series (JSON).
- `--exit-on-first-fail`: Stop on first failed test, exit non-zero.
- `--no-results-on-fail`: Discard results on failure (implies `--exit-on-first-fail`).
- `--version`: Print version.

### Differences from the Python version

- Tokenizer: the Python original loads the exact HuggingFace transformers
  tokenizer for the model under test. The Go port uses the GPT-2 byte-level
  BPE tokenizer (the same approximation the Python version falls back to)
  for prompt construction and as a fallback when the server does not return
  `token_ids`.
- CLI list flags accept values as `--pp 128 256` or `--pp 128,256`.
- Single static binary, no virtual environment required.

## Project layout

```
main.go                  CLI entry point
version.go               version (overridable via -ldflags)
internal/config          flag parsing + /models auto-detection
internal/corpus          book download, caching, tokenization
internal/prompts         (context, prompt) generation from corpus
internal/client          OpenAI-compatible streaming client
internal/runner          benchmark suite orchestration
internal/results         aggregation + md/json/csv reporting
```
