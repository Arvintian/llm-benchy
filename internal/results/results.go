// Package results aggregates benchmark run data and renders reports
// in markdown, JSON and CSV formats.
package results

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Arvintian/llm-benchy/internal/client"
)

// TimeSeriesPoint is a [timestamp, value] pair.// TimeSeries is a list of TimeSeriesPoint (one point per window).
type TimeSeriesPoint = []float64
type TimeSeries = []TimeSeriesPoint

// BenchmarkMetric holds mean/std plus raw values.
type BenchmarkMetric struct {
	Mean   float64   `json:"mean"`
	Std    float64   `json:"std"`
	Values []float64 `json:"values"`
}

// BenchmarkMetadata describes a benchmark suite run.
type BenchmarkMetadata struct {
	Version              string  `json:"version"`
	Timestamp            string  `json:"timestamp"`
	LatencyMode          string  `json:"latency_mode"`
	LatencyMs            float64 `json:"latency_ms"`
	Model                string  `json:"model"`
	PrefixCachingEnabled bool    `json:"prefix_caching_enabled"`
	MaxConcurrency       int     `json:"max_concurrency"`
}

// BenchmarkRun holds aggregated results of one test configuration.
type BenchmarkRun struct {
	Concurrency           int  `json:"concurrency"`
	ContextSize           int  `json:"context_size"`
	PromptSize            int  `json:"prompt_size"`
	ResponseSize          int  `json:"response_size"`
	IsContextPrefillPhase bool `json:"is_context_prefill_phase"`

	PPThroughput               *BenchmarkMetric `json:"pp_throughput"`
	PPReqThroughput            *BenchmarkMetric `json:"pp_req_throughput"`
	TGThroughput               *BenchmarkMetric `json:"tg_throughput"`
	TGReqThroughput            *BenchmarkMetric `json:"tg_req_throughput"`
	PeakThroughput             *BenchmarkMetric `json:"peak_throughput"`
	PeakReqThroughput          *BenchmarkMetric `json:"peak_req_throughput"`
	TTFR                       *BenchmarkMetric `json:"ttfr"`
	EstPPT                     *BenchmarkMetric `json:"est_ppt"`
	E2ETTFT                    *BenchmarkMetric `json:"e2e_ttft"`
	ThroughputOverTime         []TimeSeries     `json:"throughput_over_time,omitempty"`
	RequestsThroughputOverTime [][]TimeSeries   `json:"requests_throughput_over_time,omitempty"`
}

// BenchmarkReport is the JSON-serializable report document.
type BenchmarkReport struct {
	BenchmarkMetadata
	Benchmarks []*BenchmarkRun `json:"benchmarks"`
}

// BenchmarkResults accumulates benchmark runs.
type BenchmarkResults struct {
	Runs      []*BenchmarkRun
	Metadata  *BenchmarkMetadata
	ModelName string
}

// NewBenchmarkResults creates an empty results collector.
func NewBenchmarkResults() *BenchmarkResults {
	return &BenchmarkResults{}
}

func calculateMetric(values []float64, multiplier float64) *BenchmarkMetric {
	if len(values) == 0 {
		return nil
	}
	scaled := make([]float64, len(values))
	var sum float64
	for i, v := range values {
		scaled[i] = v * multiplier
		sum += v
	}
	mean := sum / float64(len(values))
	var sqSum float64
	for _, v := range values {
		d := v - mean
		sqSum += d * d
	}
	std := math.Sqrt(sqSum / float64(len(values))) // population std (numpy default)
	return &BenchmarkMetric{Mean: mean * multiplier, Std: std * multiplier, Values: scaled}
}

