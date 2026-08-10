// Package exporter coordinates direct source scrapes and exposes normalized
// accelerator metrics.
package exporter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"xiaoshiai.cn/rune/internal/model"
	"xiaoshiai.cn/rune/internal/transform"
)

var prometheusName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

// Scraper fetches one configured Prometheus metrics endpoint.
type Scraper interface {
	Scrape(context.Context, string) (model.Families, error)
}

// TargetProvider resolves configured static and dynamic source definitions.
type TargetProvider interface {
	Targets(context.Context) ([]model.Source, error)
}

// Options controls source scraping.
type Options struct {
	ScrapeInterval time.Duration
	ScrapeTimeout  time.Duration
	MaxConcurrency int
	Sources        []model.Source
	TargetProvider TargetProvider
}

// NewDefaultOptions returns production-safe collection defaults.
func NewDefaultOptions() Options {
	return Options{
		ScrapeInterval: 15 * time.Second,
		ScrapeTimeout:  5 * time.Second,
		MaxConcurrency: 8,
	}
}

type targetState struct {
	acceleratorType     string
	up                  bool
	durationSeconds     float64
	samplesRead         int
	scrapeErrorsTotal   uint64
	samplesDroppedTotal uint64
}

type state struct {
	ready                 bool
	lastRefreshSuccess    bool
	lastRefreshTimestamp  time.Time
	series                []model.Sample
	targets               map[string]targetState
	discoveryErrorsTotal  uint64
	duplicateSamplesTotal uint64
}

// Exporter periodically collects and merges all configured sources.
type Exporter struct {
	scraper  Scraper
	provider TargetProvider
	options  Options
	logger   *slog.Logger

	mu    sync.RWMutex
	state state
}

// New creates an Exporter.
func New(scraper Scraper, options Options, logger *slog.Logger) (*Exporter, error) {
	if scraper == nil {
		return nil, fmt.Errorf("scraper is required")
	}
	if options.ScrapeInterval <= 0 || options.ScrapeTimeout <= 0 {
		return nil, fmt.Errorf("scrape interval and timeout must be positive")
	}
	if options.MaxConcurrency < 1 {
		return nil, fmt.Errorf("max concurrency must be positive")
	}
	if len(options.Sources) == 0 && options.TargetProvider == nil {
		return nil, fmt.Errorf("at least one metrics source is required")
	}
	targets := make(map[string]targetState, len(options.Sources))
	for _, source := range options.Sources {
		if source.Name == "" || source.Address == "" {
			return nil, fmt.Errorf("every source requires a name and address")
		}
		if _, exists := targets[source.Name]; exists {
			return nil, fmt.Errorf("duplicate source name %q", source.Name)
		}
		if !supportedType(source.AcceleratorType) {
			return nil, fmt.Errorf("source %q has unsupported accelerator type %q", source.Name, source.AcceleratorType)
		}
		targets[source.Name] = targetState{acceleratorType: source.AcceleratorType}
	}
	if logger == nil {
		logger = slog.Default()
	}
	provider := options.TargetProvider
	if provider == nil {
		provider = staticTargetProvider(append([]model.Source(nil), options.Sources...))
	}
	return &Exporter{
		scraper:  scraper,
		provider: provider,
		options:  options,
		logger:   logger,
		state:    state{targets: targets},
	}, nil
}

// Run collects immediately and then at the configured interval until cancelled.
func (e *Exporter) Run(ctx context.Context) {
	e.refreshAndLog(ctx)
	ticker := time.NewTicker(e.options.ScrapeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.refreshAndLog(ctx)
		}
	}
}

func (e *Exporter) refreshAndLog(ctx context.Context) {
	if err := e.Refresh(ctx); err != nil && !errors.Is(err, context.Canceled) {
		e.logger.Error("refresh accelerator metrics", "error", err)
	}
}

type scrapeResult struct {
	index       int
	source      model.Source
	families    model.Families
	duration    time.Duration
	samplesRead int
	err         error
}

