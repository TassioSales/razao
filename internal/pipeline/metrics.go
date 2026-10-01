package pipeline

import (
	"slices"
	"sync"
	"time"
)

const (
	window  = 120   // seconds of per-second history
	samples = 16384 // latency samples kept
)

// Metrics tracks what the writer is doing.
type Metrics struct {
	mu        sync.Mutex
	committed int64
	rejected  int64
	replayed  int64
	failed    int64
	batches   int64
	commitNs  int64
	perSecond [window]int64 // committed transactions in each second
	stamp     [window]int64 // unix second each slot belongs to
	lat       [samples]int32
	latN      int
	lastBatch int
}

// NewMetrics returns empty metrics.
func NewMetrics() *Metrics { return &Metrics{} }

func (m *Metrics) latency(d time.Duration) {
	m.mu.Lock()
	m.lat[m.latN%samples] = int32(min(d.Microseconds(), 1<<31-1))
	m.latN++
	m.mu.Unlock()
}

func (m *Metrics) batchDone(now time.Time, size, committed, rejected, replayed int, took time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.committed += int64(committed)
	m.rejected += int64(rejected)
	m.replayed += int64(replayed)
	m.batches++
	m.commitNs += took.Nanoseconds()
	m.lastBatch = size
	sec := now.Unix()
	i := sec % window
	if m.stamp[i] != sec {
		m.stamp[i], m.perSecond[i] = sec, 0
	}
	m.perSecond[i] += int64(committed)
}

func (m *Metrics) batchFailed(size int) {
	m.mu.Lock()
	m.failed += int64(size)
	m.mu.Unlock()
}

// Snapshot is a point-in-time view of the metrics.
type Snapshot struct {
	Committed   int64   `json:"committed"`
	Rejected    int64   `json:"rejected"`
	Replayed    int64   `json:"replayed"`
	Failed      int64   `json:"failed"`
	Batches     int64   `json:"batches"`
	AvgBatch    float64 `json:"avgBatch"`
	LastBatch   int     `json:"lastBatch"`
	AvgCommitMs float64 `json:"avgCommitMs"`
	TPS         int64   `json:"tps"` // last full second
	P50Ms       float64 `json:"p50Ms"`
	P99Ms       float64 `json:"p99Ms"`
	MaxMs       float64 `json:"maxMs"`
	Series      []int64 `json:"series"` // committed per second, oldest first, last 60 full seconds
}

// Snapshot computes rates and percentiles.
func (m *Metrics) Snapshot() Snapshot {
	m.mu.Lock()
	s := Snapshot{
		Committed: m.committed, Rejected: m.rejected, Replayed: m.replayed, Failed: m.failed,
		Batches: m.batches, LastBatch: m.lastBatch, Series: make([]int64, 60),
	}
	if m.batches > 0 {
		s.AvgBatch = float64(m.committed+m.rejected+m.replayed) / float64(m.batches)
		s.AvgCommitMs = float64(m.commitNs) / float64(m.batches) / 1e6
	}
	now := time.Now().Unix()
	for k := range 60 {
		sec := now - 60 + int64(k) // full seconds only: the current one is still filling
		if i := sec % window; m.stamp[i] == sec {
			s.Series[k] = m.perSecond[i]
		}
	}
	s.TPS = s.Series[59]
	n := min(m.latN, samples)
	lat := slices.Clone(m.lat[:n])
	m.mu.Unlock()

	if n > 0 {
		slices.Sort(lat)
		s.P50Ms = float64(lat[n/2]) / 1000
		s.P99Ms = float64(lat[min(n-1, n*99/100)]) / 1000
		s.MaxMs = float64(lat[n-1]) / 1000
	}
	return s
}

// ResetLatency clears the latency samples, so a new measurement doesn't mix
// with an old one.
func (m *Metrics) ResetLatency() {
	m.mu.Lock()
	m.latN = 0
	m.mu.Unlock()
}
