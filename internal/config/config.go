// Package config provides command line parsing and endpoint model
// auto-detection for llm-benchy.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"flag"
)

const defaultBookURL = "https://www.gutenberg.org/files/1661/1661-0.txt"

// ErrVersionShown is returned when --version was requested (clean exit).
var ErrVersionShown = errors.New("version shown")

// BenchmarkConfig holds all configuration for a benchmark run.
type BenchmarkConfig struct {
	BaseURL                    string
	APIKey                     string
	Model                      string
	ServedModelName            string
	Tokenizer                  string
	PPCounts                   []int
	TGCounts                   []int
	Depths                     []int
	NumRuns                    int
	NoCache                    bool
	LatencyMode                string
	NoWarmup                   bool
	SkipCoherence              bool
	AdaptPrompt                bool
	EnablePrefixCaching        bool
	BookURL                    string
	PostRunCmd                 string
	ConcurrencyLevels          []int
	SaveResult                 string
	ResultFormat               string
	SaveTotalThroughputTSeries bool
	SaveAllThroughputTSeries   bool
	ExitOnFirstFail            bool
	NoResultsOnFail            bool
}

var hfModelPattern = regexp.MustCompile(`^[^/]+/[^/]+$`)

// multiIntFlag is a flag value that accepts one or more comma/space
// separated integers per occurrence and appends them to a list
// (e.g. --pp 2048 --pp 4096,8192).
type multiIntFlag struct {
	values *[]int
}

func (m *multiIntFlag) String() string {
	if m.values == nil {
		return ""
	}
	parts := make([]string, len(*m.values))
	for i, v := range *m.values {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}

func (m *multiIntFlag) Set(s string) error {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return errors.New("no values provided")
	}
	for _, f := range fields {
		v, err := strconv.Atoi(f)
		if err != nil {
			return fmt.Errorf("invalid integer %q: %w", f, err)
		}
		*m.values = append(*m.values, v)
	}
	return nil
}

