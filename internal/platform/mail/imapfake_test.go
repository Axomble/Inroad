package mail

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // CRAM-MD5 (RFC 2195) is defined in terms of HMAC-MD5; the fake server must speak the mechanism the real ones do.
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/utf7"
)

// A scriptable in-process IMAP server.
//
// WHY THIS EXISTS: every P1.8 compatibility bug is a "works against my provider,
// fails against yours" bug. None of them can be demonstrated against Gmail, and
// none of them can be demonstrated by a unit test of a pure function, because the
// bug IS the conversation on the wire. This server lets a test withhold a
// capability (AUTH=CRAM-MD5, SASL-IR, LOGINDISABLED), name the inbox something
// other than INBOX, answer a command the way a specific real server answers it,
// or accept the connection and then say nothing — and then assert what the
// client actually sent.
//
// It is reached through dialIMAPTransport, the socket-level seam in dial.go, so
// vetAddr still runs on every dial these tests make: the scripts below use
// 203.0.113.x (TEST-NET-3, which passes the guard without DNS) exactly as the
// pre-existing cancellation tests do, and a loopback host still fails the vet
// before the seam is consulted (TestTransportSeamDoesNotBypassTheSSRFGuard).
//
// It is NOT a general-purpose IMAP server and should not grow into one. It
// implements the commands this package sends and nothing else.

// imapScript is the server's whole behaviour, declared per test.
type imapScript struct {
	// Caps are the capabilities advertised in the greeting and in reply to
	// CAPABILITY, IMAP4rev1 aside (which is always advertised). Nil means a bare
	// IMAP4rev1 server: no SASL mechanism, no SASL-IR.
	Caps []string
	// User and Pass are the credentials every authentication path is checked
	// against. Empty Pass accepts any password.
	User, Pass string
	// LoginCommandReply overrides the tagged reply to the LOGIN command, e.g.
	// "NO [PRIVACYREQUIRED] plaintext login is not available". Empty means
	// "check the credentials".
	LoginCommandReply string
	// RefuseMechanisms are ADVERTISED in Caps but always answered NO — the server
	// that offers a mechanism its accounts cannot actually use, which is the shape
	// that would break a working mailbox if negotiation had no fallback.
	RefuseMechanisms []string
	// UsernameChallenge makes AUTH=LOGIN send the base64 "Username:" prompt that
	// real servers send, instead of the empty continuation that go-sasl's own
	// LOGIN client is the only thing that copes with.
	UsernameChallenge bool
	// Mailboxes is the LIST output.
	Mailboxes []imapMailbox
	// Inbox is the mailbox SELECT/EXAMINE resolves to; anything else is refused
	// with NO. Empty means "INBOX".
	Inbox string
	// Messages are the messages the inbox holds, keyed by UID.
	Messages []imapFakeMessage
	// UIDValidity and UIDNext are reported on SELECT/EXAMINE. Zero UIDValidity
	// becomes 1; zero UIDNext is derived from Messages.
	UIDValidity, UIDNext uint32
	// SilentAfterGreeting accepts the connection, sends nothing at all, and waits
	// — the black-holed server a dial timeout does not cover.
	SilentAfterGreeting bool
}

// imapMailbox is one LIST entry: its name and its attributes (RFC 6154
// special-use flags among them).
type imapMailbox struct {
	Name  string
	Attrs []string
}

// imapFakeMessage is one message the fake inbox holds.
type imapFakeMessage struct {
	UID uint32
	Raw string
}

// fakeIMAP is a running imapScript.
type fakeIMAP struct {
	script imapScript
	ln     net.Listener

	mu       sync.Mutex
	commands []string
	// selected is the last mailbox the client SELECTed or EXAMINEd, which is how
	// a junk-folder test says which folder the resolution chose.
	selected string
	// authenticated records whether any connection reached the authenticated
	// state, so a test can assert the negotiation SUCCEEDED rather than merely
	// that a particular command was sent.
	authenticated bool
}

// startFakeIMAP starts the server, installs it as this package's IMAP transport
// for the duration of the test, and tears both down afterwards.
//
// The transport swap is a package-level assignment, so these tests must not call
// t.Parallel() — the same constraint setResolver already imposes in guard_test.go,
// and the reason the helper does the restore rather than leaving it to the caller.
func startFakeIMAP(t *testing.T, script imapScript) *fakeIMAP {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakeIMAP{script: script, ln: ln}

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

	prev := dialIMAPTransport
	dialIMAPTransport = func(ctx context.Context, _ string, _ IMAPConfig, dialer *net.Dialer) (net.Conn, error) {
		return dialer.DialContext(ctx, "tcp", ln.Addr().String())
	}
	t.Cleanup(func() {
		dialIMAPTransport = prev
		_ = ln.Close()
	})
	return s
}

