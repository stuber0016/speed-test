package client

import (
	"math"
	"testing"
	"time"
)

func TestSummarizeLatency(t *testing.T) {
	ms := func(v ...int) []time.Duration {
		out := make([]time.Duration, len(v))
		for i, x := range v {
			out[i] = time.Duration(x) * time.Millisecond
		}
		return out
	}

	got := summarizeLatency(ms(10, 20, 10, 40, 20))
	want := LatencyResult{
		Samples:  5,
		MinMS:    10,
		AvgMS:    20,
		MedianMS: 20,
		P95MS:    36, // rank 3.8 between 20 and 40
		MaxMS:    40,
		JitterMS: 17.5, // (10+10+30+20)/4
	}
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	if (summarizeLatency(nil) != LatencyResult{}) {
		t.Fatal("empty input should give zero result")
	}
	if one := summarizeLatency(ms(7)); one.JitterMS != 0 || one.MedianMS != 7 {
		t.Fatalf("single sample: %+v", one)
	}
}

func TestPercentile(t *testing.T) {
	s := []float64{1, 2, 3, 4}
	for p, want := range map[float64]float64{0: 1, 50: 2.5, 100: 4} {
		if got := percentile(s, p); math.Abs(got-want) > 1e-9 {
			t.Errorf("p%v = %v, want %v", p, got, want)
		}
	}
}

func TestWindowSkipsWarmup(t *testing.T) {
	s := []sample{
		{0, 0},
		{500 * time.Millisecond, 100},  // slow start
		{1000 * time.Millisecond, 300}, // window starts here
		{2000 * time.Millisecond, 1300},
		{3000 * time.Millisecond, 2300},
	}
	b, d := window(s, time.Second)
	if b != 2000 || d != 2*time.Second {
		t.Fatalf("window = %d bytes over %v", b, d)
	}
	// 2000 bytes over 2 s = 8000 bit/s.
	if got := mbps(b, d); math.Abs(got-0.008) > 1e-12 {
		t.Fatalf("mbps = %v", got)
	}
}

func TestWindowFallsBackWhenTooShort(t *testing.T) {
	s := []sample{{0, 0}, {300 * time.Millisecond, 50}}
	b, d := window(s, time.Second)
	if b != 50 || d != 300*time.Millisecond {
		t.Fatalf("window = %d bytes over %v", b, d)
	}
}
