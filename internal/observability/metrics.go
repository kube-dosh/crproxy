// Package observability exposes a small, dependency-free Prometheus endpoint
// with bounded label cardinality and live registry/storage gauges.
package observability

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kube-dosh/crproxy/internal/storage"
	"github.com/kube-dosh/crproxy/internal/store"
	"github.com/kube-dosh/crproxy/internal/upstreammgr"
)

var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

type routeKey struct {
	method string
	route  string
}

type statusKey struct {
	method string
	route  string
	status int
}

type durationValue struct {
	count   uint64
	sum     float64
	buckets []uint64
}

type Collector struct {
	store     *store.Store
	storage   *storage.Manager
	upstreams *upstreammgr.Manager
	version   string
	startedAt time.Time

	mu          sync.Mutex
	statusCount map[statusKey]uint64
	durations   map[routeKey]*durationValue
}

func New(metadata *store.Store, storageManager *storage.Manager, upstreams *upstreammgr.Manager, version string, startedAt time.Time) (*Collector, error) {
	if metadata == nil || storageManager == nil || upstreams == nil {
		return nil, fmt.Errorf("metrics collector dependencies are required")
	}
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return &Collector{
		store:       metadata,
		storage:     storageManager,
		upstreams:   upstreams,
		version:     version,
		startedAt:   startedAt.UTC(),
		statusCount: map[statusKey]uint64{},
		durations:   map[routeKey]*durationValue{},
	}, nil
}

func (c *Collector) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &responseRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)
		elapsed := time.Since(started).Seconds()
		route := classifyRoute(r.URL.Path)
		c.observe(r.Method, route, recorder.status, elapsed)
	})
}

