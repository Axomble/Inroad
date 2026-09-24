package mail

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // CRAM-MD5 (RFC 2195) is HMAC-MD5; the fake must speak what real servers offer.
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// A scriptable in-process SMTP server, the sender-side counterpart of
// fakeIMAP. Same rationale, same seam: it is reached through
// dialSMTPTransport, which runs only after vetAddr has approved the
// destination, so these tests exercise the production dial path rather than a
// bypass of it.
//
// The scripted conversations run with SMTPConfig.AllowPlaintext set — this
// package's explicit, deliberate cleartext opt-out — because a TLS fake would
// need its certificate trusted, and injecting a root pool is the one thing a
// test must not teach this package to do. What differs between the encrypted
// and cleartext paths is which mechanism variants are offered, and that is
// covered directly by TestSMTPAuthMechanismOrder.

// smtpScript is the fake server's whole behaviour.
type smtpScript struct {
	// Extensions are advertised after the EHLO greeting line, e.g. "AUTH LOGIN"
	// or "SIZE 35882577". Nil means a server with no ESMTP extensions at all.
	Extensions []string
	// User and Pass are the credentials every mechanism is checked against.
	User, Pass string
	// RefuseEHLO answers EHLO with 502, forcing the client back to HELO — the
	// pre-ESMTP server shape.
	RefuseEHLO bool
	// SilentAfterConnect accepts the connection and never sends the greeting —
	// the black-holed server no dial timeout covers.
	SilentAfterConnect bool
}

// fakeSMTP is a running smtpScript.
type fakeSMTP struct {
	script smtpScript
	ln     net.Listener

	mu       sync.Mutex
	commands []string
	helo     string
	authMech string
	data     string
	authOK   bool
}

// startFakeSMTP starts the server and installs it as this package's SMTP
// transport for the duration of the test. Like startFakeIMAP this assigns a
// package-level variable, so its tests must not call t.Parallel().
func startFakeSMTP(t *testing.T, script smtpScript) *fakeSMTP {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeSMTP{script: script, ln: ln}

	var wg sync.WaitGroup
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				wg.Wait()
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serve(conn)
			}()
		}
	}()

	prev := dialSMTPTransport
	dialSMTPTransport = func(ctx context.Context, _ string, dialer *net.Dialer) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", ln.Addr().String())
	}
	t.Cleanup(func() {
		dialSMTPTransport = prev
		_ = ln.Close()
	})
	return s
}

// heloName returns the argument the client sent with EHLO/HELO.
func (s *fakeSMTP) heloName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.helo
}

// mechanism returns the AUTH mechanism the client chose, or "" if it did not
// authenticate.
func (s *fakeSMTP) mechanism() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authMech
}

// authenticated reports whether an AUTH exchange succeeded.
func (s *fakeSMTP) authenticated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authOK
}

// message returns the DATA payload the server accepted.
func (s *fakeSMTP) message() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

// sawCommand reports whether any line the client sent starts with verb.
func (s *fakeSMTP) sawCommand(verb string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range s.commands {
		if strings.HasPrefix(strings.ToUpper(line), strings.ToUpper(verb)) {
			return true
		}
	}
	return false
}

func (s *fakeSMTP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))
	if s.script.SilentAfterConnect {
		// Hold the socket open and say nothing; the client must give up first.
		// Bounded so a failing test cannot leak the goroutine.
		time.Sleep(15 * time.Second)
		return
	}
	sess := &smtpSession{srv: s, r: bufio.NewReader(conn), w: conn}
	sess.send("220 fake.invalid ESMTP ready")
	for {
		line, err := sess.readLine()
		if err != nil {
			return
		}
		if done := sess.handle(line); done {
			return
		}
	}
}

type smtpSession struct {
	srv *fakeSMTP
	r   *bufio.Reader
	w   io.Writer
}

func (c *smtpSession) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *smtpSession) send(line string) {
	_, _ = io.WriteString(c.w, line+"\r\n")
}