// stopListening closes the listener while leaving the transport seam pointed at
// its (now dead) address, so the next dial gets a REAL ECONNREFUSED from the
// kernel.
//
// It is how "the mail server is down" is tested without mocking the reader: an
// unreachable-server test that substitutes a failing io.Reader is a test of the
// substitute. t.Cleanup closes the listener a second time, which is a no-op
// error this harness already ignores.
func (s *fakeIMAP) stopListening(t *testing.T) {
	t.Helper()
	if err := s.ln.Close(); err != nil {
		t.Fatalf("closing the fake listener: %v", err)
	}
}

// commandLog returns every command line the server received, in order, with the
// client's tag stripped. Credentials are NOT stripped: asserting that a secret
// did or did not cross the wire is one of the things this harness is for.
func (s *fakeIMAP) commandLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// sawAuthenticated reports whether a client reached the authenticated state.
func (s *fakeIMAP) sawAuthenticated() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.authenticated
}

func (s *fakeIMAP) record(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, line)
}

func (s *fakeIMAP) markAuthenticated() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authenticated = true
}

// capabilityLine is the CAPABILITY payload, IMAP4rev1 always first.
func (s *fakeIMAP) capabilityLine() string {
	return strings.Join(append([]string{"IMAP4rev1"}, s.script.Caps...), " ")
}

func (s *fakeIMAP) hasCap(name string) bool {
	for _, c := range s.script.Caps {
		if strings.EqualFold(c, name) {
			return true
		}
	}
	return false
}

func (s *fakeIMAP) inboxName() string {
	if s.script.Inbox == "" {
		return "INBOX"
	}
	return s.script.Inbox
}

// selectable reports whether name can be SELECTed: the inbox, or any listed
// mailbox that is not a \Noselect placeholder. Every selectable mailbox serves
// the same Messages — these tests are about WHICH folder is chosen, not about
// per-folder contents.
func (s *fakeIMAP) selectable(name string) bool {
	if strings.EqualFold(name, s.inboxName()) {
		return true
	}
	for _, m := range s.script.Mailboxes {
		if !strings.EqualFold(m.Name, name) {
			continue
		}
		for _, a := range m.Attrs {
			if strings.EqualFold(a, "\\Noselect") {
				return false
			}
		}
		return true
	}
	return false
}

// selectedMailbox returns the last mailbox the client SELECTed or EXAMINEd.
func (s *fakeIMAP) selectedMailbox() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.selected
}

// serve runs one connection to completion.
func (s *fakeIMAP) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	if s.script.SilentAfterGreeting {
		// Hold the connection open and say nothing: the client must be the one
		// that gives up. Bounded so a failing test cannot leak the goroutine.
		time.Sleep(30 * time.Second)
		return
	}
	sess := &imapSession{srv: s, r: bufio.NewReader(conn), w: conn}
	sess.send("* OK [CAPABILITY " + s.capabilityLine() + "] fake IMAP ready")
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

// imapSession is one client connection's state.
type imapSession struct {
	srv *fakeIMAP
	r   *bufio.Reader
	w   io.Writer
}

func (c *imapSession) readLine() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (c *imapSession) send(line string) {
	_, _ = io.WriteString(c.w, line+"\r\n")
}

// handle dispatches one tagged command, reporting whether the connection is done.
func (c *imapSession) handle(line string) bool {
	c.srv.record(line)
	tag, rest, _ := strings.Cut(line, " ")
	name, args, _ := strings.Cut(rest, " ")
	switch strings.ToUpper(name) {
	case "CAPABILITY":
		c.send("* CAPABILITY " + c.srv.capabilityLine())
		c.send(tag + " OK CAPABILITY completed")
	case "NOOP":
		c.send(tag + " OK NOOP completed")
	case "LOGOUT":
		c.send("* BYE logging out")
		c.send(tag + " OK LOGOUT completed")
		return true
	case "LOGIN":
		c.login(tag, args)
	case "AUTHENTICATE":
		c.authenticate(tag, args)
	case "LIST":
		c.list(tag)
	case "SELECT", "EXAMINE":
		c.selectMailbox(tag, args, strings.EqualFold(name, "EXAMINE"))
	case "UID":
		c.uid(tag, args)
	default:
		c.send(tag + " BAD unknown command")
	}
	return false
}

