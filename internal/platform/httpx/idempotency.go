package httpx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/http"
)

// idempotencyMaxBody is the response cache cap (rule 2 of the brief). A
// response larger than this is still delivered to the caller in full — only
// its persistence is skipped, so a replay of that key can never come back
// truncated.
const idempotencyMaxBody = 64 * 1024

// idempotencyMaxRequestBody caps how much of the REQUEST body this middleware
// itself buffers to compute the request hash. A body larger than this is not
// a reason to reject or delay a legitimate large upload: the guard is simply
// skipped for that one request (Idempotency-Skipped: request-too-large),
// while the wrapped handler still sees the full, original body byte for
// byte.
const idempotencyMaxRequestBody = 1 << 20 // 1 MiB

// idempotencyHeader is the client-supplied replay key.
const idempotencyHeader = "Idempotency-Key"

// IdempotencyRecord is the persisted state for one (workspace, key) pair, as
// read back to resolve a conflict.
//
// StatusCode is nil while no response has been recorded yet — either the
// original request is still running, or it crashed before finishing; the
// middleware can't tell those two apart from the row alone, and rule 4 (409
// idempotency_in_flight) deliberately covers both.
//
// A non-nil StatusCode of exactly 0 is the sentinel this middleware writes
// when the wrapped handler's response exceeded idempotencyMaxBody (a real
// HTTP status is always >= 100): it distinguishes "finished, but was never
// cacheable" from "still in flight" without a dedicated schema column.
type IdempotencyRecord struct {
	RequestHash  []byte
	StatusCode   *int32
	ResponseBody []byte
	ContentType  string
}

// IdempotencyStore is the persistence seam this middleware depends on
// (dependency inversion, defined here at the platform boundary rather than in
// the app-layer store, since platform/* must never import app/*). The
// app/idempotency package's PgStore satisfies it structurally; cmd/inroad
// wires that concrete store in here.
type IdempotencyStore interface {
	// TryInsert attempts to claim (workspaceID, key) with the given request
	// hash. inserted=true means this call WON the race and owns running the
	// handler + recording its response — either a fresh row, or an EXPIRED
	// conflicting row reclaimed atomically (rule 6: a matched-but-expired row
	// is treated as absent, regardless of whether its old hash matches this
	// request). inserted=false means a row still inside its retention window
	// already exists and the caller must Get it to resolve the conflict.
	TryInsert(ctx context.Context, workspaceID, key string, requestHash []byte) (inserted bool, err error)
	// Get loads the existing (non-expired) row for (workspaceID, key).
	// found=false means no live row exists.
	Get(ctx context.Context, workspaceID, key string) (IdempotencyRecord, bool, error)
	// SetResponse persists the wrapped handler's outcome for (workspaceID,
	// key): its real status/body/content-type, or the uncacheable sentinel
	// (statusCode=0, body=nil, contentType="") when the response exceeded the
	// cache cap. Never called for a >= 500 response — see Delete.
	SetResponse(ctx context.Context, workspaceID, key string, statusCode int, body []byte, contentType string) error
	// Delete releases the claim row after a >= 500 response, so a same-key
	// retry re-executes rather than replaying (or being permanently blocked
	// behind) a transient server error.
	Delete(ctx context.Context, workspaceID, key string) error
}

// WorkspaceIDFunc resolves the authenticated caller's workspace id from the
// request. Idempotency is a generic cross-cutting concern with no knowledge of
// any specific auth scheme — platform/* must never import app/*, so this
// function seam stands in for auth.WorkspaceID; cmd/inroad wires it to
// auth.UserFromContext. Idempotency mounts INSIDE an already-authenticated
// group, so ok=false here is a programming error, not a normal user error.
type WorkspaceIDFunc func(r *http.Request) (workspaceID string, ok bool)

// SkipFunc reports that a request has no idempotency semantics to offer and
// must pass through the guard untouched, exactly as if it carried no
// Idempotency-Key.
//
// It exists for endpoints where REPLAYING a recorded response would be the
// wrong answer — a generation endpoint asked for a fresh result, say — and
// where the guard's 422 idempotency_key_reuse would collide with a status the
// route already uses for a domain meaning, leaving a client that branches on
// status alone unable to tell the two apart.
//
// This middleware mounts at the authenticated-group root, ahead of every
// domain router, so a route cannot opt itself out from its own chain; the
// decision is the composition root's (which is where the route table lives).
// nil means "guard everything", the behavior before this seam existed.
type SkipFunc func(r *http.Request) bool

