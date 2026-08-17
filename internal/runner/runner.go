// Package runner orchestrates the benchmark suite.
package runner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"llm-benchy/internal/client"
	"llm-benchy/internal/config"
	"llm-benchy/internal/prompts"
	"llm-benchy/internal/results"
)

// ErrBenchmarkFailure signals that a benchmark test failed and execution
// was stopped (used with --exit-on-first-fail).
var ErrBenchmarkFailure = errors.New("benchmark failure")

// BenchmarkRunner executes the benchmark suite.
type BenchmarkRunner struct {
	Config       *config.BenchmarkConfig
	Client       *client.LLMClient
	PromptGen    *prompts.PromptGenerator
	Results      *results.BenchmarkResults
	Version      string
	DeltaUser    int
	DeltaContext int
}

// NewBenchmarkRunner creates a runner for the given config.
func NewBenchmarkRunner(cfg *config.BenchmarkConfig, c *client.LLMClient, pg *prompts.PromptGenerator, version string) *BenchmarkRunner {
	return &BenchmarkRunner{
		Config:    cfg,
		Client:    c,
		PromptGen: pg,
		Results:   results.NewBenchmarkResults(),
		Version:   version,
	}
}

func firstError(rs []*client.RequestResult) string {
	for _, r := range rs {
		if r != nil && r.Err != "" {
			return r.Err
		}
	}
	return ""
}

// setMetadata fills in the run metadata.
func (r *BenchmarkRunner) setMetadata(latency float64, maxConcurrency int) {
	if r.Results.Metadata == nil {
		r.Results.Metadata = &results.BenchmarkMetadata{
			Version:              r.Version,
			Timestamp:            time.Now().UTC().Format("2006-01-02 15:04:05Z"),
			LatencyMode:          r.Config.LatencyMode,
			LatencyMs:            latency * 1000,
			Model:                r.Config.Model,
			PrefixCachingEnabled: r.Config.EnablePrefixCaching,
			MaxConcurrency:       maxConcurrency,
		}
	}
}

// RunSuite executes the full benchmark suite.
func (r *BenchmarkRunner) RunSuite(ctx context.Context) error {
	cfg := r.Config
	maxConcurrency := 1
	for _, c := range cfg.ConcurrencyLevels {
		if c > maxConcurrency {
			maxConcurrency = c
		}
	}

	// Match Python's aiohttp.TCPConnector(limit=max_concurrency+5, keepalive_timeout=600)
	r.Client.HTTPClient.Transport = &http.Transport{
		MaxIdleConnsPerHost: maxConcurrency + 5,
		IdleConnTimeout:     600 * time.Second,
	}

	latency := 0.0 // default in case of early interrupt

	// Warmup
	shouldWarmup := !cfg.NoWarmup
	if cfg.AdaptPrompt {
		shouldWarmup = true
	}
	if shouldWarmup {
		du, dc, err := r.Client.Warmup(r.PromptGen.Corpus)
		if err != nil {
			fmt.Printf("Warmup failed: %v\n", err)
			os.Exit(1)
		}
		r.DeltaUser = du
		r.DeltaContext = dc
	}

	// Coherence test after warmup (by default, unless skipped)
	if !cfg.SkipCoherence {
		if !r.Client.RunCoherenceTest() {
			fmt.Println("\nBenchmark failed due to coherence test failure.")
			os.Exit(1)
		}
	} else {
		fmt.Println("\nSkipping coherence test (--skip-coherence specified)")
	}

	// Measure latency
	latency = r.Client.MeasureLatency(cfg.LatencyMode)

	// Main loop
	err := r.runMainLoop(ctx, latency)

	if err == nil {
		r.setMetadata(latency, maxConcurrency)
		r.Results.SaveReport(cfg.SaveResult, cfg.ResultFormat, maxConcurrency)
		return nil
	}

	// Interrupted or failed: possibly save partial results
	if len(r.Results.Runs) > 0 {
		shouldSave := true
		if errors.Is(err, ErrBenchmarkFailure) && cfg.NoResultsOnFail {
			shouldSave = false
			fmt.Println("\n[Failed] Results discarded per --no-results-on-fail.")
		}
		if shouldSave {
			fmt.Println("\n[Interrupted/Failed] Saving partial results...")
			r.setMetadata(latency, maxConcurrency)
			r.Results.SaveReport(cfg.SaveResult, cfg.ResultFormat, maxConcurrency)
		}
	}

	if errors.Is(err, ErrBenchmarkFailure) {
		os.Exit(1)
	}
	return err
}

