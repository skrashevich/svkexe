package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// ContainersTotal tracks the number of containers by status.
	ContainersTotal = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "svkexe_containers_total",
		Help: "Total number of containers by status.",
	}, []string{"status"})

	// HTTPRequestsTotal counts HTTP requests by method, path, and status code.
	HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "svkexe_http_requests_total",
		Help: "Total number of HTTP requests.",
	}, []string{"method", "path", "status"})

	// HTTPRequestDuration measures HTTP request duration in seconds.
	HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "svkexe_http_request_duration_seconds",
		Help:    "HTTP request duration in seconds.",
		Buckets: prometheus.DefBuckets,
	}, []string{"method", "path"})

	// ProxyRequestsTotal counts proxy requests forwarded to containers.
	ProxyRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "svkexe_proxy_requests_total",
		Help: "Total number of proxy requests forwarded to containers.",
	}, []string{"container"})

	// SSHSessionsActive tracks the number of active SSH sessions.
	SSHSessionsActive = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "svkexe_ssh_sessions_active",
		Help: "Number of currently active SSH sessions.",
	})
)
