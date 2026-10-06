// This example demonstrates HTTP middleware for idempotency.
//
// Flow:
//  1. Extract the key from the `Idempotency-Key` header (missing key =>
//     either reject with 400 for unsafe methods or execute non-idempotently
//     for safe ones — a policy decision, shown here as 400 on POST).
//  2. Before the handler: check the dedup table (INSERT ... ON CONFLICT).
//  3. After the handler: store status code + body for replay.
//  4. On redelivery: short-circuit with the stored response, handler never runs.
//
// In Java/Spring this is a OncePerRequestFilter / HandlerInterceptor with the
// same table; in Go it is a net/http middleware wrapping a handler. Real
// libraries: idempotent, once, idem.
//
// Run: go run ./internal/idempotency/middleware
// Then: curl -i -X POST localhost:8080/pay -H 'Idempotency-Key: abc' (twice).
package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
)

// store is a minimal dedup table for HTTP responses.
type store struct {
	mu   sync.Mutex
	rows map[string]storedResp
}

type storedResp struct {
	code int
	body []byte
}

func newStore() *store { return &store{rows: make(map[string]storedResp)} }

// Middleware enforces idempotency for POST requests carrying Idempotency-Key.
// Key extraction -> pre-handler check -> save response -> replay on conflict.
func (s *store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r) // Only mutating endpoints need keys.
			return
		}
		key := r.Header.Get("Idempotency-Key")
		if key == "" {
			// Why 400: silently executing a retryable POST without a key
			// risks duplicates; failing fast teaches clients to send keys.
			http.Error(w, "missing Idempotency-Key header", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		if prev, ok := s.rows[key]; ok {
			s.mu.Unlock()
			// Replay: handler does NOT run again (exactly-once effect).
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(prev.code)
			_, _ = w.Write(prev.body)
			return
		}
		s.mu.Unlock()

		// Capture the handler's response so we can store it.
		// In Java: wrap HttpServletResponse with a caching wrapper.
		rec := httptest.NewRecorder()
		next.ServeHTTP(rec, r)

		s.mu.Lock()
		s.rows[key] = storedResp{code: rec.Code, body: rec.Body.Bytes()}
		s.mu.Unlock()

		for k, vv := range rec.Header() {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(rec.Code)
		_, _ = w.Write(rec.Body.Bytes())
	})
}

func main() {
	executions := 0
	// The business handler: charges exactly once per key by construction.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		executions++
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, `{"charge":"ch_%d","echo":%q}`, executions, string(body))
	})

	s := newStore()
	srv := httptest.NewServer(s.Middleware(handler))
	defer srv.Close()

	post := func(key, payload string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewBufferString(payload))
		req.Header.Set("Idempotency-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Idempotent-Replayed")
	}

	fmt.Println("== 1. first POST executes handler ==")
	code, body, replayed := post("key-1", `{"amount":100}`)
	fmt.Printf("code=%d body=%s replayed=%q executions=%d\n", code, body, replayed, executions)

	fmt.Println("== 2. retry with same key replays, handler skipped ==")
	code, body, replayed = post("key-1", `{"amount":100}`)
	fmt.Printf("code=%d body=%s replayed=%q executions=%d (still 1)\n", code, body, replayed, executions)

	fmt.Println("== 3. POST without key is rejected ==")
	req, _ := http.NewRequest(http.MethodPost, srv.URL, bytes.NewBufferString(`{}`))
	resp, _ := http.DefaultClient.Do(req)
	fmt.Printf("code=%d (expect 400)\n", resp.StatusCode)
	resp.Body.Close()
}
