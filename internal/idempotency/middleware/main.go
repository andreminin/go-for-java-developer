// This example demonstrates HTTP middleware for idempotency.
//
// Flow:
//  1. Extract the key from the `Idempotency-Key` header (missing key =>
//     either reject with 400 for unsafe methods or execute non-idempotently
//     for safe ones — a policy decision, shown here as 400 on POST).
//  2. Before the handler: check the dedup table (INSERT ... ON CONFLICT).
//  3. After the handler: store status code + headers + body for replay —
//     but ONLY for 2xx responses under the capture cap (see below).
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

// maxCaptureBytes caps how much of a response body the middleware buffers
// for replay. Above this, the response still streams to the client but is
// NOT stored, so a retry re-executes instead of replaying.
//
// Why a cap at all: buffering an unbounded body (file download, export)
// holds it in RAM and defeats the purpose of streaming. This is the same
// tradeoff Stripe documents for its Idempotency-Key API: responses above
// the replayable size are not replayed, and clients must handle that.
const maxCaptureBytes = 1 << 20 // 1 MiB

// store is a minimal dedup table for HTTP responses.
type store struct {
	mu   sync.Mutex
	rows map[string]storedResp
}

type storedResp struct {
	code   int
	header http.Header
	body   []byte
}

func newStore() *store { return &store{rows: make(map[string]storedResp)} }

// captureWriter implements http.ResponseWriter and wraps the real one.
// It tees every Write into an in-memory buffer (for idempotency replay)
// while passing the bytes through to the client immediately.
//
// In Java this is the equivalent of wrapping HttpServletResponse with a
// caching wrapper that calls through to the underlying stream AND records
// the bytes — except the servlet API makes you subclass, while Go composes
// via interface embedding.
//
// Why not httptest.NewRecorder (a test utility): it buffers the ENTIRE
// response before writing anything to the client, which breaks streaming
// responses (SSE, large downloads, http.Flusher) and holds big bodies twice
// in memory. The tee preserves first-byte latency and streaming semantics.
type captureWriter struct {
	http.ResponseWriter
	status      int
	header      http.Header // Snapshot point: cloned at WriteHeader time.
	body        bytes.Buffer
	wroteHeader bool
	truncated   bool // Set once the body exceeds maxCaptureBytes.
}

func newCaptureWriter(w http.ResponseWriter) *captureWriter {
	return &captureWriter{ResponseWriter: w, status: http.StatusOK}
}

func (c *captureWriter) WriteHeader(status int) {
	if c.wroteHeader {
		return // Mirror net/http: superfluous WriteHeader calls are ignored.
	}
	c.wroteHeader = true
	c.status = status
	// Why clone here: the handler may mutate its header map after returning,
	// so snapshot at commit time (first WriteHeader/Write), like the server
	// snapshotting headers onto the wire.
	c.header = c.ResponseWriter.Header().Clone()
	c.ResponseWriter.WriteHeader(status)
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK) // Implicit 200, exactly like net/http.
	}
	if !c.truncated {
		if c.body.Len()+len(b) > maxCaptureBytes {
			// Over the cap: the buffer is Reset AND truncated is set, so all
			// later Writes skip buffering entirely — memory stays flat no
			// matter how large the response gets. Merely skipping the store
			// while letting the buffer grow would keep the very memory
			// pressure the cap exists to prevent. Streaming continues below.
			c.truncated = true
			c.body.Reset()
		} else {
			c.body.Write(b)
		}
	}
	return c.ResponseWriter.Write(b)
}

// Flush preserves streaming for handlers that assert http.Flusher (SSE,
// long-poll). It forwards only if the underlying writer supports it.
//
// Fidelity tradeoff, documented not fixed: because Flush is always defined,
// the wrapper always satisfies http.Flusher — even if the underlying writer
// does not. A handler taking the `ok == true` branch therefore cannot tell
// the two cases apart. In practice net/http writers always flush, so the lie
// never fires; strict fidelity would need a factory returning different
// concrete types per underlying interface set, which is overkill here.
//
// Deliberately NOT implemented: http.Hijacker and http.Pusher. A hijacked
// connection (raw TCP take-over for websockets) bypasses Write/WriteHeader
// entirely, so there is nothing to capture — such requests pass through
// unrecorded and a retry re-executes. WARNING: that is only safe for
// handlers that check the assertion with `, ok` and fall back gracefully.
// Hand-rolled upgraders doing the unchecked form `w.(http.Hijacker)` will
// PANIC through this wrapper — skip the idempotency middleware entirely for
// WebSocket endpoints. Same category: Pusher (HTTP/2 server push, rarely
// used) is hidden behind the wrapper even when the server supports it.
func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// ReadFrom preserves io.Copy fast paths. Without it, io.Copy(dst, src)
// cannot use sendfile/splice through the wrapper and every large download
// falls back to a userspace buffer copy — a small but real production
// regression. When still capturing, chunks flow through Write (cap enforced
// there); once truncated, the copy delegates straight to the underlying
// writer so the fast path (src WriterTo / dst ReaderFrom) applies again.
func (c *captureWriter) ReadFrom(r io.Reader) (int64, error) {
	if !c.wroteHeader {
		c.WriteHeader(http.StatusOK) // Implicit 200, exactly like net/http.
	}
	if c.truncated {
		return io.Copy(c.ResponseWriter, r)
	}
	var total int64
	buf := make([]byte, 32*1024)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			if _, werr := c.Write(buf[:n]); werr != nil {
				return total, werr
			}
			total += int64(n)
		}
		if rerr != nil {
			if rerr == io.EOF {
				return total, nil
			}
			return total, rerr
		}
	}
}

