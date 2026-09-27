// Package client implements the Client service: it drives speed tests
// against a Target service and exposes them over an HTTP API.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"speedtest/internal/auth"
	"speedtest/internal/protocol"
)

// Phase names a stage of a speed test.
type Phase string

const (
	PhaseLatency  Phase = "latency"
	PhaseDownload Phase = "download"
	PhaseUpload   Phase = "upload"
)

const (
	sampleInterval = 250 * time.Millisecond
	// uploadRequestCap bounds one upload request body; streams that exhaust
	// it before the deadline start another request.
	uploadRequestCap int64 = 1 << 30
)

// Config tunes a test. Zero fields take defaults.
type Config struct {
	Streams   int     `json:"streams"`
	DurationS float64 `json:"duration_s"`
	WarmupS   float64 `json:"warmup_s"`
	Pings     int     `json:"pings"`
}

// DefaultConfig is used for any field left zero in a request.
var DefaultConfig = Config{Streams: 4, DurationS: 8, WarmupS: 1, Pings: 20}

// Normalize fills zero fields with defaults and validates the result.
func (c *Config) Normalize() error {
	if c.Streams == 0 {
		c.Streams = DefaultConfig.Streams
	}
	if c.DurationS == 0 {
		c.DurationS = DefaultConfig.DurationS
	}
	if c.WarmupS == 0 {
		c.WarmupS = DefaultConfig.WarmupS
	}
	if c.Pings == 0 {
		c.Pings = DefaultConfig.Pings
	}
	switch {
	case c.Streams < 1 || c.Streams > 16:
		return errors.New("streams must be in [1, 16]")
	case c.DurationS < 1 || c.DurationS > 30:
		return errors.New("duration_s must be in [1, 30]")
	case c.WarmupS < 0 || c.WarmupS >= c.DurationS:
		return errors.New("warmup_s must be in [0, duration_s)")
	case c.Pings < 1 || c.Pings > 100:
		return errors.New("pings must be in [1, 100]")
	}
	return nil
}

func (c Config) duration() time.Duration { return seconds(c.DurationS) }
func (c Config) warmup() time.Duration   { return seconds(c.WarmupS) }

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// Progress is a live snapshot of a running throughput phase.
type Progress struct {
	Phase     Phase   `json:"phase"`
	Mbps      float64 `json:"mbps"`
	ElapsedS  float64 `json:"elapsed_s"`
	DurationS float64 `json:"duration_s"`
}

// Hooks receive events while a test runs. Any of them may be nil.
type Hooks struct {
	Phase      func(Phase)
	Progress   func(Progress)
	Latency    func(LatencyResult)
	Throughput func(Phase, ThroughputResult)
}

// Runner executes speed tests against one Target.
type Runner struct {
	targetURL string
	token     string
	http      *http.Client
}

// NewRunner returns a Runner for the Target at targetURL.
func NewRunner(targetURL, token string) *Runner {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConnsPerHost = 32
	// Payload is random anyway; don't let the transport negotiate gzip.
	tr.DisableCompression = true
	return &Runner{targetURL: targetURL, token: token, http: &http.Client{Transport: tr}}
}

// TargetURL returns the Target this Runner measures against.
func (r *Runner) TargetURL() string { return r.targetURL }

// Run performs latency, download and upload measurements in order.
func (r *Runner) Run(ctx context.Context, cfg Config, h Hooks) error {
	emit(h.Phase, PhaseLatency)
	lat, err := r.measureLatency(ctx, cfg.Pings)
	if err != nil {
		return fmt.Errorf("latency: %w", err)
	}
	emit(h.Latency, lat)

	for _, p := range []struct {
		phase  Phase
		stream streamFunc
	}{
		{PhaseDownload, r.downloadStream},
		{PhaseUpload, r.uploadStream},
	} {
		emit(h.Phase, p.phase)
		res, err := r.measureThroughput(ctx, p.phase, cfg, h, p.stream)
		if err != nil {
			return fmt.Errorf("%s: %w", p.phase, err)
		}
		if h.Throughput != nil {
			h.Throughput(p.phase, res)
		}
	}
	return nil
}

func emit[T any](f func(T), v T) {
	if f != nil {
		f(v)
	}
}

func (r *Runner) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, r.targetURL+path, body)
	if err != nil {
		return nil, err
	}
	auth.SetBearer(req, r.token)
	return req, nil
}

func (r *Runner) ping(ctx context.Context) (time.Duration, error) {
	req, err := r.newRequest(ctx, http.MethodGet, protocol.PathPing, nil)
	if err != nil {
		return 0, err
	}
	start := time.Now()
	resp, err := r.http.Do(req)
	if err != nil {
		return 0, err
	}
	rtt := time.Since(start)
	// Drain so the connection goes back to the pool for the next ping.
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return 0, statusError(resp)
	}
	return rtt, nil
}

func (r *Runner) measureLatency(ctx context.Context, n int) (LatencyResult, error) {
	// Two unmeasured pings open and warm the keep-alive connection so the
	// samples reflect round trips rather than TCP handshakes.
	for range 2 {
		if _, err := r.ping(ctx); err != nil {
			return LatencyResult{}, err
		}
	}
	samples := make([]time.Duration, 0, n)
	for range n {
		rtt, err := r.ping(ctx)
		if err != nil {
			return LatencyResult{}, err
		}
		samples = append(samples, rtt)
	}
	return summarizeLatency(samples), nil
}

