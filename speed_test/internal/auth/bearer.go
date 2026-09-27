// Package auth implements static bearer-token authorization.
package auth

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"speedtest/internal/protocol"
)

// Bearer wraps next so that it only runs when the request carries
// "Authorization: Bearer <token>". The comparison is constant-time.
func Bearer(token string, next http.Handler) http.Handler {
	want := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="speedtest"`)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(protocol.Error{Error: "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// SetBearer adds the Authorization header for token to req.
func SetBearer(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+token)
}