// calculatePeakThroughput computes the peak token generation rate over a
// sliding window (1s by default), optionally returning a per-window series.
func calculatePeakThroughput(allTimestamps []float64, window float64, returnSeries bool) (float64, TimeSeries) {
	if len(allTimestamps) == 0 {
		if returnSeries {
			return 0.0, TimeSeries{}
		}
		return 0.0, nil
	}

	sorted := make([]float64, len(allTimestamps))
	copy(sorted, allTimestamps)
	sort.Float64s(sorted)

	totalDuration := sorted[len(sorted)-1] - sorted[0]
	peak := 0.0
	if totalDuration < window && totalDuration > 0 {
		peak = float64(len(sorted)) / totalDuration
		if !returnSeries {
			return peak, nil
		}
	}

	maxTokens := 0
	series := make(TimeSeries, 0, len(sorted))
	startTime := sorted[0]

	startIdx := 0
	for endIdx, endTime := range sorted {
		for startIdx < endIdx && sorted[startIdx] <= endTime-window {
			startIdx++
		}
		currentTokens := endIdx - startIdx + 1
		if currentTokens > maxTokens {
			maxTokens = currentTokens
		}
		if returnSeries {
			series = append(series, []float64{endTime - startTime, float64(currentTokens) / window})
		}
	}

	calculatedPeak := float64(maxTokens) / window
	var finalPeak float64
	if totalDuration < window && totalDuration > 0 {
		finalPeak = peak
	} else {
		finalPeak = calculatedPeak
	}

	if returnSeries {
		return finalPeak, series
	}
	return finalPeak, nil
}

// Add aggregates the results of all runs for one test configuration.
func (r *BenchmarkResults) Add(
	model string,
	pp, tg, depth, concurrency int,
	runResults [][]*client.RequestResult,
	latency float64,
	expectedPPTokens int,
	isContextPhase bool,
	saveTotalThroughputTSeries, saveAllThroughputTSeries bool,
) {
	if r.ModelName == "" {
		r.ModelName = model
	}

	var (
		aggPPSpeeds, aggTGSpeeds          []float64
		aggTTFTValues, aggTTFRTValues     []float64
		aggEstPPTValues, aggE2ETTFTValues []float64

		aggBatchPPThroughputs, aggBatchTGThroughputs []float64
		aggPeakThroughputs, aggPeakReqThroughputs    []float64

		aggThroughputSeries    []TimeSeries
		aggReqThroughputSeries [][]TimeSeries
	)

	for _, batch := range runResults {
		r.processBatch(
			batch, expectedPPTokens, latency,
			&aggPPSpeeds, &aggTGSpeeds,
			&aggTTFTValues, &aggTTFRTValues,
			&aggEstPPTValues, &aggE2ETTFTValues,
			&aggBatchPPThroughputs, &aggBatchTGThroughputs,
			&aggPeakThroughputs, &aggPeakReqThroughputs,
			saveTotalThroughputTSeries, saveAllThroughputTSeries,
			&aggThroughputSeries, &aggReqThroughputSeries,
		)
	}

	var runMetricPPThroughput, runMetricPPReqThroughput *BenchmarkMetric
	if concurrency > 1 {
		runMetricPPThroughput = calculateMetric(aggBatchPPThroughputs, 1.0)
		runMetricPPReqThroughput = calculateMetric(aggPPSpeeds, 1.0)
	} else {
		runMetricPPThroughput = calculateMetric(aggPPSpeeds, 1.0)
		runMetricPPReqThroughput = runMetricPPThroughput
	}

	var runMetricTGThroughput, runMetricTGReqThroughput *BenchmarkMetric
	if concurrency > 1 {
		runMetricTGThroughput = calculateMetric(aggBatchTGThroughputs, 1.0)
		runMetricTGReqThroughput = calculateMetric(aggTGSpeeds, 1.0)
	} else {
		runMetricTGThroughput = calculateMetric(aggTGSpeeds, 1.0)
		runMetricTGReqThroughput = runMetricTGThroughput
	}

	run := &BenchmarkRun{
		Concurrency:           concurrency,
		ContextSize:           depth,
		PromptSize:            pp,
		ResponseSize:          tg,
		IsContextPrefillPhase: isContextPhase,
		PPThroughput:          runMetricPPThroughput,
		PPReqThroughput:       runMetricPPReqThroughput,
		TGThroughput:          runMetricTGThroughput,
		TGReqThroughput:       runMetricTGReqThroughput,
		PeakThroughput:        calculateMetric(aggPeakThroughputs, 1.0),
		PeakReqThroughput:     calculateMetric(aggPeakReqThroughputs, 1.0),
		TTFR:                  calculateMetric(aggTTFRTValues, 1000),
		EstPPT:                calculateMetric(aggEstPPTValues, 1000),
		E2ETTFT:               calculateMetric(aggE2ETTFTValues, 1000),
	}
	if saveTotalThroughputTSeries {
		run.ThroughputOverTime = aggThroughputSeries
	}
	if saveAllThroughputTSeries {
		run.RequestsThroughputOverTime = aggReqThroughputSeries
	}
	r.Runs = append(r.Runs, run)
}

