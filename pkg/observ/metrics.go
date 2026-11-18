package observ

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gomods/athens/pkg/errors"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Attribute keys used on Athens' custom metrics.
const (
	attrCacheResult = "cache_result"
	attrCacheType   = "cache_type"
	attrFetchResult = "fetch_result"
	attrListResult  = "list_result"
	attrLister      = "lister"
	attrEndpoint    = "endpoint"
)

// upstreamExponentialBuckets are the histogram boundaries (in seconds) for
// upstream fetch latency.
var upstreamExponentialBuckets = []float64{0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30}

// waitBuckets are the histogram boundaries (in seconds) for time spent waiting
// for a concurrency slot.
var waitBuckets = []float64{0.001, 0.01, 0.05, 0.1, 0.5, 1, 5, 30}

// Custom instruments. They are nil until initMetrics runs (i.e. when the stats
// exporter is registered); the Record* helpers guard against that so recording
// is a no-op when metrics are disabled, mirroring OpenCensus' behavior.
var (
	cacheLookupCounter    metric.Int64Counter
	upstreamFetchCounter  metric.Int64Counter
	upstreamFetchDuration metric.Float64Histogram
	upstreamListDuration  metric.Float64Histogram
	upstreamListWait      metric.Float64Histogram
	upstreamListInFlight  metric.Int64UpDownCounter
)

// initMetrics creates Athens' custom instruments from the global MeterProvider.
// It must be called after the MeterProvider has been installed.
func initMetrics() error {
	const op errors.Op = "observ.initMetrics"
	meter := otel.Meter(instrumentationName)

	var err error
	cacheLookupCounter, err = meter.Int64Counter(
		"cache_lookup_total",
		metric.WithDescription("Count of cache lookup results"),
	)
	if err != nil {
		return errors.E(op, err)
	}

	upstreamFetchCounter, err = meter.Int64Counter(
		"upstream_fetch_total",
		metric.WithDescription("Count of upstream fetch attempts"),
	)
	if err != nil {
		return errors.E(op, err)
	}

	upstreamFetchDuration, err = meter.Float64Histogram(
		"upstream_fetch_duration_seconds",
		metric.WithDescription("Distribution of upstream fetch latency in seconds"),
		metric.WithExplicitBucketBoundaries(upstreamExponentialBuckets...),
	)
	if err != nil {
		return errors.E(op, err)
	}

	upstreamListDuration, err = meter.Float64Histogram(
		"upstream_list_duration_seconds",
		metric.WithDescription("Distribution of upstream version lookup latency in seconds"),
		metric.WithExplicitBucketBoundaries(upstreamExponentialBuckets...),
	)
	if err != nil {
		return errors.E(op, err)
	}

	upstreamListWait, err = meter.Float64Histogram(
		"upstream_list_wait_seconds",
		metric.WithDescription("Time upstream version lookups waited for a ProtocolWorkers slot in seconds"),
		metric.WithExplicitBucketBoundaries(waitBuckets...),
	)
	if err != nil {
		return errors.E(op, err)
	}

	upstreamListInFlight, err = meter.Int64UpDownCounter(
		"upstream_list_in_flight",
		metric.WithDescription("Number of upstream version lookups currently running"),
	)
	if err != nil {
		return errors.E(op, err)
	}

	return nil
}

func RecordCacheLookup(ctx context.Context, result, typ string) {
	if cacheLookupCounter == nil {
		return
	}
	cacheLookupCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrCacheResult, result),
		attribute.String(attrCacheType, typ),
	))
}

func RecordUpstreamFetch(ctx context.Context, result string) {
	if upstreamFetchCounter == nil {
		return
	}
	upstreamFetchCounter.Add(ctx, 1, metric.WithAttributes(
		attribute.String(attrFetchResult, result),
	))
}

func RecordUpstreamFetchDuration(ctx context.Context, result string, duration time.Duration) {
	if upstreamFetchDuration == nil {
		return
	}
	upstreamFetchDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String(attrFetchResult, result),
	))
}

// RecordUpstreamList records one upstream version lookup. lister is "go"
// (a go list process) or "http" (the upstream proxy's HTTP API); result is one
// of ok, not_found, timeout, canceled or error.
func RecordUpstreamList(ctx context.Context, lister, result string, duration time.Duration) {
	if upstreamListDuration == nil {
		return
	}
	upstreamListDuration.Record(ctx, duration.Seconds(), metric.WithAttributes(
		attribute.String(attrLister, lister),
		attribute.String(attrListResult, result),
	))
}

// RecordUpstreamListWait records how long a lookup waited for a slot.
func RecordUpstreamListWait(ctx context.Context, duration time.Duration) {
	if upstreamListWait == nil {
		return
	}
	upstreamListWait.Record(ctx, duration.Seconds())
}

// AddUpstreamListInFlight adjusts the number of running upstream lookups.
func AddUpstreamListInFlight(ctx context.Context, delta int64) {
	if upstreamListInFlight == nil {
		return
	}
	upstreamListInFlight.Add(ctx, delta)
}

// EndpointAttributes labels HTTP server metrics with a low-cardinality
// endpoint name derived from the request path.
func EndpointAttributes(r *http.Request) []attribute.KeyValue {
	return []attribute.KeyValue{attribute.String(attrEndpoint, endpoint(r.URL.Path))}
}

func endpoint(path string) string {
	switch {
	case strings.HasSuffix(path, "/@v/list"):
		return "list"
	case strings.HasSuffix(path, "/@latest"):
		return "latest"
	case strings.HasSuffix(path, ".info"):
		return "info"
	case strings.HasSuffix(path, ".mod"):
		return "mod"
	case strings.HasSuffix(path, ".zip"):
		return "zip"
	case strings.Contains(path, "/sumdb/"):
		return "sumdb"
	case strings.HasPrefix(path, "/dl/"):
		return "toolchain"
	case path == "/healthz" || path == "/readyz":
		return "health"
	case path == "/metrics":
		return "metrics"
	default:
		return "other"
	}
}