// Refresh performs one direct scrape and aggregation cycle.
func (e *Exporter) Refresh(ctx context.Context) error {
	startedAt := time.Now()
	sources, discoveryErr := e.provider.Targets(ctx)
	sources, validationErr := validTargets(sources)
	discoveryErr = errors.Join(discoveryErr, validationErr)
	resultsChannel := make(chan scrapeResult, len(sources))
	semaphore := make(chan struct{}, e.options.MaxConcurrency)
	var group sync.WaitGroup
	for index, source := range sources {
		group.Add(1)
		go func() {
			defer group.Done()
			result := scrapeResult{index: index, source: source}
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				result.err = ctx.Err()
				resultsChannel <- result
				return
			}
			defer func() { <-semaphore }()
			scrapeStartedAt := time.Now()
			scrapeContext, cancel := context.WithTimeout(ctx, e.options.ScrapeTimeout)
			defer cancel()
			result.families, result.err = e.scraper.Scrape(scrapeContext, source.Address)
			result.duration = time.Since(scrapeStartedAt)
			result.samplesRead = countRawSamples(result.families)
			resultsChannel <- result
		}()
	}
	group.Wait()
	close(resultsChannel)

	results := make([]scrapeResult, len(sources))
	for result := range resultsChannel {
		results[result.index] = result
	}
	inputs := make([]transform.Input, 0, len(results))
	for _, result := range results {
		if result.err == nil {
			inputs = append(inputs, transform.Input{Source: result.source, Families: result.families})
		}
	}
	inventory := transform.BuildInventory(inputs)

	e.mu.Lock()
	defer e.mu.Unlock()
	targets := make(map[string]targetState, len(sources))
	for _, source := range sources {
		target := e.state.targets[source.Name]
		target.acceleratorType = source.AcceleratorType
		targets[source.Name] = target
	}
	seriesByKey := map[string]model.Sample{}
	var refreshErrors []error
	if discoveryErr != nil {
		refreshErrors = append(refreshErrors, fmt.Errorf("discover metrics targets: %w", discoveryErr))
		e.state.discoveryErrorsTotal++
	}
	successfulTargets := 0
	var duplicateSamples uint64
	for _, result := range results {
		target := targets[result.source.Name]
		target.acceleratorType = result.source.AcceleratorType
		target.durationSeconds = result.duration.Seconds()
		target.samplesRead = result.samplesRead
		if result.err != nil {
			target.up = false
			target.scrapeErrorsTotal++
			targets[result.source.Name] = target
			refreshErrors = append(refreshErrors, fmt.Errorf("scrape source %q: %w", result.source.Name, result.err))
			continue
		}
		target.up = true
		successfulTargets++
		samples, dropped := transform.Transform(result.source, result.families, inventory)
		target.samplesDroppedTotal += uint64(dropped)
		targets[result.source.Name] = target
		for _, sample := range samples {
			key := sampleKey(sample)
			if _, exists := seriesByKey[key]; exists {
				duplicateSamples++
				continue
			}
			seriesByKey[key] = sample
		}
	}
	series := make([]model.Sample, 0, len(seriesByKey))
	for _, sample := range seriesByKey {
		series = append(series, sample)
	}
	sort.Slice(series, func(i, j int) bool { return sampleKey(series[i]) < sampleKey(series[j]) })
	e.state.ready = successfulTargets > 0 || (len(sources) == 0 && discoveryErr == nil)
	e.state.lastRefreshSuccess = len(refreshErrors) == 0
	e.state.lastRefreshTimestamp = startedAt
	e.state.series = series
	e.state.targets = targets
	e.state.duplicateSamplesTotal += duplicateSamples
	return errors.Join(refreshErrors...)
}

