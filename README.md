# llm-benchy

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
- Embeds a Project Gutenberg book (The Adventures of Sherlock Holmes, #1661)
  in the binary as prompt source text, so the default run needs no network
  access; `--book-url` downloads a custom book instead (cached in
  `~/.cache/llm-benchy/`).
- Configurable latency measurement (`api` / `generation` / `none`).
- Coherence test after warmup (skippable).
- No external network dependencies (the model name is user-supplied and used
  as-is, no HuggingFace validation); when `--model` is omitted the endpoint's
  `/models` list is shown for selection.
- Saves results to Markdown, JSON or CSV; optional throughput time series.
- `--exit-on-first-fail` / `--no-results-on-fail` controls.

## Build

Requires Go 1.21+ (tested with Go 1.25).

```bash
cd llm-benchy
go build -o llm-benchy .
```

### Using the Makefile

The Makefile wraps the common workflows:

```bash
make            # build ./llm-benchy (stripped binary)
make build      # same as above
make VERSION=1.2.3 build  # override the version at build time
make run ARGS="--base-url http://localhost:8000/v1 --model org/model --pp 128 --tg 32"
make check      # go vet + build + go test ./...
make test       # go test ./...
make vet        # go vet ./...
make fmt        # gofmt all sources
make tidy       # go mod tidy
make install    # go install into $GOPATH/bin
make cross      # build dist/ for linux-amd64 and darwin-arm64
make build-all  # build all release platforms into dist/
make release    # build-all + create tar.gz/zip archives in dist/
make clean      # remove the binary and dist/
```

The default version is taken from `version.go` and can be overridden with
`VERSION=<ver>` on the make command line or
`go build -ldflags "-X main.Version=0.1.0"`.

### Releasing

```bash
scripts/release.sh   # bump, tag and push vX.Y.Z
```

Pushing a `v*` tag triggers the GitHub Actions **Release** workflow, which
runs `make check`, `make build-all` and `make release` and publishes
tar.gz/zip binaries for linux (amd64/arm64), darwin (amd64/arm64) and
windows (amd64) to the
[releases page](https://github.com/Arvintian/llm-benchy/releases).
`scripts/version.sh` prints the current git/Go/binary version info.

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
- `--model`: Model name (required). Used as-is for API calls and reporting;
  no HuggingFace lookup is performed. If omitted, the models listed by
  `{base-url}/models` are printed to help you pick one.
- `--served-model-name`: Model name used in API calls (defaults to `--model`).
- `--tokenizer`: Hint for the tokenizer (the Go port uses gpt2 BPE as an approximation).
- `--pp`: User prompt token counts, one entry per test (list flag, e.g. `--pp 128 256`); default: `2048`. Note: this is the prompt *message* size, not the total tokens processed — the server sees `pp + depth` plus chat template overhead, reduced by the warmup template delta when `--adapt-prompt` is on (the default).
- `--tg`: Token generation counts, one entry per test (list flag, e.g. `--tg 32 64`); default: `32`.
- `--depth`: Context depths (previous conversation tokens), list flag; default: `0`.
- `--runs`: Number of runs per test (default: `3`).
- `--no-cache`: Add random UUID noise to prompts and send `cache_prompt=false`.
- `--post-run-cmd`: Shell command executed after each run.
- `--book-url`: Book URL to download for prompt text (default: empty — uses the embedded Sherlock Holmes).
- `--latency-mode`: `api` (default) | `generation` | `none`.
- `--no-warmup`: Skip warmup phase.
- `--skip-coherence`: Skip coherence test after warmup.
- `--adapt-prompt` / `--no-adapt-prompt`: Adapt prompt size based on warmup delta (default: on).
- `--enable-prefix-caching`: Two-phase prefix-caching benchmarking.
- `--concurrency`: Concurrency levels (concurrent requests per test), list flag; default: `1`.
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

## How it works

Each run proceeds through:

1. **Model name**: `--model` is required. The name is used as-is (no
   HuggingFace validation, so the tool also works fully offline against the
   target endpoint). If it is omitted, `{base-url}/models` is queried and the
   available models are listed so the user can pick one.
2. **Corpus preparation**: uses the book embedded in the binary (default:
   The Adventures of Sherlock Holmes, no network access) or, when
   `--book-url` is given, downloads that URL (cached by URL hash in
   `~/.cache/llm-benchy/`); the text is tokenized with the GPT-2 BPE
   tokenizer.
3. **Warmup**: small non-streaming generations measure the token delta added
   by the chat template (`delta_user` / `delta_context`); these are subtracted
   from `pp`/`depth` so the server actually processes the requested token count.
4. **Coherence test**: asks "What is the capital of France?" and expects an
   answer containing "Paris" (skippable with `--skip-coherence`).
5. **Latency measurement** (`--latency-mode`): `api` (3x `GET /models`),
   `generation` (3x single-token streamed generations) or `none`.
6. **Benchmark matrix**: for every `depth x pp x tg x concurrency` combination,
   `--runs` batches are issued concurrently (HTTP keep-alive pool sized to
   `max_concurrency + 5`, 1 hour per-request timeout, matching the Python
   original). Responses are streamed (SSE); token timestamps are taken from
   server-provided `token_ids` (multi-token chunks are evenly distributed
   over the chunk window) with local GPT-2 tokenization as a fallback.
   With `--enable-prefix-caching` and `depth > 0`, each run is split into a
   context prefill phase (`ctx_pp` / `ctx_tg`) and an inference phase.
7. **Reporting**: metrics are aggregated per configuration as mean ± std
   (population std, numpy convention):
   - `pp` speed (t/s) and, for concurrency > 1, per-request `t/s (req)`
   - `tg` speed (t/s)
   - `peak t/s`: max token count in a sliding 1-second window
   - `ttfr (ms)`: time to first response chunk
   - `est_ppt (ms)`: estimated prompt processing time (`ttfr` - endpoint latency)
   - `e2e_ttft (ms)`: time from request start to first generated token
   Results are printed to stdout and optionally saved via `--save-result`
   as Markdown (table), JSON (full report incl. metadata and timeseries) or
   CSV. Prompt tokens are taken from server-reported usage when it is within
   20% of the expected count, otherwise the expected count is used.

On `Ctrl-C` (or with `--exit-on-first-fail`) the run is interrupted but
collected results are still saved as a partial report, unless
`--no-results-on-fail` was given.

For the detailed per-phase behavior and exact metric formulas, see
[docs/BENCHMARKING.md](docs/BENCHMARKING.md).

## Project layout

```
main.go                  CLI entry point
version.go               version (overridable via -ldflags)
Makefile                 build / test / vet / cross-compile targets
docs/BENCHMARKING.md     execution flow and metric algorithms (detailed)
scripts/                 release.sh / version.sh (release tooling)
.github/workflows        CI (build & test) + auto release on v* tags
internal/config          flag parsing (+ endpoint /models listing)
internal/corpus          corpus loading (embed + download), tokenization
internal/corpus/embedded_book.txt  default corpus (Sherlock Holmes), embedded via //go:embed
internal/prompts         (context, prompt) generation from corpus
internal/client          OpenAI-compatible streaming client
internal/runner          benchmark suite orchestration
internal/results         aggregation + md/json/csv reporting
```