func (c *imapSession) login(tag, args string) {
	if c.srv.hasCap("LOGINDISABLED") {
		c.send(tag + " NO [PRIVACYREQUIRED] LOGIN is disabled")
		return
	}
	if reply := c.srv.script.LoginCommandReply; reply != "" {
		c.send(tag + " " + reply)
		return
	}
	fields := imapFields(args)
	if len(fields) != 2 || !c.srv.credentialsOK(fields[0], fields[1]) {
		c.send(tag + " NO [AUTHENTICATIONFAILED] bad credentials")
		return
	}
	c.srv.markAuthenticated()
	c.send(tag + " OK LOGIN completed")
}

// authenticate runs one SASL exchange. The client may have sent an initial
// response on the command line (SASL-IR); when it has not, each mechanism's
// first continuation is what drives the client to send one.
func (c *imapSession) authenticate(tag, args string) {
	mech, initial, _ := strings.Cut(args, " ")
	mech = strings.ToUpper(mech)
	if !c.srv.hasCap("AUTH=" + mech) {
		c.send(tag + " NO unsupported authentication mechanism")
		return
	}
	for _, refused := range c.srv.script.RefuseMechanisms {
		if strings.EqualFold(refused, mech) {
			c.send(tag + " NO [AUTHENTICATIONFAILED] mechanism not available for this account")
			return
		}
	}
	var user, pass string
	var err error
	switch mech {
	case "PLAIN":
		user, pass, err = c.authPlain(initial)
	case "LOGIN":
		user, pass, err = c.authLogin(initial)
	case "CRAM-MD5":
		user, pass, err = c.authCramMD5()
	default:
		c.send(tag + " NO unsupported authentication mechanism")
		return
	}
	if err != nil {
		c.send(tag + " BAD " + err.Error())
		return
	}
	if !c.srv.credentialsOK(user, pass) {
		c.send(tag + " NO [AUTHENTICATIONFAILED] bad credentials")
		return
	}
	c.srv.markAuthenticated()
	c.send(tag + " OK AUTHENTICATE completed")
}

// challenge sends a base64 continuation and reads the client's base64 reply.
func (c *imapSession) challenge(payload string) ([]byte, error) {
	c.send("+ " + base64.StdEncoding.EncodeToString([]byte(payload)))
	return c.readBase64()
}

func (c *imapSession) readBase64() ([]byte, error) {
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	c.srv.record("<continuation response>")
	if line == "*" {
		return nil, fmt.Errorf("client cancelled the exchange")
	}
	return base64.StdEncoding.DecodeString(line)
}

func (c *imapSession) authPlain(initial string) (user, pass string, err error) {
	var raw []byte
	if initial != "" {
		raw, err = base64.StdEncoding.DecodeString(initial)
	} else {
		raw, err = c.challenge("")
	}
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(string(raw), "\x00")
	if len(parts) != 3 {
		return "", "", fmt.Errorf("malformed PLAIN response")
	}
	return parts[1], parts[2], nil
}

// authLogin runs the LOGIN mechanism. UsernameChallenge decides which of the two
// shapes real servers use: the base64 "Username:" prompt (Dovecot, Courier,
// Exchange), or the empty continuation that go-sasl's LOGIN client is built
// around. The distinction is the point — a client that only handles the second
// cannot authenticate to the servers that send the first.
func (c *imapSession) authLogin(initial string) (user, pass string, err error) {
	if initial != "" {
		raw, decErr := base64.StdEncoding.DecodeString(initial)
		if decErr != nil {
			return "", "", decErr
		}
		user = string(raw)
	} else {
		prompt := ""
		if c.srv.script.UsernameChallenge {
			prompt = "Username:"
		}
		raw, cErr := c.challenge(prompt)
		if cErr != nil {
			return "", "", cErr
		}
		user = string(raw)
	}
	raw, err := c.challenge("Password:")
	if err != nil {
		return "", "", err
	}
	return user, string(raw), nil
}

