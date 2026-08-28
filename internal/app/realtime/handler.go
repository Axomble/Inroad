// Package realtime is the HTTP handshake in front of the workspace fan-out hub:
// it mints connect tickets, upgrades a socket, and pumps the hub's frames to a
// browser.
//
// The split from internal/platform/realtime is deliberate. That package is the
// transport-agnostic hub (Redis, sequences, replay) and imports no socket
// library; this one owns everything HTTP and everything WebSocket. The security
// controls all live HERE, because they are all properties of a request:
//
//   - the Origin allowlist (origin.go), since a WS handshake is not subject to
//     the same-origin policy;
//   - the single-use nonce burn (nonce.go), so a ticket in a proxy log is not a
//     reusable credential;
//   - the session re-check, so a logout between minting a ticket and spending it
//     is refused;
//   - the connection caps (limits.go).
//
// See docs/superpowers/specs/realtime-websocket.md §3, §4 and §7.
package realtime

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
	platformrealtime "github.com/inroad/inroad/internal/platform/realtime"
	"github.com/inroad/inroad/internal/platform/wsticket"
)

// Keepalive budget (spec §4). Idle-timeout intermediaries are the norm, and a
// half-open socket that still looks connected is worse than one that reconnects:
// the client shows a live indicator over a feed that has silently stopped.
//
// pongWait is just over two ping intervals, which is what "drop after two missed
// pongs" means in practice — one missed pong is a hiccup, two is a dead peer.
const (
	pingInterval = 30 * time.Second
	pongWait     = 2*pingInterval + 5*time.Second
	writeWait    = 10 * time.Second
)

// Attacher is the read half of the hub as this handler needs it. Declared at the
// seam so the handshake can be tested against a stub that yields frames on
// demand, with no Redis (dependency inversion — see platform/realtime.Publisher
// for the write-side counterpart).
type Attacher interface {
	Attach(ctx context.Context, workspaceID uuid.UUID, afterSeq int64) (<-chan platformrealtime.Frame, error)
}

// SessionChecker reports whether a session is still live. The handshake calls it
// with the session id carried in the ticket, so a logout in the seconds between
// mint and connect is refused rather than honoured for the rest of the TTL.
//
// An interface rather than a concrete verifier: this handler must not learn how
// sessions are stored, and the "session is gone" branch is one a test has to be
// able to drive.
type SessionChecker interface {
	// SessionLive returns false for a revoked, expired or unknown session. An
	// error means it could not be determined, which the caller treats as a
	// refusal.
	SessionLive(ctx context.Context, sessionID string) (bool, error)
}

// Options configures the handler. Caps of zero take the package defaults.
type Options struct {
	Secret          []byte
	AllowedOrigins  []string
	MaxPerUser      int
	MaxPerWorkspace int
	// TicketTTL defaults to wsticket.DefaultTTL. Configurable only so a test need
	// not sleep 30 seconds to exercise expiry.
	TicketTTL time.Duration
}

// Handler serves POST /realtime/ticket and GET /realtime/ws.
type Handler struct {
	hub      Attacher
	burner   TicketBurner
	sessions SessionChecker
	log      *slog.Logger
	opts     Options
	counter  *connCounter
	upgrader websocket.Upgrader
	// now is injectable so expiry and keepalive behaviour are testable without a
	// real clock.
	now func() time.Time
}

func NewHandler(hub Attacher, burner TicketBurner, sessions SessionChecker, log *slog.Logger, opts Options) *Handler {
	if opts.TicketTTL <= 0 {
		opts.TicketTTL = wsticket.DefaultTTL
	}
	return &Handler{
		hub:      hub,
		burner:   burner,
		sessions: sessions,
		log:      log,
		opts:     opts,
		counter:  newConnCounter(opts.MaxPerUser, opts.MaxPerWorkspace),
		upgrader: websocket.Upgrader{
			// The Origin allowlist is the load-bearing control here; gorilla's own
			// default is same-origin-ish and would silently pass a missing-Origin
			// non-browser client either way. See origin.go for why a missing Origin
			// is allowed and why matching is exact.
			CheckOrigin: checkOrigin(opts.AllowedOrigins),
			// No compression: envelopes are small and CRIME-style attacks on
			// compressed authenticated streams are not worth the bytes saved.
		},
		now: time.Now,
	}
}

