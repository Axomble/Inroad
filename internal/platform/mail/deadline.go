package mail

import (
	"net"
	"time"
)

// defaultSMTPTimeout bounds an SMTP conversation's per-response wait when the
// caller left Timeout unset (its zero value). It mirrors defaultIMAPTimeout's
// role for dialIMAP, and is the same 30s NewNetSender already picks.
const defaultSMTPTimeout = 30 * time.Second

// deadlineConn gives a net.Conn a PER-RESPONSE deadline: every Read and every
// Write re-arms the socket deadline to now+timeout, so the bound is "this server
// went quiet for longer than timeout", not "the whole conversation took longer
// than timeout". A slow-but-progressing server is never cut off mid-exchange; a
// server that accepts the connection and then says nothing is.
//
// This is the SMTP counterpart of go-imap's Client.Timeout, which dialIMAP
// already sets and which does exactly this for IMAP commands. net/smtp and
// gomail have no equivalent: net/smtp takes no context and no timeout at all, so
// before this the only bound on a connected-then-silent SMTP server was the
// caller eventually going away — and on TestSMTP the caller is an HTTP request.
//
// It deliberately does NOT re-arm on a partial read: Read/Write are called per
// syscall, so each one that makes progress extends the allowance, which is the
// intended "still talking to me" semantic.
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

// newDeadlineConn wraps conn so each read/write must complete within timeout,
// falling back to fallback when timeout is non-positive (an unset caller
// Timeout). It arms the deadline immediately so the first response — the SMTP
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
