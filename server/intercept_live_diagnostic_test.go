package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
)

func TestLiveReviewDiagnosticsPreserveCauseWithoutSecrets(t *testing.T) {
	const secret = "private-key-do-not-log"
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"deadline", fmt.Errorf("%s: %w", secret, context.DeadlineExceeded), "category=deadline_exceeded http_status=0"},
		{"cancelled", context.Canceled, "category=request_cancelled http_status=0"},
		{"auth", fmt.Errorf("openai: status 401: %s", secret), "category=authentication_or_permission http_status=401"},
		{"permission", fmt.Errorf("openai: status 403: %s", secret), "category=authentication_or_permission http_status=403"},
		{"rate_limit", fmt.Errorf("wrapped: %w", fmt.Errorf("openai: status 429: %s", secret)), "category=rate_limited http_status=429"},
		{"server", fmt.Errorf("openai: status 503: %s", secret), "category=provider_server_error http_status=503"},
		{"bad_request", fmt.Errorf("openai: status 400: %s", secret), "category=provider_http_error http_status=400"},
		{"network", &url.Error{Op: "Post", URL: "https://user:" + secret + "@provider.invalid/v1?api_key=" + secret, Err: errors.New("connection failed " + secret)}, "category=network_error http_status=0"},
		{"transport_timeout", &url.Error{Op: "Post", URL: "https://provider.invalid/" + secret, Err: os.ErrDeadlineExceeded}, "category=transport_timeout http_status=0"},
		{"stream", fmt.Errorf("%s: %w", secret, io.ErrUnexpectedEOF), "category=stream_truncated http_status=0"},
		{"unknown", errors.New(secret + " response body"), "category=provider_or_stream_error http_status=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := liveReviewErrorDiagnostic(tc.err)
			if strings.Contains(got, secret) || strings.Contains(got, "provider.invalid") {
				t.Fatal("diagnostic exposed private data")
			}
			if got != tc.want {
				t.Fatalf("diagnostic=%q, want %q", got, tc.want)
			}
		})
	}
}