// streamFunc moves data for one parallel stream until deadline, adding every
// transferred byte to counted. It returns a non-nil error only on failure.
type streamFunc func(ctx context.Context, deadline time.Time, counted, confirmed *atomic.Int64) error

// measureThroughput runs cfg.Streams copies of stream in parallel, samples
// the shared byte counter every sampleInterval and computes the rate over the
// post-warm-up window. Any stream failing aborts the whole phase.
func (r *Runner) measureThroughput(ctx context.Context, phase Phase, cfg Config, h Hooks, stream streamFunc) (ThroughputResult, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var counted, confirmed atomic.Int64
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once

	start := time.Now()
	deadline := start.Add(cfg.duration())
	for range cfg.Streams {
		wg.Go(func() {
			if err := stream(ctx, deadline, &counted, &confirmed); err != nil {
				errOnce.Do(func() { firstErr = err })
				cancel()
			}
		})
	}

	samples := []sample{{0, 0}}
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
loop:
	for {
		select {
		case <-ticker.C:
			samples = append(samples, sample{time.Since(start), counted.Load()})
			if h.Progress != nil {
				h.Progress(Progress{
					Phase:     phase,
					Mbps:      round3(recentMbps(samples)),
					ElapsedS:  round3(time.Since(start).Seconds()),
					DurationS: cfg.DurationS,
				})
			}
		case <-timer.C:
			samples = append(samples, sample{time.Since(start), counted.Load()})
			break loop
		case <-ctx.Done():
			break loop
		}
	}
	wg.Wait()

	if firstErr != nil {
		return ThroughputResult{}, firstErr
	}
	if err := ctx.Err(); err != nil {
		return ThroughputResult{}, err
	}

	b, d := window(samples, cfg.warmup())
	return ThroughputResult{
		Mbps:                 round3(mbps(b, d)),
		Streams:              cfg.Streams,
		TotalBytes:           counted.Load(),
		MeasuredBytes:        b,
		MeasuredSeconds:      round3(d.Seconds()),
		ServerConfirmedBytes: confirmed.Load(),
		SeriesMbps:           series(samples),
		IntervalMS:           int(sampleInterval.Milliseconds()),
	}, nil
}

// recentMbps is the rate over roughly the last second, for live display.
func recentMbps(s []sample) float64 {
	last := s[len(s)-1]
	first := s[max(0, len(s)-1-int(time.Second/sampleInterval))]
	return mbps(last.bytes-first.bytes, last.at-first.at)
}

// downloadStream repeatedly fetches the largest allowed blob, counting bytes
// as they arrive, until the deadline cancels the request.
func (r *Runner) downloadStream(ctx context.Context, deadline time.Time, counted, _ *atomic.Int64) error {
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	path := protocol.PathDownload + "?bytes=" + strconv.FormatInt(protocol.MaxDownloadBytes, 10)
	buf := make([]byte, 256<<10)
	for ctx.Err() == nil {
		req, err := r.newRequest(ctx, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		resp, err := r.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return statusError(resp)
		}
		for {
			n, err := resp.Body.Read(buf)
			counted.Add(int64(n))
			if err != nil {
				break
			}
		}
		resp.Body.Close()
	}
	return nil
}

// uploadStream repeatedly POSTs a body that ends itself at the deadline, so
// each request completes normally and the Target can report what it got.
func (r *Runner) uploadStream(ctx context.Context, deadline time.Time, counted, confirmed *atomic.Int64) error {
	// Bodies stop at the deadline; the grace period only covers the Target's
	// reply, so a hung Target can't stall the test forever.
	ctx, cancel := context.WithDeadline(ctx, deadline.Add(15*time.Second))
	defer cancel()

	for time.Now().Before(deadline) {
		body := &timedBody{deadline: deadline, remaining: uploadRequestCap, counted: counted}
		req, err := r.newRequest(ctx, http.MethodPost, protocol.PathUpload, body)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := r.http.Do(req)
		if err != nil {
			return err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return statusError(resp)
		}
		var res protocol.UploadResult
		err = json.NewDecoder(resp.Body).Decode(&res)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("decode upload result: %w", err)
		}
		confirmed.Add(res.BytesReceived)
	}
	return nil
}

// uploadPayload is shared, incompressible-looking filler for upload bodies.
var uploadPayload = func() []byte {
	p := make([]byte, 256<<10)
	// A cheap xorshift is plenty: the goal is defeating compression, not
	// cryptographic quality.
	x := uint64(0x9E3779B97F4A7C15)
	for i := range p {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		p[i] = byte(x)
	}
	return p
}()

// timedBody is an io.Reader that yields payload bytes until the deadline
// passes or remaining hits zero, counting everything it hands out.
type timedBody struct {
	deadline  time.Time
	remaining int64
	offset    int
	counted   *atomic.Int64
}

func (b *timedBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 || !time.Now().Before(b.deadline) {
		return 0, io.EOF
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n := 0
	for n < len(p) {
		c := copy(p[n:], uploadPayload[b.offset:])
		n += c
		b.offset = (b.offset + c) % len(uploadPayload)
	}
	b.remaining -= int64(n)
	b.counted.Add(int64(n))
	return n, nil
}

func statusError(resp *http.Response) error {
	var e protocol.Error
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&e)
	if e.Error != "" {
		return fmt.Errorf("target returned %s: %s", resp.Status, e.Error)
	}
	return fmt.Errorf("target returned %s", resp.Status)
}
