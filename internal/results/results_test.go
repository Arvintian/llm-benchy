package results

import (
	"math"
	"strings"
	"testing"

	"github.com/Arvintian/llm-benchy/internal/client"
)

func TestMDReportColumnsAlign(t *testing.T) {
	r := NewBenchmarkResults()
	r.ModelName = "qwen2.5:7b-instruct"
	r.Runs = append(r.Runs,
		&BenchmarkRun{Concurrency: 2, ContextSize: 4096, PromptSize: 32, ResponseSize: 128,
			PPThroughput:    &BenchmarkMetric{Mean: 12345.678, Std: 123.456},
			PPReqThroughput: &BenchmarkMetric{Mean: 6172.83, Std: 61.72},
			TTFR:            &BenchmarkMetric{Mean: 12.34, Std: 0.56},
			EstPPT:          &BenchmarkMetric{Mean: 9.87, Std: 0.12},
			E2ETTFT:         &BenchmarkMetric{Mean: 20.11, Std: 0.34},
		},
		&BenchmarkRun{Concurrency: 2, ContextSize: 4096, PromptSize: 32, ResponseSize: 128,
			TGThroughput:      &BenchmarkMetric{Mean: 345.67, Std: 1.23},
			TGReqThroughput:   &BenchmarkMetric{Mean: 172.84, Std: 0.61},
			PeakThroughput:    &BenchmarkMetric{Mean: 412.34, Std: 5.67},
			PeakReqThroughput: &BenchmarkMetric{Mean: 206.17, Std: 2.83},
		},
	)
	r.Metadata = &BenchmarkMetadata{MaxConcurrency: 4}

	lines := strings.Split(r.generateMDReport(4), "\n")
	if len(lines) < 4 {
		t.Fatalf("expected at least 4 lines, got %d", len(lines))
	}

	// Every row must occupy the same number of display columns so that
	// the "|" pipes line up in a monospace terminal.
	want := displayWidth(lines[0])
	for i, line := range lines {
		if got := displayWidth(line); got != want {
			t.Errorf("line %d display width %d, want %d:\n%s", i, got, want, line)
		}
	}
}

// TestEstPPTUsesFirstTokenChunk guards against backends that flush the
// response headers plus an empty "role" delta chunk before prefill starts:
// est_ppt (and therefore the whole pp row) must be derived from the first
// *generated* token, not from the first raw chunk.
func TestEstPPTUsesFirstTokenChunk(t *testing.T) {
	f := func(v float64) *float64 { return &v }

	start := 100.0
	res := &client.RequestResult{
		StartTs:         start,
		EndTs:           start + 1.5,
		FirstResponseTs: f(start + 0.002), // headers + empty role delta chunk
		FirstTokenTs:    f(start + 0.500), // first real token after prefill
		PromptTokens:    2048,
		TotalTokens:     32,
		TokenTimestamps: []float64{start + 0.5, start + 1.5},
	}

	r := NewBenchmarkResults()
	r.Add("model", 2048, 32, 0, 1, [][]*client.RequestResult{{res}}, 0.005, 2048, false, false, false)

	run := r.Runs[0]
	if run.PPThroughput == nil {
		t.Fatal("pp throughput missing: est_ppt must not collapse to 0 for pre-flushed role chunks")
	}
	checks := []struct {
		name      string
		metric    *BenchmarkMetric
		mean      float64
		tolerance float64
	}{
		{"ttfr", run.TTFR, 2.0, 0.01}, // ms, first raw chunk
		{"e2e_ttft", run.E2ETTFT, 500.0, 0.01},
		{"est_ppt", run.EstPPT, 495.0, 0.01}, // e2e_ttft - latency
		{"pp", run.PPThroughput, 2048 / 0.495, 0.01},
	}
	for _, c := range checks {
		if c.metric == nil {
			t.Fatalf("%s metric is nil", c.name)
		}
		if math.Abs(c.metric.Mean-c.mean) > c.tolerance {
			t.Errorf("%s = %.4f, want %.4f", c.name, c.metric.Mean, c.mean)
		}
	}
}

func TestDisplayWidth(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"t/s", 3},
		{"12.34 ± 0.56", 12},
		{"模型", 4},
		{"a±b中文", 7},
	}
	for _, c := range cases {
		if got := displayWidth(c.in); got != c.want {
			t.Errorf("displayWidth(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
