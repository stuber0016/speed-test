package client

import (
	"math"
	"slices"
	"time"
)

// LatencyResult summarises round-trip times of PathPing requests over an
// already-open keep-alive connection (so: RTT + HTTP overhead, no handshake).
type LatencyResult struct {
	Samples  int     `json:"samples"`
	MinMS    float64 `json:"min_ms"`
	AvgMS    float64 `json:"avg_ms"`
	MedianMS float64 `json:"median_ms"`
	P95MS    float64 `json:"p95_ms"`
	MaxMS    float64 `json:"max_ms"`
	// JitterMS is the mean absolute difference between consecutive samples
	// (the RFC 3550 idea, without the smoothing).
	JitterMS float64 `json:"jitter_ms"`
}

// ThroughputResult describes one download or upload phase.
type ThroughputResult struct {
	// Mbps is the headline number: bits per second over the measurement
	// window (the phase minus its warm-up), in megabits.
	Mbps            float64 `json:"mbps"`
	Streams         int     `json:"streams"`
	TotalBytes      int64   `json:"total_bytes"`
	MeasuredBytes   int64   `json:"measured_bytes"`
	MeasuredSeconds float64 `json:"measured_seconds"`
	// ServerConfirmedBytes is what the Target reported receiving (upload only).
	ServerConfirmedBytes int64 `json:"server_confirmed_bytes,omitempty"`
	// SeriesMbps is the rate per sampling interval, warm-up included.
	SeriesMbps []float64 `json:"series_mbps"`
	IntervalMS int       `json:"interval_ms"`
}

func summarizeLatency(samples []time.Duration) LatencyResult {
	if len(samples) == 0 {
		return LatencyResult{}
	}
	ms := make([]float64, len(samples))
	var sum, jitter float64
	for i, d := range samples {
		ms[i] = float64(d.Microseconds()) / 1000
		sum += ms[i]
		if i > 0 {
			jitter += math.Abs(ms[i] - ms[i-1])
		}
	}
	res := LatencyResult{Samples: len(ms), AvgMS: round3(sum / float64(len(ms)))}
	if len(ms) > 1 {
		res.JitterMS = round3(jitter / float64(len(ms)-1))
	}
	slices.Sort(ms)
	res.MinMS = ms[0]
	res.MaxMS = ms[len(ms)-1]
	res.MedianMS = round3(percentile(ms, 50))
	res.P95MS = round3(percentile(ms, 95))
	return res
}

// percentile returns the p-th percentile of sorted using linear interpolation
// between closest ranks.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	return sorted[lo] + (sorted[hi]-sorted[lo])*(rank-float64(lo))
}

// sample is a cumulative byte count observed at a point in a phase.
type sample struct {
	at    time.Duration
	bytes int64
}

// window returns the bytes and time between the first sample at or after
// warmup and the last sample. If the phase was too short to have a sample
// after warm-up, the whole phase is used instead.
func window(samples []sample, warmup time.Duration) (int64, time.Duration) {
	if len(samples) < 2 {
		return 0, 0
	}
	last := samples[len(samples)-1]
	base := samples[0]
	for _, s := range samples {
		if s.at >= warmup {
			base = s
			break
		}
	}
	if last.at-base.at <= 0 {
		base = samples[0]
	}
	return last.bytes - base.bytes, last.at - base.at
}

func mbps(bytes int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(bytes) * 8 / d.Seconds() / 1e6
}

// series converts cumulative samples to a per-interval rate.
func series(samples []sample) []float64 {
	out := make([]float64, 0, len(samples))
	for i := 1; i < len(samples); i++ {
		out = append(out, round3(mbps(samples[i].bytes-samples[i-1].bytes, samples[i].at-samples[i-1].at)))
	}
	return out
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
