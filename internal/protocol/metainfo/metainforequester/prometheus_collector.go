package metainforequester

import (
	"context"
	"net/netip"
	"time"

	"github.com/bitmagnet-io/bitmagnet/internal/protocol"
	"github.com/prometheus/client_golang/prometheus"
)

type prometheusCollector struct {
	requester               Requester
	requestDuration         prometheus.Histogram
	requestSuccessTotal     *prometheus.CounterVec
	requestErrorTotal       *prometheus.CounterVec
	requestConcurrency      prometheus.Gauge
}

const (
	namespace = "bitmagnet"
	subsystem = "meta_info_requester"
)

var ipVersionLabelNames = []string{"ip_version"}

func newPrometheusCollector(requester Requester) *prometheusCollector {
	return &prometheusCollector{
		requester: requester,
		requestDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "duration_seconds",
			Help:      "Duration of successful meta info requests in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),
		requestSuccessTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "success_total",
			Help:      "Total number of successful meta info requests.",
		}, ipVersionLabelNames),
		requestErrorTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "error_total",
			Help:      "Total number of failed meta info requests.",
		}, ipVersionLabelNames),
		requestConcurrency: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "concurrency",
			Help:      "Number of concurrent meta info requests.",
		}),
	}
}

func (l prometheusCollector) Request(ctx context.Context, infoHash protocol.ID, addr netip.AddrPort) (Response, error) {
	l.requestConcurrency.Inc()

	start := time.Now()
	resp, err := l.requester.Request(ctx, infoHash, addr)
	l.requestConcurrency.Dec()

	ipVersion := "6"
	if addr.Addr().Is4() {
		ipVersion = "4"
	}

	if err == nil {
		l.requestDuration.Observe(time.Since(start).Seconds())
		l.requestSuccessTotal.WithLabelValues(ipVersion).Inc()
	} else {
		l.requestErrorTotal.WithLabelValues(ipVersion).Inc()
	}

	return resp, err
}