// Middleware enforces idempotency for POST requests carrying Idempotency-Key.
// Key extraction -> pre-handler check -> tee-capture -> conditional store.
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
			// Original headers are restored so the replay is byte-faithful.
			for k, vv := range prev.header {
				for _, v := range vv {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(prev.code)
			_, _ = w.Write(prev.body)
			return
		}
		s.mu.Unlock()
		// NOTE: two concurrent FIRST deliveries with the same key can both
		// miss the map and both execute (check-then-act race). The in-memory
		// map is demo-grade; the PostgreSQL version does not have this race
		// because INSERT ... ON CONFLICT DO NOTHING is a single atomic
		// statement (see internal/idempotency/dedup_table).

		cw := newCaptureWriter(w)
		next.ServeHTTP(cw, r)

		// Store for replay ONLY on 2xx within the cap. The boundary choices:
		//   5xx: NEVER store. Replaying a stored 500 would mask a transient
		//     failure that a retry might survive — errors must stay
		//     retryable, so the next attempt re-executes the handler. This
		//     is the single most important correctness point here.
		//   429: never store, same reasoning — the retry must re-execute
		//     once the rate-limit window passes.
		//   3xx: not stored either. Redirects are safe to replay in theory,
		//     but clients that followed the redirect don't retry the
		//     original, so storing buys nothing; the chain simply re-runs.
		//   409: not stored. A business-rule 409 re-evaluated on retry is
		//     correct; an "already processing" 409 would also be fine to
		//     replay as backoff signal — but re-running the check is
		//     equally correct and simpler to reason about.
		//   Over-cap 2xx: not stored (see maxCaptureBytes). The retry then
		//     RE-EXECUTES the handler, side effects included — the dangerous
		//     option, chosen here for simplicity. Production alternatives:
		//     return 409 with "response too large to replay" (Stripe's
		//     approach), or store a {"replayable": false} marker and let
		//     the client decide. Whatever you pick, comment it.
		if cw.status >= 200 && cw.status < 300 && !cw.truncated {
			s.mu.Lock()
			s.rows[key] = storedResp{code: cw.status, header: cw.header, body: cw.body.Bytes()}
			s.mu.Unlock()
		}
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

	post := func(url, key, payload string) (int, string, string) {
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBufferString(payload))
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, "", err.Error()
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b), resp.Header.Get("Idempotent-Replayed")
	}

	fmt.Println("== 1. first POST executes handler ==")
	code, body, replayed := post(srv.URL, "key-1", `{"amount":100}`)
	fmt.Printf("code=%d body=%s replayed=%q executions=%d\n", code, body, replayed, executions)

	fmt.Println("== 2. retry with same key replays, handler skipped ==")
	code, body, replayed = post(srv.URL, "key-1", `{"amount":100}`)
	fmt.Printf("code=%d body=%s replayed=%q executions=%d (still 1)\n", code, body, replayed, executions)

	fmt.Println("== 3. POST without key is rejected ==")
	code, _, _ = post(srv.URL, "", `{}`)
	fmt.Printf("code=%d (expect 400)\n", code)

	fmt.Println("== 4. 5xx is NOT replayed: retry re-executes ==")
	failures := 0
	failSrv := httptest.NewServer(newStore().Middleware(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			failures++
			http.Error(w, "boom", http.StatusInternalServerError)
		}),
	))
	defer failSrv.Close()
	code, _, _ = post(failSrv.URL, "key-9", `{}`)
	code, _, replayed = post(failSrv.URL, "key-9", `{}`)
	fmt.Printf("code=%d replayed=%q failures=%d (handler ran twice: errors stay retryable)\n",
		code, replayed, failures)
}
