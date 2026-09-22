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

	"xiaoshiai.cn/accelerator-exporter/pkg/accelerator"
)

var prometheusName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)

type targetState struct {
	rule              string
	up                bool
	durationSeconds   float64
	samplesRead       int
	scrapeErrorsTotal uint64
}

type state struct {
	ready                 bool
	lastRefreshSuccess    bool
	lastRefreshTimestamp  time.Time
	series                []accelerator.Sample
	targets               map[string]targetState
	discoveryErrorsTotal  uint64
	duplicateSamplesTotal uint64
	contractViolations    map[string]uint64

	// allocationConfigured says whether this node is wired to a device
	// allocation source at all. allocationSourceUp is the outcome of the latest
	// read, so a source that stopped answering is visible instead of leaving the
	// relation layer quietly empty.
	allocationConfigured  bool
	allocationSourceUp    bool
	allocationErrorsTotal uint64
}

// configuration is the initialized form of Options and the injected external
// systems. It holds the resolved instances, so it stays package-internal and is
// never serialized.
type configuration struct {
	interval    time.Duration
	timeout     time.Duration
	concurrency int
	scraper     Scraper
	targets     TargetProvider
	allocations AllocationProvider
	logger      *slog.Logger
}

// Exporter periodically collects and merges every named source configuration.
type Exporter struct {
	configuration

	mu    sync.RWMutex
	state state
}

