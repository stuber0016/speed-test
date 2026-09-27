// Command stctl controls the Client service from a terminal.
//
//	stctl run  [-streams N] [-duration S] [-warmup S] [-pings N]
//	stctl list
//	stctl get  <id>
//
// Connection settings come from the environment, falling back to a .env file
// in the current directory, so the API key never has to be typed:
//
//	CLIENT_URL      Client service base URL (default "http://localhost:$CLIENT_PORT",
//	                with CLIENT_PORT defaulting to 8080)
//	CLIENT_API_KEY  bearer token for the Client service API (required)
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"text/tabwriter"
	"time"

	"speedtest/internal/client"
	"speedtest/internal/protocol"
)

const usage = `usage:
  stctl run  [-streams N] [-duration S] [-warmup S] [-pings N]   start a test and watch it
  stctl list                                                    show recent tests
  stctl get  <id>                                               show one test
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c, err := newAPIClient()
	if err == nil {
		switch cmd, args := os.Args[1], os.Args[2:]; cmd {
		case "run":
			err = runCmd(ctx, c, args)
		case "list":
			err = listCmd(ctx, c)
		case "get":
			if len(args) != 1 {
				fmt.Fprint(os.Stderr, usage)
				os.Exit(2)
			}
			err = getCmd(ctx, c, args[0])
		default:
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// --- commands ---------------------------------------------------------------

func runCmd(ctx context.Context, c *apiClient, args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	var cfg client.Config
	fs.IntVar(&cfg.Streams, "streams", 0, "parallel streams (default 4)")
	fs.Float64Var(&cfg.DurationS, "duration", 0, "seconds per throughput phase (default 8)")
	fs.Float64Var(&cfg.WarmupS, "warmup", 0, "seconds discarded at the start of each phase (default 1)")
	fs.IntVar(&cfg.Pings, "pings", 0, "latency samples (default 20)")
	_ = fs.Parse(args)

	var t client.Test
	if err := c.do(ctx, http.MethodPost, "/api/tests", cfg, &t); err != nil {
		return err
	}
	fmt.Printf("Speed test %s → %s  (%d streams, %gs per phase, %gs warm-up, %d pings)\n\n",
		t.ID, t.Target, t.Config.Streams, t.Config.DurationS, t.Config.WarmupS, t.Config.Pings)

	live := isTerminal(os.Stdout)
	printed := map[client.Phase]bool{}
	for {
		if err := c.do(ctx, http.MethodGet, "/api/tests/"+t.ID, nil, &t); err != nil {
			if errors.Is(err, context.Canceled) {
				fmt.Printf("\ninterrupted — the test keeps running on the server; check it with: stctl get %s\n", t.ID)
			}
			return err
		}
		for _, p := range []client.Phase{client.PhaseLatency, client.PhaseDownload, client.PhaseUpload} {
			if line := resultLine(t, p); line != "" && !printed[p] {
				if live {
					fmt.Print("\r\033[K")
				}
				fmt.Println(line)
				printed[p] = true
			}
		}
		if t.Status != client.StatusRunning {
			break
		}
		if live {
			fmt.Print("\r\033[K" + progressLine(t))
		}
		select {
		case <-ctx.Done():
			fmt.Printf("\ninterrupted — the test keeps running on the server; check it with: stctl get %s\n", t.ID)
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
	if live {
		fmt.Print("\r\033[K")
	}
	if t.Status == client.StatusFailed {
		return fmt.Errorf("test failed: %s", t.Error)
	}
	fmt.Println("\nDone.")
	return nil
}

func listCmd(ctx context.Context, c *apiClient) error {
	var tests []client.Test
	if err := c.do(ctx, http.MethodGet, "/api/tests", nil, &tests); err != nil {
		return err
	}
	if len(tests) == 0 {
		fmt.Println("No tests yet. Start one with: stctl run")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTARTED\tSTATUS\tLATENCY\tDOWNLOAD\tUPLOAD")
	for _, t := range tests {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.ID, t.StartedAt.Local().Format("2006-01-02 15:04:05"), t.Status,
			orDash(t.Latency != nil, func() string { return fmt.Sprintf("%.2f ms", t.Latency.MedianMS) }),
			orDash(t.Download != nil, func() string { return fmt.Sprintf("%.1f Mbit/s", t.Download.Mbps) }),
			orDash(t.Upload != nil, func() string { return fmt.Sprintf("%.1f Mbit/s", t.Upload.Mbps) }))
	}
	return tw.Flush()
}

func getCmd(ctx context.Context, c *apiClient, id string) error {
	var t client.Test
	if err := c.do(ctx, http.MethodGet, "/api/tests/"+id, nil, &t); err != nil {
		return err
	}
	fmt.Printf("Speed test %s → %s  started %s  status %s\n\n", t.ID, t.Target, t.StartedAt.Local().Format(time.DateTime), t.Status)
	for _, p := range []client.Phase{client.PhaseLatency, client.PhaseDownload, client.PhaseUpload} {
		if line := resultLine(t, p); line != "" {
			fmt.Println(line)
		}
	}
	if t.Status == client.StatusRunning {
		fmt.Println(progressLine(t))
	}
	if t.Error != "" {
		fmt.Println("\nerror:", t.Error)
	}
	return nil
}

// --- formatting -------------------------------------------------------------

func resultLine(t client.Test, p client.Phase) string {
	switch p {
	case client.PhaseLatency:
		if l := t.Latency; l != nil {
			return fmt.Sprintf("  ✓ Latency   %8.2f ms       min %.2f · p95 %.2f · jitter %.2f ms (%d samples)",
				l.MedianMS, l.MinMS, l.P95MS, l.JitterMS, l.Samples)
		}
	case client.PhaseDownload:
		if r := t.Download; r != nil {
			return fmt.Sprintf("  ✓ Download  %8.1f Mbit/s   %s in %.1fs over %d streams",
				r.Mbps, humanBytes(r.MeasuredBytes), r.MeasuredSeconds, r.Streams)
		}
	case client.PhaseUpload:
		if r := t.Upload; r != nil {
			return fmt.Sprintf("  ✓ Upload    %8.1f Mbit/s   %s in %.1fs over %d streams (target confirmed %s)",
				r.Mbps, humanBytes(r.MeasuredBytes), r.MeasuredSeconds, r.Streams, humanBytes(r.ServerConfirmedBytes))
		}
	}
	return ""
}

func progressLine(t client.Test) string {
	p := t.Progress
	if p == nil {
		return fmt.Sprintf("  … %s", t.Phase)
	}
	const width = 20
	filled := min(width, int(float64(width)*p.ElapsedS/p.DurationS))
	return fmt.Sprintf("  ▸ %-9s %8.1f Mbit/s   [%s%s] %.1f/%.0fs",
		titleCase(string(p.Phase)), p.Mbps, strings.Repeat("█", filled), strings.Repeat("░", width-filled), p.ElapsedS, p.DurationS)
}

func humanBytes(b int64) string {
	switch {
	case b >= 1e9:
		return fmt.Sprintf("%.2f GB", float64(b)/1e9)
	default:
		return fmt.Sprintf("%.1f MB", float64(b)/1e6)
	}
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func orDash(ok bool, f func() string) string {
	if !ok {
		return "–"
	}
	return f()
}

func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// --- API client -------------------------------------------------------------

type apiClient struct {
	base string
	key  string
	http *http.Client
}

func newAPIClient() (*apiClient, error) {
	dotenv := readDotEnv(".env")
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if v := dotenv[k]; v != "" {
			return v
		}
		return def
	}
	key := get("CLIENT_API_KEY", "")
	if key == "" {
		return nil, errors.New("CLIENT_API_KEY is not set (export it, or run from the repo root where .env lives)")
	}
	return &apiClient{
		base: strings.TrimRight(get("CLIENT_URL", "http://localhost:"+get("CLIENT_PORT", "8080")), "/"),
		key:  key,
		http: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (c *apiClient) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e protocol.Error
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s %s: %s (HTTP %d)", method, path, e.Error, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// readDotEnv parses simple KEY=VALUE lines, ignoring blanks and # comments.
// A missing file yields an empty map.
func readDotEnv(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}
