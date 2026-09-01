package aiclient

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/cis/cis-backend/internal/config"
)

// captureLog redirects the standard logger into a buffer for the duration of a
// test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	return buf
}

func clientFor(url string) *Client {
	return New(config.AIConfig{
		BaseURL:     url,
		Timeout:     time.Second,
		LongTimeout: time.Second,
	})
}

func TestAICallLogsRoundTrip(t *testing.T) {
	buf := captureLog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer srv.Close()

	if _, err := clientFor(srv.URL).SubmitPolicy(context.Background(), MatchmakingRequest{PolicyID: "p1"}); err != nil {
		t.Fatalf("SubmitPolicy: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "[ai] > POST "+pathMatchmaking) {
		t.Errorf("missing start line:\n%s", got)
	}
	if !strings.Contains(got, "[ai] < POST "+pathMatchmaking+" 202 in ") {
		t.Errorf("missing/!ok end line:\n%s", got)
	}
}

func TestAICallLogsHTTPError(t *testing.T) {
	buf := captureLog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := clientFor(srv.URL).Rescore(context.Background()); err == nil {
		t.Fatal("expected an error from a 503")
	}

	if got := buf.String(); !strings.Contains(got, "[ai] < POST "+pathRescore+" 503 after ") {
		t.Errorf("expected a 503 end line with the body, got:\n%s", got)
	}
}

func TestAICallLogsTransportFailure(t *testing.T) {
	buf := captureLog(t)

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing is listening now

	if _, err := clientFor(srv.URL).ClusterNow(context.Background()); err == nil {
		t.Fatal("expected a connection error")
	}

	if got := buf.String(); !strings.Contains(got, "[ai] < POST "+pathClusterNow+" failed after ") {
		t.Errorf("expected a transport-failure end line, got:\n%s", got)
	}
}

func TestHealthProbeLogsOnlyOnFailure(t *testing.T) {
	buf := captureLog(t)

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()

	if err := clientFor(ok.URL).Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "" {
		t.Errorf("a healthy probe should log nothing, got: %q", got)
	}

	down := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	down.Close()
	_ = clientFor(down.URL).Health(context.Background())
	if !strings.Contains(buf.String(), "[ai] < GET "+pathHealth+" failed after ") {
		t.Errorf("a failing probe should be logged, got:\n%s", buf.String())
	}
}