// Idempotency returns middleware that replays a previously-recorded response
// for a repeated Idempotency-Key on a mutating request (POST/PUT/PATCH/
// DELETE). It is a no-op for GET/HEAD or a request carrying no
// Idempotency-Key header.
//
// Semantics:
//  0. skip(r) reports true (see SkipFunc): pass through untouched, as if no
//     header were present. A nil skip guards everything.
//  1. No header, or a safe method: pass through untouched.
//  2. Fresh key: run the handler with a capped (64 KiB) response recorder and
//     persist status/body/content-type. A response over the cap still reaches
//     the caller in full but is left UNRECORDED (logged at Warning); a later
//     replay of that key then returns 409 idempotency_uncacheable.
//  3. Conflicting row, same request hash, response recorded: replay the
//     stored status/body/content-type and set Idempotency-Replayed: true.
//  4. Conflicting row, same hash, no response yet (concurrent in-flight):
//     409 idempotency_in_flight.
//  5. Conflicting row, different hash: 422 idempotency_key_reuse.
//  6. A matched-but-expired (> 24h) row is treated as absent: TryInsert
//     atomically reclaims it (any hash), so the request runs as if it were
//     fresh. The maintenance sweep is the physical storage reclaimer for a
//     key that is never reused.
//  7. A request body over 1 MiB is not hashed/guarded at all — pass through
//     untouched (Idempotency-Skipped: request-too-large) rather than reject a
//     legitimate large upload or buffer it unbounded.
//  8. A >= 500 response is never cached: the claim row is deleted so a
//     same-key retry re-executes (transient server errors must not be locked
//     in for 24h).
func Idempotency(store IdempotencyStore, workspaceID WorkspaceIDFunc, skip SkipFunc) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(idempotencyHeader)
			if key == "" || !isIdempotentGuardedMethod(r.Method) {
				next.ServeHTTP(w, r)
				return
			}
			if skip != nil && skip(r) {
				next.ServeHTTP(w, r)
				return
			}

			ws, ok := workspaceID(r)
			if !ok {
				slog.ErrorContext(r.Context(), "idempotency: no authenticated principal on request context; middleware must mount inside the authenticated group")
				Error(w, http.StatusInternalServerError, "internal error")
				return
			}

			body, tooLarge, err := readCappedBody(r, idempotencyMaxRequestBody)
			if err != nil {
				Error(w, http.StatusBadRequest, "invalid body")
				return
			}
			if tooLarge {
				slog.WarnContext(r.Context(), "idempotency: request body exceeds guard cap; skipping",
					"workspace_id", ws, "key", key, "cap_bytes", idempotencyMaxRequestBody)
				w.Header().Set("Idempotency-Skipped", "request-too-large")
				next.ServeHTTP(w, r)
				return
			}

			hash := idempotencyRequestHash(r.Method, r.URL.Path, body)

			inserted, err := store.TryInsert(r.Context(), ws, key, hash)
			if err != nil {
				slog.ErrorContext(r.Context(), "idempotency: store insert failed", "err", err)
				Error(w, http.StatusInternalServerError, "idempotency check failed")
				return
			}
			if inserted {
				runAndRecordIdempotent(store, ws, key, next, w, r)
				return
			}

			rec, found, err := store.Get(r.Context(), ws, key)
			if err != nil {
				slog.ErrorContext(r.Context(), "idempotency: store read failed", "err", err)
				Error(w, http.StatusInternalServerError, "idempotency check failed")
				return
			}
			if !found {
				// TryInsert reported a conflict but the row can't be found: an
				// invariant violation. It is NOT the normal expiry path (that is
				// TryInsert's own atomic reclaim, which never reaches this
				// branch) — this only fires if the row aged out or was purged in
				// the few-nanosecond gap between that INSERT and this SELECT.
				// Fail closed rather than silently treat this as a fresh
				// request, which would risk a double-run against a concurrent
				// racer that observes the same gap.
				slog.ErrorContext(r.Context(), "idempotency: conflicting row not found; store inconsistency", "workspace_id", ws, "key", key)
				Error(w, http.StatusInternalServerError, "idempotency check failed")
				return
			}
			if !bytes.Equal(rec.RequestHash, hash) {
				Error(w, http.StatusUnprocessableEntity, "idempotency_key_reuse")
				return
			}
			if rec.StatusCode == nil {
				Error(w, http.StatusConflict, "idempotency_in_flight")
				return
			}
			if *rec.StatusCode == 0 {
				Error(w, http.StatusConflict, "idempotency_uncacheable")
				return
			}
			if rec.ContentType != "" {
				w.Header().Set("Content-Type", rec.ContentType)
			}
			w.Header().Set("Idempotency-Replayed", "true")
			w.WriteHeader(int(*rec.StatusCode))
			_, _ = w.Write(rec.ResponseBody)
		})
	}
}