func (r *BenchmarkResults) processBatch(
	results []*client.RequestResult,
	expectedPPTokens int,
	latency float64,
	aggPPSpeeds, aggTGSpeeds *[]float64,
	aggTTFTValues, aggTTFRTValues *[]float64,
	aggEstPPTValues, aggE2ETTFTValues *[]float64,
	aggBatchPPThroughputs, aggBatchTGThroughputs *[]float64,
	aggPeakThroughputs, aggPeakReqThroughputs *[]float64,
	saveTotalThroughputTSeries, saveAllThroughputTSeries bool,
	aggThroughputSeries *[]TimeSeries,
	aggReqThroughputSeries *[][]TimeSeries,
) {
	var validResults []*client.RequestResult
	for _, res := range results {
		if res != nil && res.Err == "" {
			validResults = append(validResults, res)
		}
	}
	if len(validResults) == 0 {
		return
	}

	batchPromptTokens := 0
	batchGenTokens := 0

	var startTimes, endTimes, firstTokenTimes, lastTokenTimes []float64
	var allTokenTimestamps []float64

	var batchReqSeries []TimeSeries

	for _, res := range validResults {
		startTimes = append(startTimes, res.StartTs)
		endTimes = append(endTimes, res.EndTs)
		allTokenTimestamps = append(allTokenTimestamps, res.TokenTimestamps...)

		if saveAllThroughputTSeries {
			if len(res.TokenTimestamps) > 0 {
				peak, series := calculatePeakThroughput(res.TokenTimestamps, 1.0, true)
				batchReqSeries = append(batchReqSeries, series)
				*aggPeakReqThroughputs = append(*aggPeakReqThroughputs, peak)
			} else {
				batchReqSeries = append(batchReqSeries, TimeSeries{})
			}
		} else if len(res.TokenTimestamps) > 0 {
			peak, _ := calculatePeakThroughput(res.TokenTimestamps, 1.0, false)
			*aggPeakReqThroughputs = append(*aggPeakReqThroughputs, peak)
		}

		if len(res.TokenTimestamps) > 0 {
			lastTokenTimes = append(lastTokenTimes, res.TokenTimestamps[len(res.TokenTimestamps)-1])
		} else if res.EndTs > 0 {
			lastTokenTimes = append(lastTokenTimes, res.EndTs)
		}

		// Use reported usage if available and reasonable, else expected
		promptTokens := expectedPPTokens
		if res.PromptTokens > 0 {
			diff := math.Abs(float64(res.PromptTokens - expectedPPTokens))
			if diff < float64(expectedPPTokens)*0.2 {
				promptTokens = res.PromptTokens
			}
		}

		batchPromptTokens += promptTokens
		batchGenTokens += res.TotalTokens

		ttft := 0.0
		e2eTTFT := 0.0
		ttfr := 0.0
		estPPT := 0.0

		if res.FirstResponseTs != nil {
			ttfr = *res.FirstResponseTs - res.StartTs
			*aggTTFRTValues = append(*aggTTFRTValues, ttfr)
		}

		if res.FirstTokenTs != nil {
			firstTokenTimes = append(firstTokenTimes, *res.FirstTokenTs)
			e2eTTFT = *res.FirstTokenTs - res.StartTs
			ttft = math.Max(0, e2eTTFT-latency)
			estPPT = math.Max(0, ttfr-latency)

			*aggE2ETTFTValues = append(*aggE2ETTFTValues, e2eTTFT)
			*aggTTFTValues = append(*aggTTFTValues, ttft)
			*aggEstPPTValues = append(*aggEstPPTValues, estPPT)
		}

		// Individual speeds
		if estPPT > 0 {
			ppSpeed := float64(promptTokens) / estPPT
			*aggPPSpeeds = append(*aggPPSpeeds, ppSpeed)
		}

		if res.TotalTokens > 1 && res.FirstTokenTs != nil {
			decodeTime := res.EndTs - *res.FirstTokenTs
			if decodeTime > 0 {
				tgSpeed := float64(res.TotalTokens-1) / decodeTime
				*aggTGSpeeds = append(*aggTGSpeeds, tgSpeed)
			}
		}
	}

	if saveAllThroughputTSeries && aggReqThroughputSeries != nil {
		*aggReqThroughputSeries = append(*aggReqThroughputSeries, batchReqSeries)
	}

	// Batch-level throughput
	if len(startTimes) > 0 && len(endTimes) > 0 && len(firstTokenTimes) > 0 {
		minStart := startTimes[0]
		maxEnd := endTimes[0]
		maxFirstToken := firstTokenTimes[0]
		minFirstToken := firstTokenTimes[0]
		for _, v := range startTimes {
			if v < minStart {
				minStart = v
			}
		}
		for _, v := range endTimes {
			if v > maxEnd {
				maxEnd = v
			}
		}
		for _, v := range firstTokenTimes {
			if v > maxFirstToken {
				maxFirstToken = v
			}
			if v < minFirstToken {
				minFirstToken = v
			}
		}

		ppDuration := maxFirstToken - minStart
		if ppDuration > 0 {
			batchPPThroughput := float64(batchPromptTokens) / ppDuration
			*aggBatchPPThroughputs = append(*aggBatchPPThroughputs, batchPPThroughput)
		}

		// Use max(last_token_times) instead of max(end_times) to remove
		// protocol overhead (headers, [DONE], etc).
		maxLastToken := maxEnd
		if len(lastTokenTimes) > 0 {
			maxLastToken = lastTokenTimes[0]
			for _, v := range lastTokenTimes {
				if v > maxLastToken {
					maxLastToken = v
				}
			}
		}
		tgDuration := maxLastToken - minFirstToken
		if tgDuration > 0 {
			if batchGenTokens > len(validResults) {
				batchTGThroughput := float64(batchGenTokens-len(validResults)) / tgDuration
				*aggBatchTGThroughputs = append(*aggBatchTGThroughputs, batchTGThroughput)
			}
		}
	}

	if len(allTokenTimestamps) > 0 {
		if saveTotalThroughputTSeries {
			peak, series := calculatePeakThroughput(allTokenTimestamps, 1.0, true)
			*aggPeakThroughputs = append(*aggPeakThroughputs, peak)
			if aggThroughputSeries != nil {
				*aggThroughputSeries = append(*aggThroughputSeries, series)
			}
		} else {
			peak, _ := calculatePeakThroughput(allTokenTimestamps, 1.0, false)
			*aggPeakThroughputs = append(*aggPeakThroughputs, peak)
		}
	}
}

