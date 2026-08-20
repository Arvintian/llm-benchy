package results

import (
	"strings"
	"testing"
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
