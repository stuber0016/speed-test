package target

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"speedtest/internal/protocol"
)

const token = "secret"

func do(t *testing.T, srv *httptest.Server, method, path, tok string, body io.Reader) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, body)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAuth(t *testing.T) {
	srv := httptest.NewServer(New().Handler(token))
	defer srv.Close()

	for _, tc := range []struct {
		name, path, tok string
		want            int
	}{
		{"no token", protocol.PathPing, "", http.StatusUnauthorized},
		{"wrong token", protocol.PathPing, "nope", http.StatusUnauthorized},
		{"right token", protocol.PathPing, token, http.StatusNoContent},
		{"health is public", protocol.PathHealth, "", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := do(t, srv, http.MethodGet, tc.path, tc.tok, nil).StatusCode; got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestDownloadExactSize(t *testing.T) {
	srv := httptest.NewServer(New().Handler(token))
	defer srv.Close()

	for _, n := range []int64{1, chunkSize - 1, chunkSize, 3*chunkSize + 17} {
		resp := do(t, srv, http.MethodGet, protocol.PathDownload+"?bytes="+itoa(n), token, nil)
		got, err := io.Copy(io.Discard, resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got != n {
			t.Fatalf("bytes=%d: got %d bytes", n, got)
		}
	}
}

func TestDownloadRejectsBadSize(t *testing.T) {
	srv := httptest.NewServer(New().Handler(token))
	defer srv.Close()

	for _, q := range []string{"", "?bytes=0", "?bytes=-5", "?bytes=abc", "?bytes=" + itoa(protocol.MaxDownloadBytes+1)} {
		if got := do(t, srv, http.MethodGet, protocol.PathDownload+q, token, nil).StatusCode; got != http.StatusBadRequest {
			t.Fatalf("%q: status = %d, want 400", q, got)
		}
	}
}

func TestUploadReportsBytes(t *testing.T) {
	srv := httptest.NewServer(New().Handler(token))
	defer srv.Close()

	const n = 5<<20 + 3
	resp := do(t, srv, http.MethodPost, protocol.PathUpload, token, bytes.NewReader(make([]byte, n)))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var res protocol.UploadResult
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	if res.BytesReceived != n {
		t.Fatalf("bytes_received = %d, want %d", res.BytesReceived, n)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
