// Package target implements the Target service: the endpoint a speed test is
// measured against. It is deliberately dumb — it serves bytes, swallows bytes
// and answers pings; all measurement logic lives in the Client service.
package target

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"speedtest/internal/auth"
	"speedtest/internal/protocol"
)

const chunkSize = 1 << 20 // 1 MiB

// Server holds the pre-generated payload shared by all downloads.
type Server struct {
	payload []byte
}

// New returns a Server with a fresh random payload. Random data keeps any
// transparent compression on the path from inflating the numbers.
func New() *Server {
	p := make([]byte, chunkSize)
	if _, err := rand.Read(p); err != nil {
		panic(err)
	}
	return &Server{payload: p}
}

// Handler returns the Target HTTP API guarded by the given bearer token.
func (s *Server) Handler(token string) http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET "+protocol.PathPing, s.ping)
	api.HandleFunc("GET "+protocol.PathDownload, s.download)
	api.HandleFunc("POST "+protocol.PathUpload, s.upload)

	mux := http.NewServeMux()
	mux.HandleFunc("GET "+protocol.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", auth.Bearer(token, api))
	return mux
}

func (s *Server) ping(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
	if err != nil || n <= 0 || n > protocol.MaxDownloadBytes {
		writeError(w, http.StatusBadRequest, "bytes must be an integer in [1, "+strconv.FormatInt(protocol.MaxDownloadBytes, 10)+"]")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(n, 10))
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	for n > 0 {
		chunk := s.payload
		if int64(len(chunk)) > n {
			chunk = chunk[:n]
		}
		written, err := w.Write(chunk)
		n -= int64(written)
		if err != nil {
			// Client went away — expected when it hits its test deadline.
			return
		}
	}
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body := http.MaxBytesReader(w, r.Body, protocol.MaxUploadBytes)
	buf := make([]byte, 256<<10)
	var received int64
	for {
		n, err := body.Read(buf)
		received += int64(n)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, "upload exceeds limit")
				return
			}
			// Client aborted mid-body; nobody is listening for a reply.
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(protocol.UploadResult{
		BytesReceived: received,
		DurationMS:    float64(time.Since(start).Microseconds()) / 1000,
	})
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(protocol.Error{Error: msg}); err != nil {
		log.Printf("write error response: %v", err)
	}
}