func (r *BenchmarkResults) generateRows() []map[string]interface{} {
	rows := make([]map[string]interface{}, 0)
	for _, run := range r.Runs {
		cSuffix := ""
		if r.Metadata != nil && r.Metadata.MaxConcurrency > 1 {
			cSuffix = fmt.Sprintf(" (c%d)", run.Concurrency)
		}

		modelName := r.ModelName
		if modelName == "" {
			modelName = "Unknown"
		}

		if run.IsContextPrefillPhase {
			if run.PPThroughput != nil {
				rows = append(rows, map[string]interface{}{
					"model":       modelName,
					"test_name":   fmt.Sprintf("ctx_pp @ d%d%s", run.ContextSize, cSuffix),
					"t_s":         run.PPThroughput,
					"t_s_req":     run.PPReqThroughput,
					"peak_ts":     nil,
					"peak_ts_req": nil,
					"ttfr":        run.TTFR,
					"est_ppt":     run.EstPPT,
					"e2e_ttft":    run.E2ETTFT,
				})
			}
			if run.TGThroughput != nil {
				rows = append(rows, map[string]interface{}{
					"model":       modelName,
					"test_name":   fmt.Sprintf("ctx_tg @ d%d%s", run.ContextSize, cSuffix),
					"t_s":         run.TGThroughput,
					"t_s_req":     run.TGReqThroughput,
					"peak_ts":     run.PeakThroughput,
					"peak_ts_req": run.PeakReqThroughput,
					"ttfr":        nil,
					"est_ppt":     nil,
					"e2e_ttft":    nil,
				})
			}
		} else {
			dSuffix := ""
			if run.ContextSize > 0 {
				dSuffix = fmt.Sprintf(" @ d%d", run.ContextSize)
			}

			if run.PPThroughput != nil {
				rows = append(rows, map[string]interface{}{
					"model":       modelName,
					"test_name":   fmt.Sprintf("pp%d%s%s", run.PromptSize, dSuffix, cSuffix),
					"t_s":         run.PPThroughput,
					"t_s_req":     run.PPReqThroughput,
					"peak_ts":     nil,
					"peak_ts_req": nil,
					"ttfr":        run.TTFR,
					"est_ppt":     run.EstPPT,
					"e2e_ttft":    run.E2ETTFT,
				})
			}
			if run.TGThroughput != nil {
				rows = append(rows, map[string]interface{}{
					"model":       modelName,
					"test_name":   fmt.Sprintf("tg%d%s%s", run.ResponseSize, dSuffix, cSuffix),
					"t_s":         run.TGThroughput,
					"t_s_req":     run.TGReqThroughput,
					"peak_ts":     run.PeakThroughput,
					"peak_ts_req": run.PeakReqThroughput,
					"ttfr":        nil,
					"est_ppt":     nil,
					"e2e_ttft":    nil,
				})
			}
		}
	}
	return rows
}

