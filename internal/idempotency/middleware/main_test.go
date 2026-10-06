package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testServer() (*store, *httptest.Server, *int) {
	executions := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executions++
		b, _ := io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(append([]byte("ok:"), b...))
	})
	s := newStore()
	return s, httptest.NewServer(s.Middleware(handler)), &executions
}

func doPost(t *testing.T, url, key, payload string) (int, string, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(payload))
	if err != nil {
		t.Fatal(err)
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b), resp.Header.Get("Idempotent-Replayed")
}

func TestRetryReplaysWithoutReexecuting(t *testing.T) {
	_, srv, executions := testServer()
	defer srv.Close()

	code, body1, _ := doPost(t, srv.URL, "k1", "pay")
	if code != http.StatusOK {
		t.Fatalf("code = %d, want 200", code)
	}
	code, body2, replayed := doPost(t, srv.URL, "k1", "pay")
	if code != http.StatusOK || body1 != body2 {
		t.Fatalf("replay mismatch: %q vs %q", body1, body2)
	}
	if replayed != "true" {
		t.Fatalf("replayed header = %q, want true", replayed)
	}
	if *executions != 1 {
		t.Fatalf("handler executions = %d, want 1", *executions)
	}
}

func TestMissingKeyRejected(t *testing.T) {
	_, srv, _ := testServer()
	defer srv.Close()
	code, _, _ := doPost(t, srv.URL, "", "pay")
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", code)
	}
}

func TestDifferentKeysExecuteIndependently(t *testing.T) {
	_, srv, executions := testServer()
	defer srv.Close()
	doPost(t, srv.URL, "k1", "a")
	doPost(t, srv.URL, "k2", "b")
	if *executions != 2 {
		t.Fatalf("executions = %d, want 2", *executions)
	}
}

func TestNon2xxIsNotReplayed(t *testing.T) {
	// A stored 500 would mask transient failures, so errors stay retryable:
	// every same-key attempt re-executes the handler.
	executions := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		executions++
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	s := newStore()
	srv := httptest.NewServer(s.Middleware(handler))
	defer srv.Close()

	for i := 0; i < 2; i++ {
		code, _, replayed := doPost(t, srv.URL, "k9", "pay")
		if code != http.StatusInternalServerError {
			t.Fatalf("attempt %d: code = %d, want 500", i, code)
		}
		if replayed != "" {
			t.Fatalf("attempt %d: replayed = %q, want empty (no replay)", i, replayed)
		}
	}
	if executions != 2 {
		t.Fatalf("executions = %d, want 2 (each attempt re-ran)", executions)
	}
}

func TestHandlerHeadersPreservedOnReplay(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Charge-Id", "ch_1")
		_, _ = w.Write([]byte("ok"))
	})
	s := newStore()
	srv := httptest.NewServer(s.Middleware(handler))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewBufferString("pay"))
	req.Header.Set("Idempotency-Key", "kh")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	first := resp.Header.Get("X-Charge-Id")
	resp.Body.Close()

	req2, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewBufferString("pay"))
	req2.Header.Set("Idempotency-Key", "kh")
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if got := resp2.Header.Get("X-Charge-Id"); got != first || got != "ch_1" {
		t.Fatalf("replayed X-Charge-Id = %q, want ch_1 like the original", got)
	}
	if got := resp2.Header.Get("Idempotent-Replayed"); got != "true" {
		t.Fatalf("Idempotent-Replayed = %q, want true", got)
	}
}

func TestFlushSupportedThroughMiddleware(t *testing.T) {
	// Streaming handlers assert http.Flusher; the tee must not break that,
	// otherwise SSE/long-poll handlers silently lose server-push.
	sawFlusher := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f, ok := w.(http.Flusher)
		sawFlusher = ok
		if !ok {
			_, _ = w.Write([]byte("no-flush"))
			return
		}
		for i := 0; i < 3; i++ {
			_, _ = w.Write([]byte("chunk;"))
			f.Flush()
		}
	})
	s := newStore()
	srv := httptest.NewServer(s.Middleware(handler))
	defer srv.Close()

	_, body, _ := doPost(t, srv.URL, "kflush", "pay")
	if !sawFlusher {
		t.Fatal("handler did not see http.Flusher through the middleware")
	}
	if body != "chunk;chunk;chunk;" {
		t.Fatalf("streamed body = %q, want 3 chunks", body)
	}
}

func TestLargeBodyStreamsButIsNotStored(t *testing.T) {
	// Over-cap responses still reach the client untouched, but are never
	// stored — so a retry re-executes instead of replaying.
	executions := 0
	big := bytes.Repeat([]byte("x"), maxCaptureBytes+1024)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		executions++
		_, _ = w.Write(big)
	})
	s := newStore()
	srv := httptest.NewServer(s.Middleware(handler))
	defer srv.Close()

	code, body1, _ := doPost(t, srv.URL, "kbig", "pay")
	if code != http.StatusOK || len(body1) != len(big) {
		t.Fatalf("first: code=%d len=%d, want 200 and %d", code, len(body1), len(big))
	}
	_, _, replayed := doPost(t, srv.URL, "kbig", "pay")
	if replayed != "" {
		t.Fatal("over-cap response must not be replayed")
	}
	if executions != 2 {
		t.Fatalf("executions = %d, want 2 (no stored replay)", executions)
	}
}
