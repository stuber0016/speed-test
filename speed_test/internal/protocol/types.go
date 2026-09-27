// Package protocol defines the HTTP contract between the Client service and
// the Target service. Both sides compile against these constants and types so
// the contract lives in exactly one place.
//
// Every endpoint except PathHealth requires "Authorization: Bearer <token>".
package protocol

const (
	// PathPing answers 204 No Content immediately. Used for latency sampling.
	PathPing = "/ping"

	// PathDownload streams ?bytes=N bytes of incompressible data.
	PathDownload = "/download"

	// PathUpload reads and discards the request body, then answers with an
	// UploadResult describing what the server actually received.
	PathUpload = "/upload"

	// PathHealth is an unauthenticated liveness probe.
	PathHealth = "/healthz"
)

const (
	// MaxDownloadBytes caps a single /download response. Clients that need
	// more simply issue another request.
	MaxDownloadBytes int64 = 2 << 30 // 2 GiB

	// MaxUploadBytes caps a single /upload request body.
	MaxUploadBytes int64 = 2 << 30 // 2 GiB
)

// UploadResult is the JSON body returned by PathUpload.
type UploadResult struct {
	BytesReceived int64   `json:"bytes_received"`
	DurationMS    float64 `json:"duration_ms"`
}

// Error is the JSON body returned on any non-2xx response by either service.
type Error struct {
	Error string `json:"error"`
}