// FromFlags parses command line arguments into a BenchmarkConfig.
func FromFlags(args []string, version string) (*BenchmarkConfig, error) {
	fs := flag.NewFlagSet("llm-benchy", flag.ExitOnError)

	baseURL := fs.String("base-url", "", "OpenAI compatible endpoint URL (required)")
	apiKey := fs.String("api-key", "EMPTY", "API Key for the endpoint")
	model := fs.String("model", "", "Model name to use for benchmarking (auto-detected from endpoint if not specified)")
	servedModelName := fs.String("served-model-name", "", "Model name used in API calls (defaults to --model if not specified)")
	tokenizer := fs.String("tokenizer", "", "Tokenizer to use (defaults to gpt2 BPE approximation)")

	var pp, tg, depth, concurrency []int
	fs.Var(&multiIntFlag{values: &pp}, "pp", "List of prompt processing token counts - default: 2048")
	fs.Var(&multiIntFlag{values: &tg}, "tg", "List of token generation counts - default: 32")
	fs.Var(&multiIntFlag{values: &depth}, "depth", "List of context depths (previous conversation tokens) - default: 0")
	fs.Var(&multiIntFlag{values: &concurrency}, "concurrency", "List of concurrency levels (number of concurrent requests per test) - default: [1]")

	numRuns := fs.Int("runs", 3, "Number of runs per test - default: 3")
	noCache := fs.Bool("no-cache", false, "Ensure unique requests to avoid prefix caching and send cache_prompt=false to the server")
	postRunCmd := fs.String("post-run-cmd", "", "Command to execute after each test run")
	bookURL := fs.String("book-url", defaultBookURL, "URL of a book to use for text generation, defaults to Sherlock Holmes")
	latencyMode := fs.String("latency-mode", "api", "Method to measure latency: 'api' (list models) - default, 'generation' (single token generation), or 'none' (skip latency measurement)")
	noWarmup := fs.Bool("no-warmup", false, "Skip warmup phase")
	skipCoherence := fs.Bool("skip-coherence", false, "Skip coherence test after warmup")
	adaptPrompt := fs.Bool("adapt-prompt", true, "Adapt prompt size based on warmup token usage delta (default: true)")
	noAdaptPrompt := fs.Bool("no-adapt-prompt", false, "Disable prompt size adaptation")
	enablePrefixCaching := fs.Bool("enable-prefix-caching", false, "Enable prefix caching performance measurement")
	saveResult := fs.String("save-result", "", "File to save results to")
	format := fs.String("format", "md", "Output format: md, json, csv")
	saveTotalTS := fs.Bool("save-total-throughput-timeseries", false, "Save calculated TOTAL throughput for each 1 second window inside peak throughput calculation during the run.")
	saveAllTS := fs.Bool("save-all-throughput-timeseries", false, "Save calculated throughput timeseries for EACH individual request.")
	exitOnFirstFail := fs.Bool("exit-on-first-fail", false, "Stop execution on first failed test and exit with non-zero status")
	noResultsOnFail := fs.Bool("no-results-on-fail", false, "Prevent saving/printing results when error is experienced, turns on --exit-on-first-fail as well")
	showVersion := fs.Bool("version", false, "Print version and exit")

	// Support Python-argparse style list flags: --pp 128 256
	rewriteListArgs := func(args []string, listFlags map[string]bool) []string {
		out := make([]string, 0, len(args))
		for i := 0; i < len(args); i++ {
			a := args[i]
			name := strings.TrimLeft(a, "-")
			if eq := strings.IndexByte(a, '='); eq != -1 {
				name = name[:eq]
			}
			if listFlags[name] && !strings.Contains(a, "=") {
				var vals []string
				for i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
					vals = append(vals, args[i+1])
					i++
				}
				if len(vals) > 0 {
					out = append(out, a+"="+strings.Join(vals, ","))
				} else {
					out = append(out, a)
				}
			} else {
				out = append(out, a)
			}
		}
		return out
	}
	args = rewriteListArgs(args, map[string]bool{"pp": true, "tg": true, "depth": true, "concurrency": true})

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if *showVersion {
		fmt.Printf("%s %s\n", fs.Name(), version)
		// Returning a nil config with nil error signals clean exit.
		return nil, ErrVersionShown
	}

	if *baseURL == "" {
		return nil, errors.New("required flag --base-url must be specified")
	}
	switch *latencyMode {
	case "api", "generation", "none":
	default:
		return nil, fmt.Errorf("invalid --latency-mode %q (must be api, generation or none)", *latencyMode)
	}
	switch *format {
	case "md", "json", "csv":
	default:
		return nil, fmt.Errorf("invalid --format %q (must be md, json or csv)", *format)
	}

	if len(pp) == 0 {
		pp = []int{2048}
	}
	if len(tg) == 0 {
		tg = []int{32}
	}
	if len(depth) == 0 {
		depth = []int{0}
	}
	if len(concurrency) == 0 {
		concurrency = []int{1}
	}

	if *noResultsOnFail {
		*exitOnFirstFail = true
	}

	cfg := &BenchmarkConfig{
		BaseURL:                    *baseURL,
		APIKey:                     *apiKey,
		Model:                      *model,
		ServedModelName:            *servedModelName,
		Tokenizer:                  *tokenizer,
		PPCounts:                   pp,
		TGCounts:                   tg,
		Depths:                     depth,
		NumRuns:                    *numRuns,
		NoCache:                    *noCache,
		LatencyMode:                *latencyMode,
		NoWarmup:                   *noWarmup,
		SkipCoherence:              *skipCoherence,
		AdaptPrompt:                *adaptPrompt && !*noAdaptPrompt,
		EnablePrefixCaching:        *enablePrefixCaching,
		BookURL:                    *bookURL,
		PostRunCmd:                 *postRunCmd,
		ConcurrencyLevels:          concurrency,
		SaveResult:                 *saveResult,
		ResultFormat:               *format,
		SaveTotalThroughputTSeries: *saveTotalTS,
		SaveAllThroughputTSeries:   *saveAllTS,
		ExitOnFirstFail:            *exitOnFirstFail,
		NoResultsOnFail:            *noResultsOnFail,
	}

	// Auto-detect model if not specified
	if cfg.Model == "" {
		fmt.Println("No model specified, attempting to auto-detect from endpoint...")
		hfModel, servedModel, err := DetectHFModelFromEndpoint(cfg.BaseURL, cfg.APIKey)
		if err != nil {
			return nil, err
		}
		cfg.Model = hfModel
		if cfg.ServedModelName != "" {
			// explicit --served-model-name wins
		} else {
			cfg.ServedModelName = servedModel
		}
		fmt.Printf("Auto-detected HF model: %s (served as: %s)\n", cfg.Model, cfg.ServedModelName)
	} else if cfg.ServedModelName == "" {
		cfg.ServedModelName = cfg.Model
	}

	return cfg, nil
}