// Metrics serves unified accelerator metrics and exporter self-metrics.
func (e *Exporter) Metrics(writer http.ResponseWriter, _ *http.Request) {
	e.mu.RLock()
	current := state{
		ready:                 e.state.ready,
		lastRefreshSuccess:    e.state.lastRefreshSuccess,
		lastRefreshTimestamp:  e.state.lastRefreshTimestamp,
		series:                append([]model.Sample(nil), e.state.series...),
		targets:               cloneTargetStates(e.state.targets),
		discoveryErrorsTotal:  e.state.discoveryErrorsTotal,
		duplicateSamplesTotal: e.state.duplicateSamplesTotal,
	}
	e.mu.RUnlock()

	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	lastRefreshTimestamp := float64(0)
	if !current.lastRefreshTimestamp.IsZero() {
		lastRefreshTimestamp = float64(current.lastRefreshTimestamp.Unix())
	}
	writeGauge(writer, "accelerator_exporter_ready", "Whether at least one configured source was scraped successfully.", boolFloat(current.ready))
	writeGauge(writer, "accelerator_exporter_last_refresh_success", "Whether every configured source succeeded in the latest refresh.", boolFloat(current.lastRefreshSuccess))
	writeGauge(writer, "accelerator_exporter_targets_discovered", "Number of static or dynamically discovered accelerator metrics targets.", float64(len(current.targets)))
	writeGauge(writer, "accelerator_exporter_series_exposed", "Number of normalized accelerator series currently exposed.", float64(len(current.series)))
	writeGauge(writer, "accelerator_exporter_last_refresh_timestamp_seconds", "Unix timestamp of the latest refresh attempt.", lastRefreshTimestamp)
	writeCounter(writer, "accelerator_exporter_duplicate_samples_total", "Total duplicate normalized samples discarded.", current.duplicateSamplesTotal)
	writeCounter(writer, "accelerator_exporter_discovery_errors_total", "Total static or Kubernetes target discovery errors.", current.discoveryErrorsTotal)

	names := make([]string, 0, len(current.targets))
	for name := range current.targets {
		names = append(names, name)
	}
	sort.Strings(names)
	writeTargetMetricHeader(writer, "accelerator_exporter_target_up", "Whether the latest direct scrape of a configured source succeeded.", "gauge")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_target_up", name, target.acceleratorType, boolFloat(target.up))
	}
	writeTargetMetricHeader(writer, "accelerator_exporter_target_scrape_duration_seconds", "Duration of the latest direct source scrape.", "gauge")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_target_scrape_duration_seconds", name, target.acceleratorType, target.durationSeconds)
	}
	writeTargetMetricHeader(writer, "accelerator_exporter_target_samples_read", "Number of raw samples read from a source in the latest scrape.", "gauge")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_target_samples_read", name, target.acceleratorType, float64(target.samplesRead))
	}
	writeTargetMetricHeader(writer, "accelerator_exporter_scrape_errors_total", "Total direct source scrape errors.", "counter")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_scrape_errors_total", name, target.acceleratorType, float64(target.scrapeErrorsTotal))
	}
	writeTargetMetricHeader(writer, "accelerator_exporter_samples_dropped_total", "Total source samples dropped because identity labels were missing.", "counter")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_samples_dropped_total", name, target.acceleratorType, float64(target.samplesDroppedTotal))
	}

	lastMetric := ""
	for _, sample := range current.series {
		if sample.Name != lastMetric {
			metadata := canonicalMetrics[sample.Name]
			_, _ = fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s gauge\n", sample.Name, metadata, sample.Name)
			lastMetric = sample.Name
		}
		_, _ = fmt.Fprintf(writer, "%s%s %s\n", sample.Name, formatLabels(sample.Labels), formatFloat(sample.Value))
	}
}

// Health reports process liveness.
func (*Exporter) Health(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("ok\n"))
}

// Ready reports whether at least one configured source has been scraped.
func (e *Exporter) Ready(writer http.ResponseWriter, _ *http.Request) {
	e.mu.RLock()
	ready := e.state.ready
	e.mu.RUnlock()
	if !ready {
		http.Error(writer, "accelerator metrics are not ready", http.StatusServiceUnavailable)
		return
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte("ok\n"))
}

