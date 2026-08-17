// Command llm-benchy is a Go port of llama-benchy: a llama-bench style
// benchmarking tool for all OpenAI-compatible LLM backends.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"llm-benchy/internal/client"
	"llm-benchy/internal/config"
	"llm-benchy/internal/corpus"
	"llm-benchy/internal/prompts"
	"llm-benchy/internal/runner"
)

func main() {
	// 1. Parse configuration
	cfg, err := config.FromFlags(os.Args[1:], Version)
	if err != nil {
		if errors.Is(err, config.ErrVersionShown) {
			return
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	// 2. Print header
	currentTime := time.Now().Format("2006-01-02 15:04:05")
	fmt.Printf("llm-benchy (%s)\n", Version)
	fmt.Printf("Date: %s\n", currentTime)
	fmt.Printf("Benchmarking model: %s at %s\n", cfg.Model, cfg.BaseURL)
	fmt.Printf("Concurrency levels: %v\n", cfg.ConcurrencyLevels)

	// 3. Prepare data
	tokenizedCorpus := corpus.NewTokenizedCorpus(cfg.BookURL, cfg.Tokenizer, cfg.Model)
	fmt.Printf("Total tokens available in text corpus: %d\n", tokenizedCorpus.Len())

	// 4. Initialize components
	promptGen := prompts.NewPromptGenerator(tokenizedCorpus)
	llmClient := client.NewLLMClient(cfg.BaseURL, cfg.APIKey, cfg.ServedModelName)
	benchRunner := runner.NewBenchmarkRunner(cfg, llmClient, promptGen, Version)

	// 5. Run benchmark suite
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := benchRunner.RunSuite(ctx); err != nil {
		os.Exit(1)
	}

	fmt.Printf("\nllm-benchy (%s)\n", Version)
	fmt.Printf("date: %s | latency mode: %s\n", currentTime, cfg.LatencyMode)
}
