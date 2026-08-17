# Benchmarking: execution flow and metric algorithms

This document describes in detail how `llm-benchy` runs a benchmark and how
every reported number is computed. The short version lives in the
[README](../README.md#how-it-works).

## Execution flow

1. **Config parsing**: list flags (`--pp`, `--tg`, `--depth`, `--concurrency`)
   accept repeated or comma-separated values. `--model` is required; if
   omitted, `{base-url}/models` is queried (best effort) and the available
   model ids are printed, then the run aborts with exit code 1.
2. **Corpus loading**: the embedded book (or a `--book-url` download, cached
   by URL MD5) is tokenized with GPT-2 BPE into a flat token pool used by
   the prompt generator.
3. **HTTP pool setup**: the client's connection pool is sized to
   `MaxIdleConnsPerHost = max_concurrency + 5` with a 600 s idle timeout,
   mirroring the Python original's `aiohttp.TCPConnector(limit=...,
   keepalive_timeout=600)`.
4. **Warmup** (unless `--no-warmup`; forced on when `--adapt-prompt`, the
   default): two non-streaming probes with `max_tokens=1` using the text
   `"Warmup "` × 10:
   - *user only*: `delta_user = server_prompt_tokens − local_gpt2_tokens`
   - *system + text, empty user*: `delta_context = server − local`
     (falls back to `delta_user` if no usage stats).

   These deltas capture the chat-template overhead. If the server reports no
   usage stats, both deltas are 0. Warmup also loads the model into memory
   before measurement starts.
5. **Coherence test** (unless `--skip-coherence`): non-streaming request
   "What is the capital of France? Please reply with one word only"
   (`max_tokens=100`). Passes when `content` + `reasoning` (lowercased)
   contains "paris"; otherwise the run aborts (exit 1).
6. **Latency calibration** (`--latency-mode`, 3 samples, average taken):
   - `api`: full round trip of `GET /models`
   - `generation`: time until the *first* SSE chunk of a streamed
     `max_tokens=1` "hello" request
   - `none`: assumed 0 ms.
7. **Benchmark matrix**: nested loops `depth → pp → tg → concurrency`; for
   each combination `--runs` batches are executed:
   - *Adaptation* (`--adapt-prompt`, default on): before each run, if
     `depth == 0` then `pp' = max(1, pp − delta_user)`, otherwise
     `depth' = max(1, depth − delta_context)`.
   - *Prompt batch*: `concurrency` fresh random (context, prompt) pairs are
     drawn from the corpus with the (adapted) sizes. `--no-cache` appends a
     random UUID to every prompt and sends `cache_prompt=false`.
   - *Standard run*: one batch of `concurrency` concurrent streaming
     requests — `system=context`, `user=prompt`, `max_tokens=tg`,
     `stream=true`, `return_token_ids=true`, `stream_options.include_usage=
     true`. Each request has a 3600 s timeout. After every run,
     `--post-run-cmd` is executed (`sh -c`).
   - *Prefix caching* (`--enable-prefix-caching` with `depth > 0`): each run
     has two phases over the *same* prompts:
     1. **Context Load** — `system=context`, empty `user` message,
        `max_tokens=tg`; reported as `ctx_pp`/`ctx_tg @ d{depth}` rows with
        expected prompt size `depth'`.
     2. **Inference** — the full (context, prompt) request; the shared
        prefix is now cached server-side. Reported as normal `pp`/`tg` rows.
   - *Failure handling*: `--exit-on-first-fail` stops the suite at the first
     failed request; without it, failed requests are excluded from
     aggregation and the rest of the matrix continues.
8. **Reporting**: each configuration is aggregated (see below) into one
   `pp` row and one `tg` row, printed and optionally saved (md/json/csv).
   JSON includes metadata (version, UTC timestamp, latency mode + measured
   ms, model, prefix-caching flag, max concurrency) and, with the
   timeseries flags, per-window throughput series.
9. **Interruption**: on `Ctrl-C` or failure, all results collected so far
   are saved as a partial report unless `--no-results-on-fail` was given.

## Metric algorithms

All timestamps are monotonic seconds since process start
(`time.Since(processStart)`).

### Per-request timestamps and token counting

For every streamed request the client records:

| Field | Meaning |
|---|---|
| `start_ts` | request POST issued |
| `first_response_ts` | first SSE chunk carrying `choices` |
| `first_token_ts` | first chunk with non-empty `content` or `reasoning` |
| `end_ts` | stream fully read |
| token timestamps | one per generated token, at chunk arrival |

Tokens per chunk are counted in this priority order:

1. Server-provided `token_ids` (multi-token prediction / MTP endpoints):
   `n = len(token_ids)`.
2. Otherwise the chunk text is tokenized locally with GPT-2 BPE.
3. Otherwise 1 token per chunk.

When a chunk carries `n > 1` tokens, their timestamps are spread evenly over
`(previous_token_ts, chunk_arrival]` (falling back to `start_ts` when no
earlier token exists), which approximates the unknown intra-chunk timing.

### Effective prompt token count

For each request the denominator of the pp speed uses the server-reported
`usage.prompt_tokens` when it is within 20% of the *expected* count
(adapted `pp + depth'`, or `depth'` for the context-load phase); otherwise
the expected count is used.

### Per-request metrics

With `L` = calibrated endpoint latency (s):

```
ttfr       = first_response_ts − start_ts
e2e_ttft   = first_token_ts    − start_ts
est_ppt    = max(0, ttfr      − L)      (report column: est_ppt, ms)
pp_speed   = prompt_tokens / est_ppt                    (t/s, per request)
tg_speed   = (total_tokens − 1) / (end_ts − first_token_ts)   (t/s)
```

A pure `ttft = max(0, e2e_ttft − L)` is computed internally as well but is
not part of the report columns.

### Batch metrics (concurrency > 1)

Across the `c` concurrent requests of a batch (min/max over requests):

```
batch_pp = Σ prompt_tokens / (max(first_token_ts) − min(start_ts))
batch_tg = (Σ total_tokens − c) / (max(last_token_ts) − min(first_token_ts))
```

`max(last_token_ts)` (the arrival of the final generated token) is used
instead of `max(end_ts)` to exclude protocol overhead (headers, `[DONE]`,
stream close). With `c = 1`, the report's `t/s` columns are the per-request
speeds; with `c > 1`, `t/s` shows the batch total and `t/s (req)` the
per-request average.

### Peak throughput

A sliding 1-second window is swept over *all* token timestamps of the batch
(sorted, two-pointer): `peak = max over end positions of (tokens in the
window ending there) / 1s`. If the whole batch spans less than 1 s,
`peak = token_count / total_span` instead. `peak t/s (req)` is the same
computation restricted to each single request's timestamps.

### Aggregation

For each `(depth, pp, tg, concurrency)` configuration, the per-batch values
(`--runs` of them) are summarized as `mean ± std`, where std is the
*population* standard deviation (`sqrt(Σ(v−mean)² / n)`, numpy default).
Report row names: `pp{n}` / `tg{n}`, suffixed ` @ d{depth}` when depth > 0
and ` (c{k})` when concurrency > 1; prefix-caching rows are
`ctx_pp @ d{depth}` / `ctx_tg @ d{depth}`. Rows whose metric has no samples
(e.g. nothing generated) are omitted.
