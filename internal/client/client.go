// Package client implements the OpenAI-compatible API client used to
// run benchmark generations.
package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Arvintian/llm-benchy/internal/corpus"
)

var processStart = time.Now()

// perfCounter returns monotonic seconds since process start
// (equivalent to Python's time.perf_counter usage).
func perfCounter() float64 {
	return time.Since(processStart).Seconds()
}

// RequestResult holds timing and usage data for a single request.
type RequestResult struct {
	StartTs         float64
	EndTs           float64
	FirstTokenTs    *float64
	FirstResponseTs *float64
	PromptTokens    int
	TotalTokens     int
	Err             string
	TokenTimestamps []float64
}

// LLMClient talks to an OpenAI-compatible endpoint.
type LLMClient struct {
	BaseURL    string
	APIKey     string
	ModelName  string
	Headers    http.Header
	HTTPClient *http.Client
}

var warnedAboutFallback struct {
	sync.Once
}

// NewLLMClient creates a client for the given endpoint.
func NewLLMClient(baseURL, apiKey, modelName string) *LLMClient {
	return &LLMClient{
		BaseURL:   baseURL,
		APIKey:    apiKey,
		ModelName: modelName,
		Headers: http.Header{
			"Authorization": []string{"Bearer " + apiKey},
		},
		HTTPClient: &http.Client{},
	}
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model          string    `json:"model"`
	Messages       []message `json:"messages"`
	MaxTokens      int       `json:"max_tokens"`
	Stream         *bool     `json:"stream,omitempty"`
	ReturnTokenIDs *bool     `json:"return_token_ids,omitempty"`
	CachePrompt    *bool     `json:"cache_prompt,omitempty"`
	StreamOptions  *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message *struct {
			Content          string `json:"content"`
			Reasoning        string `json:"reasoning"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
}

func (c *LLMClient) postChat(reqBody interface{}, ctx context.Context) (*http.Response, error) {
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range c.Headers {
		req.Header[k] = v
	}
	return c.HTTPClient.Do(req)
}

// MeasureLatency measures endpoint latency in seconds.
func (c *LLMClient) MeasureLatency(mode string) float64 {
	if mode == "none" {
		fmt.Println("Skipping latency measurement (assuming 0 ms).")
		return 0
	}

	fmt.Printf("Measuring latency using mode: %s...\n", mode)
	latencies := make([]float64, 0, 3)

	for i := 0; i < 3; i++ {
		start := perfCounter()
		if mode == "api" {
			req, err := http.NewRequest(http.MethodGet, c.BaseURL+"/models", nil)
			if err != nil {
				fmt.Printf("Error measuring latency: %v\n", err)
				continue
			}
			for k, v := range c.Headers {
				req.Header[k] = v
			}
			resp, err := c.HTTPClient.Do(req)
			if err != nil {
				fmt.Printf("Error measuring latency: %v\n", err)
				continue
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			latencies = append(latencies, perfCounter()-start)
		} else if mode == "generation" {
			stream := true
			reqBody := chatRequest{
				Model:     c.ModelName,
				Messages:  []message{{Role: "user", Content: "hello"}},
				MaxTokens: 1,
				Stream:    &stream,
			}
			resp, err := c.postChat(reqBody, context.Background())
			if err != nil {
				fmt.Printf("Error measuring latency: %v\n", err)
				continue
			}
			// Measure until the first chunk arrives
			buf := bufio.NewReader(resp.Body)
			if _, _, err := buf.ReadRune(); err == nil {
				latencies = append(latencies, perfCounter()-start)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	if len(latencies) > 0 {
		var sum float64
		for _, l := range latencies {
			sum += l
		}
		avg := sum / float64(len(latencies))
		fmt.Printf("Average latency (%s): %.2f ms\n", mode, avg*1000)
		return avg
	}
	return 0
}

// RunCoherenceTest verifies the model responds correctly.
func (c *LLMClient) RunCoherenceTest() bool {
	fmt.Println("\nRunning coherence test...")
	prompt := "What is the capital of France? Please reply with one word only"
	reqBody := chatRequest{
		Model:     c.ModelName,
		Messages:  []message{{Role: "user", Content: prompt}},
		MaxTokens: 100,
	}

	resp, err := c.postChat(reqBody, context.Background())
	if err != nil {
		fmt.Printf("Coherence test FAILED with error: %v\n", err)
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("Coherence test FAILED with error: %v\n", err)
		return false
	}

	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		fmt.Printf("Coherence test FAILED with error: %v\n", err)
		return false
	}
	if len(parsed.Choices) == 0 {
		fmt.Println("Coherence test FAILED: No choices in response")
		return false
	}

	content := ""
	reasoning := ""
	if msg := parsed.Choices[0].Message; msg != nil {
		content = msg.Content
		reasoning = msg.Reasoning
		if reasoning == "" {
			reasoning = msg.ReasoningContent
		}
	}
	fullContent := strings.ToLower(content + reasoning)
	if strings.Contains(fullContent, "paris") {
		fmt.Println("Coherence test PASSED.")
		return true
	}
	trunc := content
	if len(trunc) > 200 {
		trunc = trunc[:200]
	}
	fmt.Printf("Coherence test FAILED: Expected 'Paris'. Got: %s...\n", trunc)
	return false
}

// Warmup warms up the endpoint and returns (deltaUser, deltaContext).
// Returns an error if the warmup request fails (main should exit).
func (c *LLMClient) Warmup(tokenizer *corpus.TokenizedCorpus) (int, int, error) {
	fmt.Println("Warming up...")
	warmupText := "Warmup "
	warmupText = strings.Repeat(warmupText, 10)

	deltaUser := 0
	deltaContext := 0

	// 1. User only
	reqBody := chatRequest{
		Model:     c.ModelName,
		Messages:  []message{{Role: "user", Content: warmupText}},
		MaxTokens: 1,
	}
	resp, err := c.postChat(reqBody, context.Background())
	if err != nil {
		return 0, 0, fmt.Errorf("warmup failed: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return 0, 0, fmt.Errorf("warmup failed: %v", err)
	}
	if resp.StatusCode != 200 {
		return 0, 0, fmt.Errorf("warmup failed: HTTP %d: %s", resp.StatusCode, string(body))
	}
	var parsed chatResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, 0, fmt.Errorf("warmup failed: %v", err)
	}

	if tokenizer != nil {
		if parsed.Usage != nil {
			promptTokens := parsed.Usage.PromptTokens
			localTokens := len(tokenizer.Tokenizer.Encode(warmupText))
			deltaUser = promptTokens - localTokens
			fmt.Printf("Warmup (User only) complete. Delta: %d tokens (Server: %d, Local: %d)\n",
				deltaUser, promptTokens, localTokens)

			// 2. Context Only
			sysBody := chatRequest{
				Model: c.ModelName,
				Messages: []message{
					{Role: "system", Content: warmupText},
					{Role: "user", Content: ""},
				},
				MaxTokens: 1,
			}
			sysResp, err := c.postChat(sysBody, context.Background())
			if err != nil {
				return 0, 0, fmt.Errorf("warmup failed: %v", err)
			}
			sysBodyBytes, err := io.ReadAll(sysResp.Body)
			sysResp.Body.Close()
			if err != nil {
				return 0, 0, fmt.Errorf("warmup failed: %v", err)
			}
			if sysResp.StatusCode != 200 {
				return 0, 0, fmt.Errorf("warmup failed: HTTP %d: %s", sysResp.StatusCode, string(sysBodyBytes))
			}
			var sysParsed chatResponse
			if err := json.Unmarshal(sysBodyBytes, &sysParsed); err != nil {
				return 0, 0, fmt.Errorf("warmup failed: %v", err)
			}
			if sysParsed.Usage != nil {
				promptTokens = sysParsed.Usage.PromptTokens
				localTokens = len(tokenizer.Tokenizer.Encode(warmupText))
				deltaContext = promptTokens - localTokens
				fmt.Printf("Warmup (System+Empty) complete. Delta: %d tokens (Server: %d, Local: %d)\n",
					deltaContext, promptTokens, localTokens)
			} else {
				deltaContext = deltaUser
			}
		} else {
			fmt.Println("Warmup (User only) complete (no usage stats found).")
		}
	} else {
		fmt.Println("Warmup complete.")
	}

	return deltaUser, deltaContext, nil
}

type sseDelta struct {
	Content          *string `json:"content"`
	ReasoningContent *string `json:"reasoning_content"`
	Reasoning        *string `json:"reasoning"`
}

type sseChunk struct {
	Usage *struct {
		PromptTokens int `json:"prompt_tokens"`
	} `json:"usage"`
	Choices []struct {
		Delta    sseDelta `json:"delta"`
		TokenIDs []int    `json:"token_ids"`
	} `json:"choices"`
}

// RunGeneration executes a single streaming generation and records timing.
func (c *LLMClient) RunGeneration(ctx context.Context, contextText, promptText string, maxTokens int, noCache bool, tokenizer *corpus.TokenizedCorpus) *RequestResult {
	messages := make([]message, 0, 2)
	if contextText != "" {
		messages = append(messages, message{Role: "system", Content: contextText})
	}
	messages = append(messages, message{Role: "user", Content: promptText})

	result := &RequestResult{}
	stream := true
	tokenIDs := true
	reqBody := chatRequest{
		Model:          c.ModelName,
		Messages:       messages,
		MaxTokens:      maxTokens,
		Stream:         &stream,
		ReturnTokenIDs: &tokenIDs,
		StreamOptions: &struct {
			IncludeUsage bool `json:"include_usage"`
		}{IncludeUsage: true},
	}
	if noCache {
		falseVal := false
		reqBody.CachePrompt = &falseVal
	}

	resp, err := c.postChat(reqBody, ctx)
	if err != nil {
		fmt.Printf("Error during run: %v\n", err)
		result.Err = err.Error()
		return result
	}
	defer resp.Body.Close()

	result.StartTs = perfCounter()

	if resp.StatusCode != 200 {
		errText, _ := io.ReadAll(resp.Body)
		result.Err = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(errText))
		fmt.Println(result.Err)
		return result
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)

	addTokenTimestamps := func(count int, chunkTime float64) {
		result.TotalTokens += count
		if count == 1 {
			result.TokenTimestamps = append(result.TokenTimestamps, chunkTime)
			return
		}
		var lastTs float64
		hasLast := false
		if len(result.TokenTimestamps) > 0 {
			lastTs = result.TokenTimestamps[len(result.TokenTimestamps)-1]
			hasLast = true
		} else if result.FirstTokenTs != nil {
			lastTs = *result.FirstTokenTs
			hasLast = true
		}
		if !hasLast {
			lastTs = result.StartTs
		}
		timeWindow := chunkTime - lastTs
		for i := 0; i < count; i++ {
			ts := lastTs + timeWindow*float64(i+1)/float64(count)
			result.TokenTimestamps = append(result.TokenTimestamps, ts)
		}
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if line == "data: [DONE]" || line == "data:[DONE]" {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		chunkTime := perfCounter()
		jsonStr := strings.TrimSpace(line[len("data:"):])
		var chunk sseChunk
		if err := json.Unmarshal([]byte(jsonStr), &chunk); err != nil {
			continue
		}

		if chunk.Usage != nil {
			result.PromptTokens = chunk.Usage.PromptTokens
		}

		if len(chunk.Choices) > 0 {
			if result.FirstResponseTs == nil {
				result.FirstResponseTs = &chunkTime
			}

			choice := chunk.Choices[0]
			content := choice.Delta.Content
			reasoningContent := choice.Delta.ReasoningContent
			reasoning := choice.Delta.Reasoning

			// Empty strings are treated as absent (Python falsy semantics)
			hasContent := (content != nil && *content != "") ||
				(reasoningContent != nil && *reasoningContent != "") ||
				(reasoning != nil && *reasoning != "")

			if hasContent {
				if result.FirstTokenTs == nil {
					result.FirstTokenTs = &chunkTime
				}

				if len(choice.TokenIDs) > 0 {
					addTokenTimestamps(len(choice.TokenIDs), chunkTime)
				} else if tokenizer != nil {
					warnedAboutFallback.Do(func() {
						fmt.Println("  No token_ids in response, using local tokenization")
					})
					fullContent := ""
					if content != nil && *content != "" {
						fullContent = *content
					} else if reasoningContent != nil && *reasoningContent != "" {
						fullContent = *reasoningContent
					} else if reasoning != nil && *reasoning != "" {
						fullContent = *reasoning
					}
					tokenCount := len(tokenizer.Tokenizer.Encode(fullContent))
					addTokenTimestamps(tokenCount, chunkTime)
				} else {
					warnedAboutFallback.Do(func() {
						fmt.Println("  No token_ids or tokenizer, assuming 1 token per chunk")
					})
					addTokenTimestamps(1, chunkTime)
				}
			}
		}
	}

	result.EndTs = perfCounter()

	if err := scanner.Err(); err != nil {
		fmt.Printf("Error during run: %v\n", err)
		result.Err = err.Error()
	}

	return result
}