// Routes mounts the two endpoints. Authentication is applied by the surrounding
// protected group (cmd/inroad mounts this in sessionOnly), so both handlers can
// assume a verified principal — and the ws handler additionally re-derives
// everything it trusts from the ticket rather than from that principal, because
// a browser cannot send a bearer token on an Upgrade.
func (h *Handler) Routes(ticketThrottle func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		if ticketThrottle != nil {
			r.Use(ticketThrottle)
		}
		// Minting is throttled because it is an authenticated endpoint that mints
		// CREDENTIALS (spec §7.4) — treated like loginThrottle, not like a read.
		r.Post("/ticket", h.mintTicket)
	})
	r.Get("/ws", h.serveWS)
	return r
}

// ticketResponse is the mint endpoint's body. expires_in is seconds, matching
// the OAuth convention the rest of this API already uses for token lifetimes.
type ticketResponse struct {
	Ticket    string `json:"ticket"`
	ExpiresIn int    `json:"expires_in"`
}

func (h *Handler) mintTicket(w http.ResponseWriter, r *http.Request) {
	p, ok := auth.UserFromContext(r.Context())
	if !ok {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	workspaceID, ok := auth.WorkspaceID(w, r)
	if !ok {
		return
	}
	// A session principal always carries a session id; an api-key or OAuth
	// principal does not. This route is mounted session-only so that should be
	// unreachable, but the socket's logout re-check is meaningless without one, so
	// refuse rather than mint a ticket that cannot be revoked.
	if p.SessionID == "" {
		httpx.Error(w, http.StatusUnauthorized, "session required")
		return
	}

	nonce, err := newNonce()
	if err != nil {
		// A failure here is a broken CSPRNG, not a client problem.
		h.log.ErrorContext(r.Context(), "realtime: generate ticket nonce", "error", err)
		httpx.Error(w, http.StatusInternalServerError, "internal error")
		return
	}

	ticket := wsticket.Make(h.opts.Secret, wsticket.Ticket{
		WorkspaceID: workspaceID.String(),
		UserID:      p.UserID,
		SessionID:   p.SessionID,
		ExpiresAt:   h.now().Add(h.opts.TicketTTL),
		Nonce:       nonce,
	})

	httpx.JSON(w, http.StatusOK, ticketResponse{
		Ticket:    ticket,
		ExpiresIn: int(h.opts.TicketTTL.Seconds()),
	})
}

func newNonce() (string, error) {
	b := make([]byte, wsticket.NonceBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// serveWS validates a ticket and, if everything holds, upgrades the connection
// and pumps hub frames until the client goes away or the process shuts down.
//
// Every refusal below returns the SAME opaque status to the client and logs the
// real reason server-side. A client that could tell "expired" from "bad
// signature" from "already spent" has a probing oracle over the ticket format.
func (h *Handler) serveWS(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	raw := r.URL.Query().Get("ticket")
	if raw == "" {
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	ticket, err := wsticket.Parse(h.opts.Secret, raw, h.now())
	if err != nil {
		h.log.WarnContext(ctx, "realtime: ticket rejected", "error", err)
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	workspaceID, err := uuid.Parse(ticket.WorkspaceID)
	if err != nil {
		h.log.WarnContext(ctx, "realtime: ticket workspace unparseable", "error", err)
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	userID, err := uuid.Parse(ticket.UserID)
	if err != nil {
		h.log.WarnContext(ctx, "realtime: ticket user unparseable", "error", err)
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// The session re-check comes BEFORE burning the nonce: a logged-out user's
	// ticket should be refused without consuming it, so the (equally refused)
	// retry reports the same reason rather than "already spent".
	live, err := h.sessions.SessionLive(ctx, ticket.SessionID)
	if err != nil {
		h.log.ErrorContext(ctx, "realtime: session check failed", "error", err)
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if !live {
		h.log.WarnContext(ctx, "realtime: ticket for a dead session")
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Burn the nonce with the ticket's REMAINING lifetime, so the key expires
	// exactly when the ticket does — remembering a spent nonce for longer than
	// the ticket could have been valid buys nothing.
	remaining := ticket.ExpiresAt.Sub(h.now())
	fresh, err := h.burner.Burn(ctx, ticket.Nonce, remaining)
	if err != nil {
		h.log.ErrorContext(ctx, "realtime: burn ticket nonce", "error", err)
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable")
		return
	}
	if !fresh {
		h.log.WarnContext(ctx, "realtime: ticket replay refused")
		httpx.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	release, err := h.counter.acquire(workspaceID, userID)
	if err != nil {
		h.log.WarnContext(ctx, "realtime: connection cap reached", "error", err)
		httpx.Error(w, http.StatusTooManyRequests, "too many connections")
		return
	}
	defer release()

	// Attach BEFORE upgrading: if the hub cannot subscribe, a plain HTTP error is
	// still possible. After the Upgrade writes its 101 the status line is spent,
	// and the only way to report a failure is a close frame.
	frames, err := h.hub.Attach(ctx, workspaceID, lastSeq(r))
	if err != nil {
		h.log.ErrorContext(ctx, "realtime: hub attach", "error", err)
		httpx.Error(w, http.StatusServiceUnavailable, "unavailable")
		return
	}

	conn, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade has already written its own response (including the 403 an Origin
		// rejection produces), so do not write another.
		h.log.WarnContext(ctx, "realtime: upgrade failed", "error", err)
		return
	}
	defer func() {
		if err := conn.Close(); err != nil {
			h.log.DebugContext(ctx, "realtime: close socket", "error", err)
		}
	}()

	h.pump(ctx, conn, frames)
}

// lastSeq reads the client's replay position. Absent or unparseable means "from
// now": a fresh connection, not a replay from zero — a client that has seen
// nothing wants the live feed, and replaying the whole window to it would
// deliver events it will also fetch in its initial page load.
func lastSeq(r *http.Request) int64 {
	raw := r.URL.Query().Get("last_seq")
	if raw == "" {
		return -1
	}
	var n int64
	for _, c := range raw {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int64(c-'0')
		if n > 1<<62 {
			return -1
		}
	}
	return n
}

// pump writes hub frames to the socket and runs the keepalive.
//
// The read side exists only to service pongs and to notice the client going
// away: this transport is server→client today (spec §5), so any client message
// is discarded rather than interpreted. Presence (slice 8) is what will give the
// read side real work.
func (h *Handler) pump(ctx context.Context, conn *websocket.Conn, frames <-chan platformrealtime.Frame) {
	// The server's WriteTimeout (30s) applies to the whole connection once headers
	// are read and would kill this socket (spec §4). Hijack has already detached
	// it from that machinery; clearing the deadlines here and setting per-message
	// ones below is what replaces it. Widening WriteTimeout globally would remove
	// a Slowloris bound from every other route.
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Reader goroutine: drains and discards client frames, refreshing the read
	// deadline on every pong. Its exit (a dead peer, a close frame, a missed pong)
	// cancels the writer.
	go func() {
		defer cancel()
		if err := conn.SetReadDeadline(h.now().Add(pongWait)); err != nil {
			return
		}
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(h.now().Add(pongWait))
		})
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Shutdown or a dead peer. Try to say why, but never block on it.
			_ = conn.SetWriteDeadline(h.now().Add(writeWait))
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, ""))
			return

		case frame, ok := <-frames:
			if !ok {
				// The hub dropped this reader (a slow consumer, or Close on shutdown).
				// Closing normally tells the client to reconnect and replay, which is
				// the designed recovery.
				_ = conn.SetWriteDeadline(h.now().Add(writeWait))
				_ = conn.WriteMessage(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := conn.SetWriteDeadline(h.now().Add(writeWait)); err != nil {
				return
			}
			// The hub already encoded the envelope, so this writes bytes rather than
			// re-marshalling per connection.
			if err := conn.WriteMessage(websocket.TextMessage, frame.Data); err != nil {
				return
			}

		case <-ticker.C:
			if err := conn.SetWriteDeadline(h.now().Add(writeWait)); err != nil {
				return
			}
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Compile-time proof the hub satisfies the read seam this handler needs.
var _ Attacher = (*platformrealtime.Hub)(nil)
