package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"speedtest/internal/target"
)

const (
	targetToken = "target-secret"
	apiKey      = "client-secret"
)

// newTestAPI starts a Target and a Client API; the Client presents
// presentedToken to the Target.
func newTestAPI(t *testing.T, presentedToken string) *httptest.Server {
	t.Helper()
	tgt := httptest.NewServer(target.New().Handler(targetToken))
	t.Cleanup(tgt.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	api := httptest.NewServer(NewAPI(ctx, NewRunner(tgt.URL, presentedToken), NewStore(20), apiKey))
	t.Cleanup(api.Close)
	return api
}

func call(t *testing.T, srv *httptest.Server, method, path, key, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	return resp.StatusCode
}

func waitDone(t *testing.T, api *httptest.Server, id string) Test {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var got Test
		call(t, api, http.MethodGet, "/api/tests/"+id, apiKey, "", &got)
		if got.Status != StatusRunning {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("test did not finish in time")
	return Test{}
}

func TestEndToEnd(t *testing.T) {
	api := newTestAPI(t, targetToken)

	if code := call(t, api, http.MethodPost, "/api/tests", "", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("no key: status %d, want 401", code)
	}
	if code := call(t, api, http.MethodPost, "/api/tests", apiKey, `{"streams":99}`, nil); code != http.StatusBadRequest {
		t.Fatalf("bad config: status %d, want 400", code)
	}

	var started Test
	cfg := `{"streams":2,"duration_s":1,"warmup_s":0.25,"pings":5}`
	if code := call(t, api, http.MethodPost, "/api/tests", apiKey, cfg, &started); code != http.StatusAccepted {
		t.Fatalf("start: status %d, want 202", code)
	}
	if code := call(t, api, http.MethodPost, "/api/tests", apiKey, cfg, nil); code != http.StatusConflict {
		t.Fatalf("concurrent start: status %d, want 409", code)
	}

	got := waitDone(t, api, started.ID)
	if got.Status != StatusDone {
		t.Fatalf("status %s, error %q", got.Status, got.Error)
	}
	if got.Latency == nil || got.Latency.Samples != 5 || got.Latency.MedianMS <= 0 {
		t.Fatalf("latency: %+v", got.Latency)
	}
	for name, r := range map[string]*ThroughputResult{"download": got.Download, "upload": got.Upload} {
		if r == nil || r.Mbps <= 0 || r.MeasuredBytes <= 0 || r.Streams != 2 {
			t.Fatalf("%s: %+v", name, r)
		}
	}
	if got.Upload.ServerConfirmedBytes != got.Upload.TotalBytes {
		t.Fatalf("upload: target confirmed %d bytes, client sent %d", got.Upload.ServerConfirmedBytes, got.Upload.TotalBytes)
	}

	var list []Test
	call(t, api, http.MethodGet, "/api/tests", apiKey, "", &list)
	if len(list) != 1 || list[0].ID != started.ID {
		t.Fatalf("list: %+v", list)
	}
}

func TestWrongTargetTokenFailsTest(t *testing.T) {
	api := newTestAPI(t, "wrong")

	var started Test
	if code := call(t, api, http.MethodPost, "/api/tests", apiKey, `{"duration_s":1,"warmup_s":0.5}`, &started); code != http.StatusAccepted {
		t.Fatalf("start: status %d, want 202", code)
	}
	got := waitDone(t, api, started.ID)
	if got.Status != StatusFailed || !strings.Contains(got.Error, "401") {
		t.Fatalf("status %s, error %q; want failed with 401", got.Status, got.Error)
	}
}