// runAndRecordIdempotent runs the wrapped handler behind a capped response
// recorder and persists its outcome. Called only after this request has WON
// the TryInsert race for (workspaceID, key), so it is solely responsible for
// eventually giving the row a response (or releasing the claim).
func runAndRecordIdempotent(store IdempotencyStore, workspaceID, key string, next http.Handler, w http.ResponseWriter, r *http.Request) {
	rec := &idempotencyRecorder{ResponseWriter: w, status: http.StatusOK}
	next.ServeHTTP(rec, r)

	ctx := r.Context()

	// A transient server error must not be locked in for 24h — neither
	// replayed as a cached failure nor (worse) turned into a permanent
	// idempotency_uncacheable 409 by the tooLarge branch below. Checked FIRST
	// so it takes priority over tooLarge: a huge 5xx body is released, not
	// marked uncacheable. Mirrors common public-API idempotency practice
	// (e.g. Stripe): only 2xx/3xx/4xx outcomes are cached.
	if rec.status >= http.StatusInternalServerError {
		if err := store.Delete(ctx, workspaceID, key); err != nil {
			// The claim row is left in place until it is next reclaimed (rule
			// 6, on any future request for this key) or physically purged by
			// the maintenance sweep — bounded at 24h either way, never
			// permanent, so a failure here degrades to "this key stays
			// blocked for at most a day" rather than a stuck lock.
			slog.ErrorContext(ctx, "idempotency: failed to release claim after server error", "err", err)
		}
		return
	}

	if rec.tooLarge {
		slog.WarnContext(ctx, "idempotency: response exceeds cache cap; not recorded",
			"workspace_id", workspaceID, "key", key, "bytes", rec.size, "cap_bytes", idempotencyMaxBody)
		if err := store.SetResponse(ctx, workspaceID, key, 0, nil, ""); err != nil {
			slog.ErrorContext(ctx, "idempotency: failed to mark response uncacheable", "err", err)
		}
		return
	}
	if err := store.SetResponse(ctx, workspaceID, key, rec.status, rec.buf.Bytes(), rec.Header().Get("Content-Type")); err != nil {
		// Same bounded-degradation note as above: a failed write leaves the
		// row claimed-but-unset (reads as in-flight) until it is reclaimed or
		// purged, at most 24h later — never indefinitely.
		slog.ErrorContext(ctx, "idempotency: failed to persist response", "err", err)
	}
}

// idempotencyRecorder wraps the real ResponseWriter to capture a copy of the
// status/body up to idempotencyMaxBody, while always forwarding every byte to
// the real writer — so the actual caller always gets the full, real response
// regardless of whether it ends up cacheable.
type idempotencyRecorder struct {
	http.ResponseWriter
	status      int
	buf         bytes.Buffer
	size        int
	tooLarge    bool
	wroteHeader bool
}

func (rec *idempotencyRecorder) WriteHeader(status int) {
	if rec.wroteHeader {
		return
	}
	rec.wroteHeader = true
	rec.status = status
	rec.ResponseWriter.WriteHeader(status)
}

func (rec *idempotencyRecorder) Write(p []byte) (int, error) {
	if !rec.wroteHeader {
		rec.WriteHeader(http.StatusOK)
	}
	rec.size += len(p)
	if !rec.tooLarge {
		if rec.buf.Len()+len(p) > idempotencyMaxBody {
			rec.tooLarge = true
			rec.buf.Reset()
		} else {
			rec.buf.Write(p)
		}
	}
	return rec.ResponseWriter.Write(p)
}

// readCappedBody reads up to capBytes+1 bytes of r.Body to decide whether the
// body fits the cap, and ALWAYS restores r.Body so the wrapped handler sees
// the exact original stream byte for byte:
//   - within the cap: body holds the (now fully buffered) content, and r.Body
//     is reset to replay it.
//   - over the cap: tooLarge=true, body is nil, and r.Body is reset to the
//     already-read prefix followed by whatever remains unread on the
//     original body — i.e. the untouched, full original stream.
func readCappedBody(r *http.Request, capBytes int64) (body []byte, tooLarge bool, err error) {
	prefix, err := io.ReadAll(io.LimitReader(r.Body, capBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(prefix)) > capBytes {
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(prefix), r.Body))
		return nil, true, nil
	}
	r.Body = io.NopCloser(bytes.NewReader(prefix))
	return prefix, false, nil
}

// idempotencyRequestHash pins the exact request an Idempotency-Key was first
// used for: SHA-256(method + "\n" + path + "\n" + body). A replay against a
// DIFFERENT method, path, or body is therefore rejected as key reuse rather
// than silently matching.
func idempotencyRequestHash(method, path string, body []byte) []byte {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte("\n"))
	h.Write([]byte(path))
	h.Write([]byte("\n"))
	h.Write(body)
	return h.Sum(nil)
}

func isIdempotentGuardedMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