func (r *BenchmarkRunner) runMainLoop(ctx context.Context, latency float64) error {
	cfg := r.Config
	tokenizerCorpus := r.PromptGen.Corpus

	// gather runs f(0..concurrency-1) concurrently, preserving order
	gather := func(concurrency int, f func(i int, reqCtx context.Context) *client.RequestResult) []*client.RequestResult {
		out := make([]*client.RequestResult, concurrency)
		var wg sync.WaitGroup
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				reqCtx, cancel := context.WithTimeout(ctx, 3600*time.Second) // matches aiohttp total timeout
				defer cancel()
				out[i] = f(i, reqCtx)
			}(i)
		}
		wg.Wait()
		return out
	}

	for _, depth := range cfg.Depths {
		for _, pp := range cfg.PPCounts {
			for _, tg := range cfg.TGCounts {
				for _, concurrency := range cfg.ConcurrencyLevels {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Printf("Running test: pp=%d, tg=%d, depth=%d, concurrency=%d\n",
						pp, tg, depth, concurrency)

					runStdResults := make([][]*client.RequestResult, 0, cfg.NumRuns)
					runCtxResults := make([][]*client.RequestResult, 0, cfg.NumRuns)
					expectedPP := pp
					expectedCtx := depth

					for run := 0; run < cfg.NumRuns; run++ {
						if ctx.Err() != nil {
							return ctx.Err()
						}

						// Adapt prompt tokens
						currentPP := pp
						currentDepth := depth
						if cfg.AdaptPrompt {
							if depth == 0 {
								currentPP = max(1, pp-r.DeltaUser)
							} else {
								currentDepth = max(1, depth-r.DeltaContext)
							}
						}
						expectedPP = currentPP
						expectedCtx = currentDepth

						promptBatch := r.PromptGen.GenerateBatch(concurrency, currentPP, currentDepth, cfg.NoCache)

						if cfg.EnablePrefixCaching && depth > 0 {
							// Phase 1: Context Load
							fmt.Printf("  Run %d/%d (Context Load, batch size %d)...\n",
								run+1, cfg.NumRuns, concurrency)
							loadResults := gather(concurrency, func(i int, reqCtx context.Context) *client.RequestResult {
								pb := promptBatch[i]
								contextText := pb[0]
								return r.Client.RunGeneration(reqCtx, contextText, "", tg, cfg.NoCache, tokenizerCorpus)
							})
							runCtxResults = append(runCtxResults, loadResults)

							if cfg.ExitOnFirstFail {
								if e := firstError(loadResults); e != "" {
									fmt.Printf("\n[Error] Stopping due to error in context load: %s\n", e)
									return ErrBenchmarkFailure
								}
							}

							// Phase 2: Inference
							fmt.Printf("  Run %d/%d (Inference, batch size %d)...\n",
								run+1, cfg.NumRuns, concurrency)
							infResults := gather(concurrency, func(i int, reqCtx context.Context) *client.RequestResult {
								pb := promptBatch[i]
								contextText, promptText := pb[0], pb[1]
								return r.Client.RunGeneration(reqCtx, contextText, promptText, tg, cfg.NoCache, tokenizerCorpus)
							})
							runStdResults = append(runStdResults, infResults)

							if cfg.ExitOnFirstFail {
								if e := firstError(infResults); e != "" {
									fmt.Printf("\n[Error] Stopping due to error in inference: %s\n", e)
									return ErrBenchmarkFailure
								}
							}
						} else {
							// Standard Run
							fmt.Printf("  Run %d/%d (batch size %d)...\n",
								run+1, cfg.NumRuns, concurrency)
							batchResults := gather(concurrency, func(i int, reqCtx context.Context) *client.RequestResult {
								pb := promptBatch[i]
								contextText, promptText := pb[0], pb[1]
								return r.Client.RunGeneration(reqCtx, contextText, promptText, tg, cfg.NoCache, tokenizerCorpus)
							})
							runStdResults = append(runStdResults, batchResults)

							if cfg.ExitOnFirstFail {
								if e := firstError(batchResults); e != "" {
									fmt.Printf("\n[Error] Stopping due to error in standard run: %s\n", e)
									return ErrBenchmarkFailure
								}
							}
						}

						// Post Run Command
						if cfg.PostRunCmd != "" {
							if err := exec.Command("sh", "-c", cfg.PostRunCmd).Run(); err != nil {
								fmt.Printf("Post-run command failed: %v\n", err)
							}
						}
					}

					// Aggregate and record
					if cfg.EnablePrefixCaching && depth > 0 {
						r.Results.Add(cfg.Model, pp, tg, depth, concurrency, runCtxResults, latency, expectedCtx, true,
							cfg.SaveTotalThroughputTSeries, cfg.SaveAllThroughputTSeries)
						r.Results.Add(cfg.Model, pp, tg, depth, concurrency, runStdResults, latency, expectedPP, false,
							cfg.SaveTotalThroughputTSeries, cfg.SaveAllThroughputTSeries)
					} else {
						// Standard run expected tokens = pp + depth (usually depth=0 or concatenated)
						r.Results.Add(cfg.Model, pp, tg, depth, concurrency, runStdResults, latency, expectedPP+expectedCtx, false,
							cfg.SaveTotalThroughputTSeries, cfg.SaveAllThroughputTSeries)
					}
				}
			}
		}
	}
	return nil
}