func (c *Collector) observe(method, route string, status int, seconds float64) {
	if method == "" {
		method = http.MethodGet
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.statusCount[statusKey{method: method, route: route, status: status}]++
	key := routeKey{method: method, route: route}
	value := c.durations[key]
	if value == nil {
		value = &durationValue{buckets: make([]uint64, len(durationBuckets))}
		c.durations[key] = value
	}
	value.count++
	value.sum += seconds
	for index, upper := range durationBuckets {
		if seconds <= upper {
			value.buckets[index]++
		}
	}
}

func (c *Collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	writer := bufio.NewWriter(w)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	writeMetricHeader(writer, "crproxy_build_info", "Build information for crproxy.", "gauge")
	fmt.Fprintf(writer, "crproxy_build_info{version=%q} 1\n", escapeLabel(c.version))
	writeMetricHeader(writer, "crproxy_uptime_seconds", "Process uptime in seconds.", "gauge")
	fmt.Fprintf(writer, "crproxy_uptime_seconds %.3f\n", time.Since(c.startedAt).Seconds())

	c.writeHTTPMetrics(writer)

	summary, summaryErr := c.storage.Summary(ctx)
	if summaryErr == nil {
		writeMetricHeader(writer, "crproxy_storage_physical_bytes", "Physical bytes managed by the registry.", "gauge")
		fmt.Fprintf(writer, "crproxy_storage_physical_bytes %d\n", summary.Usage.PhysicalBytes)
		writeMetricHeader(writer, "crproxy_storage_local_bytes", "Physical bytes referenced by local repositories.", "gauge")
		fmt.Fprintf(writer, "crproxy_storage_local_bytes %d\n", summary.Usage.LocalBytes)
		writeMetricHeader(writer, "crproxy_storage_cache_bytes", "Physical bytes referenced exclusively by proxy caches.", "gauge")
		fmt.Fprintf(writer, "crproxy_storage_cache_bytes %d\n", summary.Usage.CacheBytes)
		writeMetricHeader(writer, "crproxy_storage_content_total", "Number of content records.", "gauge")
		fmt.Fprintf(writer, "crproxy_storage_content_total %d\n", summary.Usage.ContentCount)
		writeMetricHeader(writer, "crproxy_storage_max_bytes", "Configured logical storage limit.", "gauge")
		fmt.Fprintf(writer, "crproxy_storage_max_bytes %d\n", summary.Settings.MaxBytes)
		writeMetricHeader(writer, "crproxy_cache_max_bytes", "Configured proxy cache limit.", "gauge")
		fmt.Fprintf(writer, "crproxy_cache_max_bytes %d\n", summary.Settings.CacheMaxBytes)
		writeMetricHeader(writer, "crproxy_disk_available_bytes", "Available bytes on the storage filesystem.", "gauge")
		fmt.Fprintf(writer, "crproxy_disk_available_bytes %d\n", summary.Disk.AvailableBytes)
		writeMetricHeader(writer, "crproxy_active_downloads", "Currently active proxy downloads.", "gauge")
		fmt.Fprintf(writer, "crproxy_active_downloads %d\n", summary.ActiveDownloads)
		writeMetricHeader(writer, "crproxy_storage_temp_bytes", "Bytes held in temporary files.", "gauge")
		fmt.Fprintf(writer, "crproxy_storage_temp_bytes %d\n", summary.TempBytes)
		writeMetricHeader(writer, "crproxy_storage_state", "Storage state (0=normal, 1=high, 2=critical, 3=emergency).", "gauge")
		fmt.Fprintf(writer, "crproxy_storage_state %d\n", storageStateValue(summary.State))
		writeMetricHeader(writer, "crproxy_gc_running", "Whether a garbage collection cycle is running.", "gauge")
		if summary.GC.Running {
			fmt.Fprintln(writer, "crproxy_gc_running 1")
		} else {
			fmt.Fprintln(writer, "crproxy_gc_running 0")
		}
	}

	events, eventErr := c.store.EventSummarySince(ctx, time.Now().UTC().Add(-24*time.Hour))
	if eventErr == nil {
		writeMetricHeader(writer, "crproxy_registry_events_24h", "Registry events in the last 24 hours.", "gauge")
		fmt.Fprintf(writer, "crproxy_registry_events_24h{kind=\"pull_requests\"} %d\n", events.PullRequests)
		fmt.Fprintf(writer, "crproxy_registry_events_24h{kind=\"cache_hits\"} %d\n", events.CacheHits)
		fmt.Fprintf(writer, "crproxy_registry_events_24h{kind=\"cache_misses\"} %d\n", events.CacheMisses)
		fmt.Fprintf(writer, "crproxy_registry_events_24h{kind=\"upstream_errors\"} %d\n", events.UpstreamErrors)
		fmt.Fprintf(writer, "crproxy_registry_events_24h{kind=\"bytes_served\"} %d\n", events.BytesServed)
		fmt.Fprintf(writer, "crproxy_registry_events_24h{kind=\"bytes_cached\"} %d\n", events.BytesCached)
	}

	upstreams, upstreamErr := c.upstreams.List(ctx)
	if upstreamErr == nil {
		writeMetricHeader(writer, "crproxy_upstream_health", "Upstream health (0=unknown, 1=healthy, 2=unhealthy).", "gauge")
		writeMetricHeader(writer, "crproxy_upstream_enabled", "Whether an upstream is enabled.", "gauge")
		for _, upstream := range upstreams {
			enabled := 0
			if upstream.Enabled {
				enabled = 1
			}
			fmt.Fprintf(writer, "crproxy_upstream_health{id=%q,name=%q} %d\n", escapeLabel(upstream.ID), escapeLabel(upstream.Name), healthValue(upstream.HealthState))
			fmt.Fprintf(writer, "crproxy_upstream_enabled{id=%q,name=%q} %d\n", escapeLabel(upstream.ID), escapeLabel(upstream.Name), enabled)
		}
	}

	writeMetricHeader(writer, "crproxy_scrape_error", "Number of data sources that failed during this scrape.", "gauge")
	errors := 0
	if summaryErr != nil {
		errors++
	}
	if eventErr != nil {
		errors++
	}
	if upstreamErr != nil {
		errors++
	}
	fmt.Fprintf(writer, "crproxy_scrape_error %d\n", errors)
	_ = writer.Flush()
}

func (c *Collector) writeHTTPMetrics(w io.Writer) {
	c.mu.Lock()
	statuses := make([]statusKey, 0, len(c.statusCount))
	for key := range c.statusCount {
		statuses = append(statuses, key)
	}
	durations := make([]routeKey, 0, len(c.durations))
	for key := range c.durations {
		durations = append(durations, key)
	}
	statusSnapshot := make(map[statusKey]uint64, len(c.statusCount))
	for key, value := range c.statusCount {
		statusSnapshot[key] = value
	}
	durationSnapshot := make(map[routeKey]durationValue, len(c.durations))
	for key, value := range c.durations {
		durationSnapshot[key] = durationValue{count: value.count, sum: value.sum, buckets: append([]uint64(nil), value.buckets...)}
	}
	c.mu.Unlock()

	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].route != statuses[j].route {
			return statuses[i].route < statuses[j].route
		}
		if statuses[i].method != statuses[j].method {
			return statuses[i].method < statuses[j].method
		}
		return statuses[i].status < statuses[j].status
	})
	sort.Slice(durations, func(i, j int) bool {
		if durations[i].route != durations[j].route {
			return durations[i].route < durations[j].route
		}
		return durations[i].method < durations[j].method
	})

	writeMetricHeader(w, "crproxy_http_requests_total", "HTTP requests processed by method, route class and status.", "counter")
	for _, key := range statuses {
		fmt.Fprintf(w, "crproxy_http_requests_total{method=%q,route=%q,status=%q} %d\n",
			escapeLabel(key.method), escapeLabel(key.route), strconv.Itoa(key.status), statusSnapshot[key])
	}
	writeMetricHeader(w, "crproxy_http_request_duration_seconds", "HTTP request latency by method and route class.", "histogram")
	for _, key := range durations {
		value := durationSnapshot[key]
		for index, upper := range durationBuckets {
			fmt.Fprintf(w, "crproxy_http_request_duration_seconds_bucket{method=%q,route=%q,le=%q} %d\n",
				escapeLabel(key.method), escapeLabel(key.route), formatFloat(upper), value.buckets[index])
		}
		fmt.Fprintf(w, "crproxy_http_request_duration_seconds_bucket{method=%q,route=%q,le=\"+Inf\"} %d\n",
			escapeLabel(key.method), escapeLabel(key.route), value.count)
		fmt.Fprintf(w, "crproxy_http_request_duration_seconds_sum{method=%q,route=%q} %s\n",
			escapeLabel(key.method), escapeLabel(key.route), formatFloat(value.sum))
		fmt.Fprintf(w, "crproxy_http_request_duration_seconds_count{method=%q,route=%q} %d\n",
			escapeLabel(key.method), escapeLabel(key.route), value.count)
	}
}