func (c *smtpSession) handle(line string) bool {
	c.srv.mu.Lock()
	c.srv.commands = append(c.srv.commands, line)
	c.srv.mu.Unlock()

	verb, args, _ := strings.Cut(line, " ")
	switch strings.ToUpper(verb) {
	case "EHLO":
		c.ehlo(args)
	case "HELO":
		c.srv.setHELO(args)
		c.send("250 fake.invalid")
	case "AUTH":
		c.auth(args)
	case "MAIL", "RCPT", "RSET", "NOOP":
		c.send("250 2.1.0 OK")
	case "DATA":
		c.data()
	case "QUIT":
		c.send("221 2.0.0 bye")
		return true
	default:
		c.send("500 5.5.1 unrecognised command")
	}
	return false
}

func (c *smtpSession) ehlo(args string) {
	if c.srv.script.RefuseEHLO {
		c.send("502 5.5.1 EHLO not implemented")
		return
	}
	c.srv.setHELO(args)
	exts := c.srv.script.Extensions
	if len(exts) == 0 {
		c.send("250 fake.invalid")
		return
	}
	c.send("250-fake.invalid")
	for i, e := range exts {
		sep := "-"
		if i == len(exts)-1 {
			sep = " "
		}
		c.send("250" + sep + e)
	}
}

func (s *fakeSMTP) setHELO(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.helo = name
}

func (c *smtpSession) auth(args string) {
	mech, initial, _ := strings.Cut(args, " ")
	mech = strings.ToUpper(mech)
	c.srv.mu.Lock()
	c.srv.authMech = mech
	c.srv.mu.Unlock()

	var user, pass string
	switch mech {
	case "PLAIN":
		raw := initial
		if raw == "" {
			raw = c.prompt("")
		}
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			c.send("501 5.5.2 cannot decode response")
			return
		}
		parts := strings.Split(string(decoded), "\x00")
		if len(parts) != 3 {
			c.send("501 5.5.2 malformed PLAIN response")
			return
		}
		user, pass = parts[1], parts[2]
	case "LOGIN":
		user = decodeBase64(c.prompt("Username:"))
		pass = decodeBase64(c.prompt("Password:"))
	case "CRAM-MD5":
		const chal = "<4321.1234@fake.invalid>"
		resp := decodeBase64(c.prompt(chal))
		name, digest, ok := strings.Cut(resp, " ")
		if !ok {
			c.send("501 5.5.2 malformed CRAM-MD5 response")
			return
		}
		mac := hmac.New(md5.New, []byte(c.srv.script.Pass))
		mac.Write([]byte(chal))
		if !hmac.Equal([]byte(digest), []byte(hex.EncodeToString(mac.Sum(nil)))) {
			c.send("535 5.7.8 authentication failed")
			return
		}
		user, pass = name, c.srv.script.Pass
	default:
		c.send("504 5.5.4 unrecognised authentication type")
		return
	}

	if (c.srv.script.User != "" && user != c.srv.script.User) ||
		(c.srv.script.Pass != "" && pass != c.srv.script.Pass) {
		c.send("535 5.7.8 authentication failed")
		return
	}
	c.srv.mu.Lock()
	c.srv.authOK = true
	c.srv.mu.Unlock()
	c.send("235 2.7.0 authentication succeeded")
}

// prompt sends a base64 334 challenge and returns the client's raw reply line.
func (c *smtpSession) prompt(payload string) string {
	c.send("334 " + base64.StdEncoding.EncodeToString([]byte(payload)))
	line, err := c.readLine()
	if err != nil {
		return ""
	}
	return line
}

func decodeBase64(s string) string {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (c *smtpSession) data() {
	c.send("354 end with <CRLF>.<CRLF>")
	var b strings.Builder
	for {
		line, err := c.readLine()
		if err != nil {
			return
		}
		if line == "." {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	c.srv.mu.Lock()
	c.srv.data = b.String()
	c.srv.mu.Unlock()
	c.send("250 2.0.0 OK: queued as FAKE1")
}
