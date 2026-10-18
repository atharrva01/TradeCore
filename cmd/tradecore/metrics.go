package main

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics feeds both /metrics (Prometheus) and the admin stats page, which
// needs recent rates and percentiles rather than lifetime counters.
type metrics struct {
	registry    *prometheus.Registry
	orders      *prometheus.CounterVec
	rejections  *prometheus.CounterVec
	trades      prometheus.Counter
	matchTime   prometheus.Histogram
	commitTime  prometheus.Histogram
	commitBatch prometheus.Histogram
	wsClients   prometheus.Gauge

	mu        sync.Mutex
	perSecond [60]secondBucket
	latencies [2048]time.Duration
	nextLat   int
	filled    int
}

type secondBucket struct {
	second int64
	orders int
	trades int
}

func newMetrics() *metrics {
	m := &metrics{
		registry: prometheus.NewRegistry(),
		orders: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tradecore_orders_total", Help: "Orders accepted by the matching engine.",
		}, []string{"source"}),
		rejections: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "tradecore_order_rejections_total", Help: "Orders refused, by error code.",
		}, []string{"code"}),
		trades: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "tradecore_trades_total", Help: "Trades executed.",
		}),
		matchTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "tradecore_engine_match_seconds", Help: "Time the matching engine spends on one order.",
			Buckets: prometheus.ExponentialBuckets(250e-9, 2, 16),
		}),
		commitTime: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "tradecore_commit_seconds", Help: "Time to commit one group of changes to Postgres.",
			Buckets: prometheus.ExponentialBuckets(0.0005, 2, 14),
		}),
		commitBatch: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "tradecore_commit_batch_size", Help: "Changesets grouped into one commit.",
			Buckets: prometheus.ExponentialBuckets(1, 2, 8),
		}),
		wsClients: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "tradecore_websocket_clients", Help: "Connected WebSocket clients.",
		}),
	}
	m.registry.MustRegister(m.orders, m.rejections, m.trades, m.matchTime, m.commitTime, m.commitBatch, m.wsClients,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *metrics) handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

func (m *metrics) observeMatch(d time.Duration, bot bool) {
	source := "human"
	if bot {
		source = "bot"
	}
	m.orders.WithLabelValues(source).Inc()
	m.matchTime.Observe(d.Seconds())
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bucket(time.Now()).orders++
	m.latencies[m.nextLat] = d
	m.nextLat = (m.nextLat + 1) % len(m.latencies)
	m.filled = min(m.filled+1, len(m.latencies))
}

func (m *metrics) observeTrade() {
	m.trades.Inc()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bucket(time.Now()).trades++
}

// bucket returns the per-second bucket for now. The caller must hold m.mu.
func (m *metrics) bucket(now time.Time) *secondBucket {
	second := now.Unix()
	b := &m.perSecond[second%int64(len(m.perSecond))]
	if b.second != second {
		*b = secondBucket{second: second}
	}
	return b
}

type engineStats struct {
	OrdersPerSecond float64 `json:"ordersPerSecond"`
	TradesPerSecond float64 `json:"tradesPerSecond"`
	MatchP50Micros  float64 `json:"matchP50Micros"`
	MatchP99Micros  float64 `json:"matchP99Micros"`
	MatchSamples    int     `json:"matchSamples"`
}

// snapshot reports rates over the last minute and latency percentiles over
// the most recent orders.
func (m *metrics) snapshot(now time.Time) engineStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	orders, trades := 0, 0
	for _, b := range m.perSecond {
		if now.Unix()-b.second < int64(len(m.perSecond)) {
			orders += b.orders
			trades += b.trades
		}
	}
	samples := append([]time.Duration(nil), m.latencies[:m.filled]...)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	stats := engineStats{
		OrdersPerSecond: float64(orders) / float64(len(m.perSecond)),
		TradesPerSecond: float64(trades) / float64(len(m.perSecond)),
		MatchSamples:    len(samples),
	}
	if len(samples) > 0 {
		stats.MatchP50Micros = float64(samples[len(samples)/2].Nanoseconds()) / 1000
		stats.MatchP99Micros = float64(samples[len(samples)*99/100].Nanoseconds()) / 1000
	}
	return stats
}