func formatMetric(m *BenchmarkMetric) string {
	if m == nil {
		return ""
	}
	return fmt.Sprintf("%.2f \u00b1 %.2f", m.Mean, m.Std)
}

// displayWidth returns the number of terminal columns a string occupies.
// Multi-byte UTF-8 sequences such as "\u00b1" count as one column; East Asian
// wide and fullwidth runes count as two.
func displayWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case r < 0x80:
			w++
		case isWideRune(r):
			w += 2
		default:
			w++
		}
	}
	return w
}

func isWideRune(r rune) bool {
	return (r >= 0x1100 && r <= 0x115F) || // Hangul Jamo
		(r >= 0x2E80 && r <= 0x303E) || // CJK radicals, CJK symbols
		(r >= 0x3041 && r <= 0x33FF) || // Hiragana .. CJK compatibility
		(r >= 0x3400 && r <= 0x4DBF) || // CJK ext A
		(r >= 0x4E00 && r <= 0x9FFF) || // CJK unified
		(r >= 0xA000 && r <= 0xA4CF) || // Yi
		(r >= 0xAC00 && r <= 0xD7A3) || // Hangul syllables
		(r >= 0xF900 && r <= 0xFAFF) || // CJK compat ideographs
		(r >= 0xFE30 && r <= 0xFE4F) || // CJK compat forms
		(r >= 0xFF00 && r <= 0xFF60) || // Fullwidth forms
		(r >= 0xFFE0 && r <= 0xFFE6) || // Fullwidth signs
		(r >= 0x20000 && r <= 0x3FFFD) // CJK ext B+
}