// New initializes structural options and the injected external systems into an
// Exporter. A nil allocations provider is accepted and means this node has no
// device allocation source.
//
// It validates only the collection loop: the dependencies are already built, and
// whether the configuration named any sources is Run's business.
func New(
	options *Options, scraper Scraper, targets TargetProvider, allocations AllocationProvider, logger *slog.Logger,
) (*Exporter, error) {
	if options == nil {
		return nil, fmt.Errorf("exporter options are required")
	}
	if err := validateCollection(options.ScrapeInterval, options.ScrapeTimeout, options.MaxConcurrency); err != nil {
		return nil, err
	}
	if scraper == nil {
		return nil, fmt.Errorf("scraper is required")
	}
	if targets == nil {
		return nil, fmt.Errorf("target provider is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Exporter{
		configuration: configuration{
			interval:    options.ScrapeInterval,
			timeout:     options.ScrapeTimeout,
			concurrency: options.MaxConcurrency,
			scraper:     scraper,
			targets:     targets,
			allocations: allocations,
			logger:      logger,
		},
		state: state{
			targets:              map[string]targetState{},
			contractViolations:   map[string]uint64{},
			allocationConfigured: allocations != nil,
		},
	}, nil
}

// Run collects immediately and then at the configured interval until cancelled.
func (e *Exporter) Run(ctx context.Context) {
	e.refreshAndLog(ctx)
	ticker := time.NewTicker(e.interval)
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
	source      accelerator.Source
	families    accelerator.Families
	duration    time.Duration
	samplesRead int
	err         error
}

// Refresh performs one direct scrape and aggregation cycle.
func (e *Exporter) Refresh(ctx context.Context) error {
	startedAt := time.Now()
	sources, discoveryErr := e.targets.Targets(ctx)
	sources, validationErr := validTargets(sources)
	discoveryErr = errors.Join(discoveryErr, validationErr)
	resultsChannel := make(chan scrapeResult, len(sources))
	semaphore := make(chan struct{}, e.concurrency)
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
			scrapeContext, cancel := context.WithTimeout(ctx, e.timeout)
			defer cancel()
			result.families, result.err = e.scraper.Scrape(scrapeContext, source)
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
	inputs := make([]accelerator.InventoryInput, 0, len(results))
	for _, result := range results {
		if result.err == nil {
			inputs = append(inputs, accelerator.InventoryInput{Source: result.source, Families: result.families})
		}
	}
	inventory := accelerator.BuildInventory(inputs)

	allocations, allocationErr := e.readAllocations(ctx)

	e.mu.Lock()
	defer e.mu.Unlock()
	targets := make(map[string]targetState, len(sources))
	for _, source := range sources {
		target := e.state.targets[source.Name]
		target.rule = source.Rule
		targets[source.Name] = target
	}
	var refreshErrors []error
	if discoveryErr != nil {
		refreshErrors = append(refreshErrors, fmt.Errorf("discover metrics targets: %w", discoveryErr))
		e.state.discoveryErrorsTotal++
	}
	if allocationErr != nil {
		refreshErrors = append(refreshErrors, fmt.Errorf("read device allocation facts: %w", allocationErr))
	}

	var converted []accelerator.Sample
	var allocationIndexes []map[string]accelerator.Identity
	successfulTargets := 0
	var duplicateSamples uint64
	for _, result := range results {
		target := targets[result.source.Name]
		target.rule = result.source.Rule
		target.durationSeconds = result.duration.Seconds()
		target.samplesRead = result.samplesRead
		if result.err != nil {
			target.up = false
			target.scrapeErrorsTotal++
			targets[result.source.Name] = target
			refreshErrors = append(refreshErrors, fmt.Errorf("scrape source %q: %w", result.source.Name, result.err))
			continue
		}
		rule, ok := accelerator.RuleByName(result.source.Rule)
		if !ok {
			// validTargets already rejected an unknown rule, so reaching this
			// point means the rule set changed under a discovered target.
			target.up = false
			targets[result.source.Name] = target
			refreshErrors = append(refreshErrors, fmt.Errorf("source %q has unknown rule %q", result.source.Name, result.source.Rule))
			continue
		}
		target.up = true
		successfulTargets++
		applied := rule.Apply(result.source, result.families, inventory)
		targets[result.source.Name] = target
		converted = append(converted, applied.Samples...)
		allocationIndexes = append(allocationIndexes, applied.AllocationIndex)
	}
	// The relation layer is assembled once every source has been resolved, so a
	// binding can match a target any source on this node reported.
	converted = append(converted, accelerator.BuildPodInfo(allocations, allocationIndexes...)...)

	seriesByKey := map[string]accelerator.Sample{}
	violations := map[string]uint64{}
	for _, sample := range converted {
		drop, reasons := sample.Validate()
		for _, reason := range reasons {
			violations[reason]++
		}
		if drop {
			continue
		}
		key := sampleKey(sample)
		if _, exists := seriesByKey[key]; exists {
			duplicateSamples++
			continue
		}
		seriesByKey[key] = sample
	}

	series := make([]accelerator.Sample, 0, len(seriesByKey))
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
	e.state.contractViolations = mergeCounters(e.state.contractViolations, violations)
	return errors.Join(refreshErrors...)
}

// readAllocations reads the device allocation facts of this round.
//
// The answer is never carried over from the previous round: a failed read leaves
// the relation layer empty on purpose, because a stale Pod-to-device mapping
// would report a workload as still owning a device it no longer holds.
func (e *Exporter) readAllocations(ctx context.Context) ([]accelerator.Allocation, error) {
	if e.allocations == nil {
		return nil, nil
	}
	return e.allocations.Allocations(ctx)
}

// Metrics serves unified accelerator metrics and exporter self-metrics.
func (e *Exporter) Metrics(writer http.ResponseWriter, _ *http.Request) {
	e.mu.RLock()
	current := state{
		ready:                 e.state.ready,
		lastRefreshSuccess:    e.state.lastRefreshSuccess,
		lastRefreshTimestamp:  e.state.lastRefreshTimestamp,
		series:                append([]accelerator.Sample(nil), e.state.series...),
		targets:               cloneTargetStates(e.state.targets),
		discoveryErrorsTotal:  e.state.discoveryErrorsTotal,
		duplicateSamplesTotal: e.state.duplicateSamplesTotal,
		contractViolations:    mergeCounters(nil, e.state.contractViolations),
		allocationConfigured:  e.state.allocationConfigured,
		allocationSourceUp:    e.state.allocationSourceUp,
		allocationErrorsTotal: e.state.allocationErrorsTotal,
	}
	e.mu.RUnlock()

	writer.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	lastRefreshTimestamp := float64(0)
	if !current.lastRefreshTimestamp.IsZero() {
		lastRefreshTimestamp = float64(current.lastRefreshTimestamp.Unix())
	}
	writeGauge(writer, "accelerator_exporter_ready", "Whether at least one configured source was scraped successfully.", boolFloat(current.ready))
	writeGauge(writer, "accelerator_exporter_last_refresh_success", "Whether every configured source succeeded in the latest refresh.", boolFloat(current.lastRefreshSuccess))
	writeGauge(writer, "accelerator_exporter_targets_discovered", "Number of Kubernetes-discovered accelerator metrics targets on this node.", float64(len(current.targets)))
	writeGauge(writer, "accelerator_exporter_series_exposed", "Number of normalized accelerator series currently exposed.", float64(len(current.series)))
	writeGauge(writer, "accelerator_exporter_last_refresh_timestamp_seconds", "Unix timestamp of the latest refresh attempt.", lastRefreshTimestamp)
	writeCounter(writer, "accelerator_exporter_duplicate_samples_total", "Total duplicate normalized samples discarded.", current.duplicateSamplesTotal)
	writeCounter(writer, "accelerator_exporter_discovery_errors_total", "Total Kubernetes target discovery errors.", current.discoveryErrorsTotal)
	// The allocation source is reported separately from the scrapes: a node with
	// no source at all and a node whose source stopped answering both leave
	// accelerator_pod_info empty, and they are different incidents.
	writeGauge(writer, "accelerator_exporter_allocation_source_configured", "Whether this node is wired to a device allocation source.", boolFloat(current.allocationConfigured))
	writeGauge(writer, "accelerator_exporter_allocation_source_up", "Whether the latest device allocation fact read succeeded.", boolFloat(current.allocationSourceUp))
	writeCounter(writer, "accelerator_exporter_allocation_errors_total", "Total device allocation fact read errors.", current.allocationErrorsTotal)

	writeTargetMetricHeader(writer, "accelerator_exporter_contract_violations_total", "Total samples that breached the accelerator metric contract, by reason.", "counter")
	for _, reason := range sortedCounterKeys(current.contractViolations) {
		_, _ = fmt.Fprintf(writer, "accelerator_exporter_contract_violations_total{reason=\"%s\"} %d\n", escapeLabelValue(reason), current.contractViolations[reason])
	}

	names := make([]string, 0, len(current.targets))
	for name := range current.targets {
		names = append(names, name)
	}
	sort.Strings(names)
	writeTargetMetricHeader(writer, "accelerator_exporter_target_up", "Whether the latest direct scrape of a configured source succeeded.", "gauge")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_target_up", name, target.rule, boolFloat(target.up))
	}
	writeTargetMetricHeader(writer, "accelerator_exporter_target_scrape_duration_seconds", "Duration of the latest direct source scrape.", "gauge")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_target_scrape_duration_seconds", name, target.rule, target.durationSeconds)
	}
	writeTargetMetricHeader(writer, "accelerator_exporter_target_samples_read", "Number of raw samples read from a source in the latest scrape.", "gauge")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_target_samples_read", name, target.rule, float64(target.samplesRead))
	}
	writeTargetMetricHeader(writer, "accelerator_exporter_scrape_errors_total", "Total direct source scrape errors.", "counter")
	for _, name := range names {
		target := current.targets[name]
		writeTargetMetric(writer, "accelerator_exporter_scrape_errors_total", name, target.rule, float64(target.scrapeErrorsTotal))
	}

	lastMetric := ""
	for _, sample := range current.series {
		if sample.Name != lastMetric {
			metadata, ok := accelerator.CanonicalMetric(sample.Name)
			if !ok {
				continue
			}
			_, _ = fmt.Fprintf(writer, "# HELP %s %s\n# TYPE %s %s\n", sample.Name, metadata.Help, sample.Name, metadata.Type)
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

func countRawSamples(families accelerator.Families) int {
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

func mergeCounters(current, addition map[string]uint64) map[string]uint64 {
	merged := make(map[string]uint64, len(current)+len(addition))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range addition {
		merged[key] += value
	}
	return merged
}

func sortedCounterKeys(values map[string]uint64) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validTargets(sources []accelerator.Source) ([]accelerator.Source, error) {
	valid := make([]accelerator.Source, 0, len(sources))
	seen := map[string]struct{}{}
	var targetErrors []error
	for _, source := range sources {
		if source.Name == "" || source.Address == "" {
			targetErrors = append(targetErrors, fmt.Errorf("invalid discovered target %q", source.Name))
			continue
		}
		if _, ok := accelerator.RuleByName(source.Rule); !ok {
			targetErrors = append(targetErrors, fmt.Errorf(
				"discovered target %q has unknown rule %q, expected one of %s",
				source.Name, source.Rule, strings.Join(accelerator.RuleNames(), ", "),
			))
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

func sampleKey(sample accelerator.Sample) string {
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

func writeTargetMetric(writer http.ResponseWriter, metricName, sourceName, rule string, value float64) {
	labels := map[string]string{"source": sourceName, "rule": rule}
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