var canonicalMetrics = map[string]string{
	"accelerator_info":                                    "Static accelerator device information.",
	"accelerator_core_utilization_percentage":             "Accelerator compute core utilization percentage.",
	"accelerator_memory_bandwidth_utilization_percentage": "Accelerator memory bandwidth utilization percentage.",
	"accelerator_memory_total_bytes":                      "Total accelerator memory in bytes.",
	"accelerator_memory_used_bytes":                       "Used accelerator memory in bytes.",
	"accelerator_memory_free_bytes":                       "Free accelerator memory in bytes.",
	"accelerator_memory_utilization_percentage":           "Accelerator memory capacity utilization percentage.",
	"accelerator_temperature_celsius":                     "Accelerator core temperature in degrees Celsius.",
	"accelerator_power_usage_watts":                       "Current accelerator power usage in watts.",
	"accelerator_power_limit_watts":                       "Accelerator enforced power limit in watts.",
}

func supportedType(acceleratorType string) bool {
	switch acceleratorType {
	case model.AcceleratorTypeGPU, model.AcceleratorTypeNPU, model.AcceleratorTypeVGPU, model.AcceleratorTypeVNPU:
		return true
	default:
		return false
	}
}

func countRawSamples(families model.Families) int {
	total := 0
	for _, samples := range families {
		total += len(samples)
	}
	return total
}

func cloneTargetStates(states map[string]targetState) map[string]targetState {
	cloned := make(map[string]targetState, len(states))
	for name, target := range states {
		cloned[name] = target
	}
	return cloned
}

type staticTargetProvider []model.Source

func (provider staticTargetProvider) Targets(context.Context) ([]model.Source, error) {
	return append([]model.Source(nil), provider...), nil
}

func validTargets(sources []model.Source) ([]model.Source, error) {
	valid := make([]model.Source, 0, len(sources))
	seen := map[string]struct{}{}
	var targetErrors []error
	for _, source := range sources {
		if source.Name == "" || source.Address == "" || !supportedType(source.AcceleratorType) {
			targetErrors = append(targetErrors, fmt.Errorf("invalid discovered target %q", source.Name))
			continue
		}
		if _, exists := seen[source.Name]; exists {
			targetErrors = append(targetErrors, fmt.Errorf("duplicate discovered target name %q", source.Name))
			continue
		}
		seen[source.Name] = struct{}{}
		valid = append(valid, source)
	}
	return valid, errors.Join(targetErrors...)
}

func sampleKey(sample model.Sample) string {
	labels := make([]string, 0, len(sample.Labels))
	for name, value := range sample.Labels {
		labels = append(labels, name+"="+value)
	}
	sort.Strings(labels)
	return sample.Name + "\xff" + strings.Join(labels, "\xff")
}

func writeGauge(writer http.ResponseWriter, name, help string, value float64) {
	_, _ = fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s gauge\n%s %s\n", name, help, name, name, formatFloat(value))
}

func writeCounter(writer http.ResponseWriter, name, help string, value uint64) {
	_, _ = fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, value)
}

func writeTargetMetricHeader(writer http.ResponseWriter, name, help, metricType string) {
	_, _ = fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func writeTargetMetric(writer http.ResponseWriter, metricName, sourceName, acceleratorType string, value float64) {
	labels := map[string]string{"source": sourceName, "accelerator_type": acceleratorType}
	_, _ = fmt.Fprintf(writer, "%s%s %s\n", metricName, formatLabels(labels), formatFloat(value))
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		if prometheusName.MatchString(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var builder strings.Builder
	builder.WriteByte('{')
	for index, name := range names {
		if index > 0 {
			builder.WriteByte(',')
		}
		builder.WriteString(name)
		builder.WriteString("=\"")
		builder.WriteString(escapeLabelValue(labels[name]))
		builder.WriteByte('"')
	}
	builder.WriteByte('}')
	return builder.String()
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