type modelInfo struct {
	ID    string `json:"id"`
	Root  string `json:"root"`
	Model string `json:"model"`
}

type dataArrayResponse struct {
	Data []modelInfo `json:"data"`
}

type modelsArrayResponse struct {
	Models []modelInfo `json:"models"`
}

// DetectHFModelFromEndpoint fetches models from {baseURL}/models and
// identifies the HF model name.
//
// Returns a tuple of (hf_model_name, served_model_name).
func DetectHFModelFromEndpoint(baseURL, apiKey string) (string, string, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return "", "", err
	}
	if apiKey != "" && apiKey != "EMPTY" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	response, err := client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf(
			"Unable to connect to %s/models endpoint: %s\nPlease specify --model explicitly.",
			baseURL, err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", fmt.Errorf(
			"Unable to connect to %s/models endpoint: %s\nPlease specify --model explicitly.",
			baseURL, err)
	}

	var dataResponse dataArrayResponse
	var modelsResponse modelsArrayResponse
	if err := json.Unmarshal(body, &dataResponse); err != nil {
		return "", "", fmt.Errorf(
			"Unable to parse response from %s/models endpoint: %s\nPlease specify --model explicitly.",
			baseURL, err)
	}
	_ = json.Unmarshal(body, &modelsResponse)

	var hfFormatted [][2]string // (hf_name, served_name)
	var nonHFFormatted []string

	// Parse data array first
	if dataResponse.Data != nil {
		for _, m := range dataResponse.Data {
			if m.Root != "" && hfModelPattern.MatchString(m.Root) {
				hfFormatted = append(hfFormatted, [2]string{m.Root, m.ID})
			} else if hfModelPattern.MatchString(m.ID) {
				hfFormatted = append(hfFormatted, [2]string{m.ID, m.ID})
			} else {
				nonHFFormatted = append(nonHFFormatted, m.ID)
			}
		}
	}
	// Parse models array as a fallback; only if "data" was not present.
	if dataResponse.Data == nil && modelsResponse.Models != nil {
		for _, m := range modelsResponse.Models {
			name := m.Model
			if name == "" {
				name = m.ID
			}
			if hfModelPattern.MatchString(name) {
				hfFormatted = append(hfFormatted, [2]string{name, name})
			} else {
				nonHFFormatted = append(nonHFFormatted, name)
			}
		}
	}

	// Guard: multiple models available - cannot determine which one to use
	if len(hfFormatted)+len(nonHFFormatted) > 1 {
		var b strings.Builder
		b.WriteString("Multiple models available at the endpoint. Please specify --model explicitly.\n\n")
		if len(hfFormatted) > 0 {
			b.WriteString("Models with HF format:\n")
			for _, m := range hfFormatted {
				b.WriteString(fmt.Sprintf("  - %s\n", m[0]))
			}
		}
		if len(nonHFFormatted) > 0 {
			b.WriteString("Models without HF format:\n")
			for _, m := range nonHFFormatted {
				b.WriteString(fmt.Sprintf("  - %s\n", m))
			}
		}
		b.WriteString("\nPlease specify --model explicitly with the model name you want to test.")
		return "", "", errors.New(b.String())
	}

	// No models found
	if len(hfFormatted) == 0 && len(nonHFFormatted) == 0 {
		return "", "", errors.New("No models found at the endpoint.\nPlease specify --model explicitly.")
	}

	// Single non-HF model found
	if len(hfFormatted) == 0 {
		return "", "", fmt.Errorf(
			"Model '%s' is not in HF format (namespace/model).\nPlease specify --model explicitly with a valid HF model name.",
			nonHFFormatted[0])
	}

	// Single HF-formatted model found - validate against HF Hub
	hfName, servedName := hfFormatted[0][0], hfFormatted[0][1]
	hfClient := &http.Client{Timeout: 3 * time.Second}
	hfResp, err := hfClient.Get("https://huggingface.co/api/models/" + hfName)
	if err == nil {
		status := hfResp.StatusCode
		hfResp.Body.Close()
		if status == 200 || status == 401 {
			return hfName, servedName, nil
		}
	}

	return "", "", fmt.Errorf(
		"Model '%s' is not a valid HuggingFace model.\nPlease specify --model explicitly with a valid HF model name.",
		hfName)
}