func classifyRoute(path string) string {
	switch {
	case path == "/metrics":
		return "metrics"
	case path == "/api/v1/healthz", path == "/api/v1/readyz":
		return "health"
	case strings.HasPrefix(path, "/api/v1/auth/"):
		return "api_auth"
	case strings.HasPrefix(path, "/api/v1/repositories"):
		return "api_repositories"
	case strings.HasPrefix(path, "/api/v1/cache"):
		return "api_cache"
	case strings.HasPrefix(path, "/api/v1/storage"):
		return "api_storage"
	case strings.HasPrefix(path, "/api/v1/upstreams"):
		return "api_upstreams"
	case strings.HasPrefix(path, "/api/v1/users"), strings.HasPrefix(path, "/api/v1/tokens"):
		return "api_identity"
	case strings.HasPrefix(path, "/api/v1/audit"):
		return "api_audit"
	case strings.HasPrefix(path, "/api/v1/uploads"):
		return "api_uploads"
	case strings.HasPrefix(path, "/api/"):
		return "api_other"
	case path == "/v2/" || path == "/v2":
		return "registry_ping"
	case path == "/v2/token":
		return "registry_token"
	case strings.HasPrefix(path, "/v2/"):
		switch {
		case strings.Contains(path, "/manifests/"):
			return "registry_manifests"
		case strings.Contains(path, "/blobs/uploads/"):
			return "registry_uploads"
		case strings.Contains(path, "/blobs/"):
			return "registry_blobs"
		case strings.HasSuffix(path, "/tags/list"):
			return "registry_tags"
		case path == "/v2/_catalog":
			return "registry_catalog"
		default:
			return "registry_other"
		}
	case strings.HasPrefix(path, "/assets/"):
		return "ui_assets"
	case path == "/" || !strings.HasPrefix(path, "/api") && !strings.HasPrefix(path, "/v2"):
		return "ui"
	default:
		return "other"
	}
}

func storageStateValue(state string) int {
	switch state {
	case "high":
		return 1
	case "critical":
		return 2
	case "emergency":
		return 3
	default:
		return 0
	}
}

func healthValue(state string) int {
	switch state {
	case "healthy":
		return 1
	case "unhealthy":
		return 2
	default:
		return 0
	}
}

func writeMetricHeader(w io.Writer, name, help, metricType string) {
	fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return value
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(data)
}

func (r *responseRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *responseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *responseRecorder) Push(target string, options *http.PushOptions) error {
	if pusher, ok := r.ResponseWriter.(http.Pusher); ok {
		return pusher.Push(target, options)
	}
	return http.ErrNotSupported
}

func (r *responseRecorder) ReadFrom(reader io.Reader) (int64, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if readerFrom, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(reader)
	}
	return io.Copy(r.ResponseWriter, reader)
}

func (r *responseRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