// generateMDReport renders the results as a pipe-formatted markdown table.
func (r *BenchmarkResults) generateMDReport(concurrency int) string {
	rows := r.generateRows()
	if len(rows) == 0 {
		return "No results collected. Check if the model is generating tokens."
	}

	var headers []string
	var keyOrder []string
	tsHeader := "t/s"
	if concurrency > 1 {
		tsHeader = "t/s (total)"
		headers = []string{"model", "test", tsHeader, "t/s (req)", "peak t/s", "peak t/s (req)", "ttfr (ms)", "est_ppt (ms)", "e2e_ttft (ms)"}
		keyOrder = []string{"model", "test_name", "t_s", "t_s_req", "peak_ts", "peak_ts_req", "ttfr", "est_ppt", "e2e_ttft"}
	} else {
		headers = []string{"model", "test", tsHeader, "peak t/s", "ttfr (ms)", "est_ppt (ms)", "e2e_ttft (ms)"}
		keyOrder = []string{"model", "test_name", "t_s", "peak_ts", "ttfr", "est_ppt", "e2e_ttft"}
	}

	data := make([][]string, 0, len(rows))
	for _, row := range rows {
		var line []string
		for _, key := range keyOrder {
			val := row[key]
			switch v := val.(type) {
			case string:
				line = append(line, v)
			case *BenchmarkMetric:
				line = append(line, formatMetric(v))
			default:
				line = append(line, "")
			}
		}
		data = append(data, line)
	}

	// Compute column widths. Widths are measured in display columns (runes),
	// not bytes, because cells contain multi-byte characters such as "±"
	// that render as a single column in the terminal.
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = displayWidth(h)
	}
	for _, line := range data {
		for i, cell := range line {
			if w := displayWidth(cell); w > widths[i] {
				widths[i] = w
			}
		}
	}

	var b strings.Builder
	writeRow := func(cells []string, aligns []string) {
		b.WriteString("|")
		for i, cell := range cells {
			if aligns[i] == "left" {
				b.WriteString(" " + cell + strings.Repeat(" ", widths[i]-displayWidth(cell)) + " |")
			} else {
				b.WriteString(" " + strings.Repeat(" ", widths[i]-displayWidth(cell)) + cell + " |")
			}
		}
		b.WriteString("\n")
	}
	// Alignment: first column left, the rest right (as in the Python version)
	aligns := make([]string, len(headers))
	aligns[0] = "left"
	for i := 1; i < len(headers); i++ {
		aligns[i] = "right"
	}
	writeRow(headers, aligns)

	// Separator row
	b.WriteString("|")
	for i := range headers {
		if aligns[i] == "left" {
			b.WriteString(":" + strings.Repeat("-", widths[i]) + " |")
		} else {
			b.WriteString(strings.Repeat("-", widths[i]) + ": |")
		}
	}
	b.WriteString("\n")

	for _, line := range data {
		writeRow(line, aligns)
	}

	return strings.TrimRight(b.String(), "\n")
}

// SaveReport prints or saves the results in the given format.
func (r *BenchmarkResults) SaveReport(filename, format string, concurrency int) {
	var msg string
	if filename != "" {
		msg += fmt.Sprintf("Saving results to %s in %s format...\n", filename, strings.ToUpper(format))
	} else {
		msg += fmt.Sprintf("Printing results in %s format:\n", strings.ToUpper(format))
	}
	fmt.Printf("%s\n", msg)

	switch format {
	case "md":
		output := r.generateMDReport(concurrency)
		if filename != "" {
			_ = os.WriteFile(filename, []byte(output+"\n"), 0o644)
		} else {
			fmt.Printf("\n%s\n", output)
		}

	case "json":
		report := BenchmarkReport{Benchmarks: r.Runs}
		if r.Metadata != nil {
			report.BenchmarkMetadata = *r.Metadata
		}
		jsonBytes, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			fmt.Printf("Error serializing JSON report: %v\n", err)
			return
		}
		if filename != "" {
			_ = os.WriteFile(filename, append(jsonBytes, '\n'), 0o644)
		} else {
			fmt.Println(string(jsonBytes))
		}

	case "csv":
		csvHeaders := []string{"model", "test_name", "t_s_mean", "t_s_std", "t_s_req_mean", "t_s_req_std", "peak_ts_mean", "peak_ts_std", "peak_ts_req_mean", "peak_ts_req_std", "ttfr_mean", "ttfr_std", "est_ppt_mean", "est_ppt_std", "e2e_ttft_mean", "e2e_ttft_std"}
		metricCells := func(m *BenchmarkMetric) []string {
			if m == nil {
				return []string{"", ""}
			}
			return []string{strconv2(m.Mean), strconv2(m.Std)}
		}

		var w *csv.Writer
		var f *os.File
		if filename != "" {
			var err error
			f, err = os.Create(filename)
			if err != nil {
				fmt.Printf("Error creating CSV file: %v\n", err)
				return
			}
			defer f.Close()
			w = csv.NewWriter(f)
		} else {
			w = csv.NewWriter(os.Stdout)
		}
		_ = w.Write(csvHeaders)
		for _, row := range r.generateRows() {
			record := []string{
				row["model"].(string),
				row["test_name"].(string),
			}
			for _, key := range []string{"t_s", "t_s_req", "peak_ts", "peak_ts_req", "ttfr", "est_ppt", "e2e_ttft"} {
				if m, ok := row[key].(*BenchmarkMetric); ok {
					record = append(record, metricCells(m)...)
				} else {
					record = append(record, "", "")
				}
			}
			_ = w.Write(record)
		}
		w.Flush()
	}
}

func strconv2(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}
