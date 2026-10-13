package observability

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	PutLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "content_put_latency_seconds",
		Help:    "Put latency by tenant and tier.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tenant", "tier"})

	GetLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "content_get_latency_seconds",
		Help:    "Get latency by tenant and tier.",
		Buckets: prometheus.DefBuckets,
	}, []string{"tenant", "tier"})

	PutErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "content_put_errors_total",
		Help: "Put failures by tenant.",
	}, []string{"tenant"})

	GetErrors = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "content_get_errors_total",
		Help: "Get failures by tenant.",
	}, []string{"tenant"})

	IntegrityFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "content_integrity_failures_total",
		Help: "Digest mismatches found by reads or the integrity job, by tenant.",
	}, []string{"tenant"})

	DispositionDeleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "content_disposition_deleted_total",
		Help: "Artifacts physically deleted by the disposition job, by tenant.",
	}, []string{"tenant"})

	DispositionSkipped = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "content_disposition_skipped_total",
		Help: "Disposition candidates skipped (legal hold, live reference), by tenant and reason.",
	}, []string{"tenant", "reason"})

	DispositionDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "content_disposition_duration_seconds",
		Help:    "Disposition sweep duration.",
		Buckets: prometheus.DefBuckets,
	})

	OrphanCandidates = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "content_orphan_candidates",
		Help: "Orphan candidates found by the last sweeper run, by tenant and kind.",
	}, []string{"tenant", "kind"})

	OrphanDeleted = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "content_orphan_deleted_total",
		Help: "Orphan objects physically deleted by the sweeper, by tenant.",
	}, []string{"tenant"})

	IntegrityChecked = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "content_integrity_checked_total",
		Help: "Artifacts verified by the integrity job, by tenant.",
	}, []string{"tenant"})

	ParquetCandidates = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "content_parquet_candidates",
		Help: "Parquet-tier artifacts awaiting compaction, by tenant.",
	}, []string{"tenant"})
)