// authCramMD5 runs RFC 2195: the server sends a challenge, the client answers
// "username hex(hmac-md5(password, challenge))".
func (c *imapSession) authCramMD5() (user, pass string, err error) {
	chal := "<1896.697170952@fake.invalid>"
	raw, err := c.challenge(chal)
	if err != nil {
		return "", "", err
	}
	name, digest, ok := strings.Cut(string(raw), " ")
	if !ok {
		return "", "", fmt.Errorf("malformed CRAM-MD5 response")
	}
	mac := hmac.New(md5.New, []byte(c.srv.script.Pass))
	mac.Write([]byte(chal))
	if !hmac.Equal([]byte(digest), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		// Signal a credential mismatch by returning a password that cannot match.
		return name, "\x00cram-md5-digest-mismatch", nil
	}
	return name, c.srv.script.Pass, nil
}

// credentialsOK checks a username/password pair against the script.
func (s *fakeIMAP) credentialsOK(user, pass string) bool {
	if s.script.User != "" && user != s.script.User {
		return false
	}
	return s.script.Pass == "" || pass == s.script.Pass
}

func (c *imapSession) list(tag string) {
	for _, m := range c.srv.script.Mailboxes {
		c.send(fmt.Sprintf("* LIST (%s) \"/\" %q", strings.Join(m.Attrs, " "), toUTF7(m.Name)))
	}
	c.send(tag + " OK LIST completed")
}

// toUTF7 encodes a mailbox name the way IMAP4rev1 requires (modified UTF-7,
// RFC 3501 §5.1.3). Scripts are written in plain UTF-8 — a localized folder
// name is the point of several tests — and a real server would never put those
// bytes on the wire raw, so the fake must not either.
func toUTF7(name string) string {
	encoded, err := utf7.Encoding.NewEncoder().String(name)
	if err != nil {
		return name
	}
	return encoded
}

// fromUTF7 is the inverse, for command arguments the client sends.
func fromUTF7(name string) string {
	decoded, err := utf7.Encoding.NewDecoder().String(name)
	if err != nil {
		return name
	}
	return decoded
}

func (c *imapSession) selectMailbox(tag, args string, readOnly bool) {
	fields := imapFields(args)
	if len(fields) == 0 {
		c.send(tag + " BAD missing mailbox name")
		return
	}
	name := fromUTF7(fields[0])
	if !c.srv.selectable(name) {
		c.send(tag + " NO [NONEXISTENT] mailbox does not exist")
		return
	}
	c.srv.mu.Lock()
	c.srv.selected = name
	c.srv.mu.Unlock()
	uidValidity := c.srv.script.UIDValidity
	if uidValidity == 0 {
		uidValidity = 1
	}
	uidNext := c.srv.script.UIDNext
	if uidNext == 0 {
		for _, m := range c.srv.script.Messages {
			if m.UID >= uidNext {
				uidNext = m.UID + 1
			}
		}
		if uidNext == 0 {
			uidNext = 1
		}
	}
	c.send("* FLAGS (\\Seen \\Answered \\Flagged \\Deleted \\Draft)")
	c.send(fmt.Sprintf("* %d EXISTS", len(c.srv.script.Messages)))
	c.send("* 0 RECENT")
	c.send(fmt.Sprintf("* OK [UIDVALIDITY %d] UIDs valid", uidValidity))
	c.send(fmt.Sprintf("* OK [UIDNEXT %d] predicted next UID", uidNext))
	access := "READ-WRITE"
	if readOnly {
		access = "READ-ONLY"
	}
	c.send(fmt.Sprintf("%s OK [%s] completed", tag, access))
}

// uid handles UID FETCH. The requested range is ignored on purpose: what these
// tests assert is the surrounding conversation, and uidRangeSeqSet already has
// its own unit tests for the range itself.
func (c *imapSession) uid(tag, args string) {
	sub, _, _ := strings.Cut(args, " ")
	if !strings.EqualFold(sub, "FETCH") {
		c.send(tag + " BAD unsupported UID subcommand")
		return
	}
	for i, m := range c.srv.script.Messages {
		body := strings.ReplaceAll(m.Raw, "\n", "\r\n")
		c.send(fmt.Sprintf("* %d FETCH (UID %d BODY[] {%d}", i+1, m.UID, len(body)))
		_, _ = io.WriteString(c.w, body+")\r\n")
	}
	c.send(tag + " OK UID FETCH completed")
}

// imapFields splits a command's arguments, honouring double-quoted strings. It
// is deliberately minimal: no literals, no nesting — the commands this package
// sends never need them.
func imapFields(s string) []string {
	var out []string
	var cur strings.Builder
	inQuotes, started := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			started = true
		case r == ' ' && !inQuotes:
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}
