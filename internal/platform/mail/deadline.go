package mail

import (
	"net"
	"time"
)

// deadlineConn gives a net.Conn a PER-RESPONSE deadline: every Read and every
// Write re-arms the socket deadline to now+timeout, so the bound is "this server
// went quiet for longer than timeout", not "the whole conversation took longer
// than timeout". A slow-but-progressing server is never cut off mid-exchange; a
// server that accepts the connection and then says nothing is.
//
// It exists for the IMAP path (dialIMAP), where go-imap's own Client.Timeout
// cannot cover the whole session: client.New READS THE GREETING, and Timeout
// cannot be set until New returns. go-imap's DialWithDialer works around that
// with a one-shot conn deadline; dialIMAP hand-rolls the dial to get a context
// and so has to arm the conn itself. Re-arming per read/write additionally
// covers the STARTTLS handshake, which neither mechanism bounded.
//
// The SMTP side needs no equivalent: gomail sets a conn deadline at dial and
// refreshes it per phase, and its dial is context-aware.
//
// It deliberately does NOT re-arm on a partial read only: Read and Write are
// called per syscall, so each one that makes progress extends the allowance,
// which is the intended "still talking to me" semantic. That does impose an IDLE
// limit — a connection left open with no traffic for longer than timeout dies —
// which is safe here because every IMAP session this package opens runs a short
// burst of commands and logs out. Nothing uses IDLE.
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

// newDeadlineConn wraps conn so each read/write must complete within timeout,
// falling back to fallback when timeout is non-positive (an unset caller
// Timeout). It arms the deadline immediately so the first response — the
// greeting, which no dial timeout covers — is bounded too.
func newDeadlineConn(conn net.Conn, timeout, fallback time.Duration) net.Conn {
	if timeout <= 0 {
		timeout = fallback
	}
	c := &deadlineConn{Conn: conn, timeout: timeout}
	c.arm()
	return c
}

// arm pushes the socket deadline out by one timeout. A SetDeadline error is
// dropped on purpose: the only way it fails is a conn that is already closed or
// does not support deadlines, and in both cases the Read/Write that follows
// reports the real problem. Returning it here would mean failing a healthy
// exchange over a bookkeeping call.
func (c *deadlineConn) arm() {
	_ = c.SetDeadline(time.Now().Add(c.timeout))
}

func (c *deadlineConn) Read(b []byte) (int, error) {
	c.arm()
	return c.Conn.Read(b)
}

func (c *deadlineConn) Write(b []byte) (int, error) {
	c.arm()
	return c.Conn.Write(b)
}
