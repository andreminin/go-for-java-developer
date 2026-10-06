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
