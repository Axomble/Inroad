package mail

import (
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // RFC 2195 defines CRAM-MD5 as HMAC-MD5; speaking the mechanism a server offers is the point, and the alternative for such a server is no connection at all.
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-sasl"
)

// imapMechanisms is the SASL preference order this package negotiates, most
// preferred first, and only among the mechanisms a server actually advertises.
//
// The ordering is a COMPATIBILITY choice, not a security one: every IMAP
// connection this package makes is already encrypted (implicit TLS on 993,
// mandatory STARTTLS on 143 — see dialIMAP), so the credential is protected on
// the wire whichever mechanism carries it. CRAM-MD5 leads because a server that
// offers it is usually a server that prefers it, and it is the mechanism most
// often offered by the small self-hosted servers that have no other option;
// PLAIN trails because it is universally available and therefore never the one
// that decides whether a mailbox can connect at all.
//
// What the order deliberately does NOT do is bet a working mailbox on it. Any
// mechanism failure falls back to the plain LOGIN command when the server has
// not disabled it (authenticateIMAP), so a server advertising a mechanism its
// accounts cannot use ends up exactly where it is today.
var imapMechanisms = []string{mechCramMD5, mechLogin, mechPlain}

const (
	mechCramMD5 = "CRAM-MD5"
	mechLogin   = "LOGIN"
	mechPlain   = "PLAIN"
)

// ErrAuthRejected marks a failure at the AUTHENTICATION step: the server
// understood the command and refused the credential. It wraps rather than
// replaces the server's own error, so the detail an operator needs is still
// there and the sentinel is still testable.
//
// It exists because "the password is wrong" and "the server is down" want
// opposite handling from the poller — see ClassifyConnectFailure, which reads
// transport evidence BEFORE this sentinel precisely because a connection that
// drops mid-LOGIN also fails here.
//
// No error wrapped by it carries the password: the digests, prompts and PLAIN
// payload stay inside the sasl.Client implementations below, and the underlying
// errors carry only what the server said (docs/security.md, credential
// handling).
var ErrAuthRejected = errors.New("mail: authentication rejected")

// ErrNoIMAPAuthMechanism is returned when a server disables the LOGIN command
// and advertises no SASL mechanism this package implements. It is a
// configuration fact an operator can act on ("this server wants GSSAPI"), which
// is why it is a sentinel rather than an opaque server string.
var ErrNoIMAPAuthMechanism = errors.New("mail: no supported IMAP authentication mechanism")

// authenticateIMAP logs cfg's credentials into an already-connected client,
// negotiating a mechanism instead of assuming the LOGIN command works.
//
// The unconditional c.Login() this replaces is the single largest source of
// "the mailbox connected and then silently never polled": a server advertising
// LOGINDISABLED — which RFC 3501 requires of any server that will not take a
// plaintext LOGIN — makes go-imap fail the call locally, without sending
// anything, and the mailbox stays 'active' forever.
//
// At most TWO authentication attempts are made per connection: the preferred
// advertised mechanism, then the LOGIN command. That ceiling is deliberate.
// Walking every mechanism would turn one wrong password into four rejected
// sign-ins per poll, every three minutes, which is how a provider decides to
// lock an account — and the second attempt only happens when the first already
// failed, so a healthy mailbox still authenticates exactly once.
//
// No error returned from here contains cfg.Password, or any value derived from
// it: the CRAM-MD5 digest, the LOGIN prompts and the PLAIN payload are all
// confined to the sasl.Client implementations below (docs/security.md, the
// credential-handling invariant).
func authenticateIMAP(c *client.Client, cfg IMAPConfig) error {
	loginDisabled, err := c.Support("LOGINDISABLED")
	if err != nil {
		return fmt.Errorf("imap capability: %w", err)
	}
	mech, err := pickIMAPMechanism(c)
	if err != nil {
		return fmt.Errorf("imap capability: %w", err)
	}

	if mech == "" {
		if loginDisabled {
			return ErrNoIMAPAuthMechanism
		}
		return loginIMAP(c, cfg)
	}

	mechErr := c.Authenticate(imapSASLClient(mech, cfg))
	if mechErr == nil {
		return nil
	}
	mechErr = fmt.Errorf("%w: imap authenticate %s: %w", ErrAuthRejected, mech, mechErr)
	if loginDisabled {
		return mechErr
	}
	// The fallback that makes the preference order safe to have at all.
	if loginErr := loginIMAP(c, cfg); loginErr != nil {
		return errors.Join(mechErr, loginErr)
	}
	return nil
}

// pickIMAPMechanism returns the most preferred mechanism the server advertises,
// or "" when it advertises none of them. c.SupportAuth issues a CAPABILITY
// command only if the greeting did not already carry one.
func pickIMAPMechanism(c *client.Client) (string, error) {
	for _, mech := range imapMechanisms {
		ok, err := c.SupportAuth(mech)
		if err != nil {
			return "", err
		}
		if ok {
			return mech, nil
		}
	}
	return "", nil
}

// loginIMAP runs the plain LOGIN command — the path every mailbox that works
// today takes, kept as both the default for servers that advertise no mechanism
// and the fallback for a mechanism that fails.
func loginIMAP(c *client.Client, cfg IMAPConfig) error {
	if err := c.Login(cfg.Username, cfg.Password); err != nil {
		return fmt.Errorf("%w: imap login: %w", ErrAuthRejected, err)
	}
	return nil
}

// imapSASLClient builds the client for a mechanism pickIMAPMechanism chose, so
// the switch is exhaustive over imapMechanisms by construction.
func imapSASLClient(mech string, cfg IMAPConfig) sasl.Client {
	switch mech {
	case mechCramMD5:
		return newCramMD5Client(cfg.Username, cfg.Password)
	case mechLogin:
		return newIMAPLoginClient(cfg.Username, cfg.Password)
	default:
		return sasl.NewPlainClient("", cfg.Username, cfg.Password)
	}
}

// cramMD5Client implements RFC 2195: the server sends a challenge, the client
// answers "username hex(hmac-md5(password, challenge))". The password itself
// never leaves the process.
//
// Written here rather than taken from go-sasl because the version in this
// module graph (v0.0.0-20200509203442) ships anonymous, external, login,
// oauthbearer and plain — and no CRAM-MD5 client at all. The mechanism is one
// HMAC, and TestCramMD5ClientMatchesTheRFC2195Example checks it against the
// specification's own worked example.
type cramMD5Client struct {
	username, password string
	answered           bool
}

func newCramMD5Client(username, password string) *cramMD5Client {
	return &cramMD5Client{username: username, password: password}
}

// Start names the mechanism. CRAM-MD5 has no initial response — the exchange
// begins with the server's challenge — so ir is nil, which go-imap serializes
// as a bare "AUTHENTICATE CRAM-MD5" even when the server advertises SASL-IR.
func (a *cramMD5Client) Start() (mech string, ir []byte, err error) {
	return mechCramMD5, nil, nil
}

// Next answers the server's challenge. It refuses a second one: RFC 2195 is a
// single round trip, and a server that keeps prompting is either broken or
// fishing for repeated digests of the same secret.
func (a *cramMD5Client) Next(challenge []byte) ([]byte, error) {
	if a.answered {
		return nil, errors.New("mail: unexpected second CRAM-MD5 challenge")
	}
	if len(challenge) == 0 {
		return nil, errors.New("mail: empty CRAM-MD5 challenge")
	}
	a.answered = true
	mac := hmac.New(md5.New, []byte(a.password))
	mac.Write(challenge)
	return []byte(a.username + " " + hex.EncodeToString(mac.Sum(nil))), nil
}

// imapLoginClient implements the (obsolete but widely required) LOGIN SASL
// mechanism, tolerating BOTH shapes servers use for it.
//
// go-sasl's own LOGIN client is not usable here: it sends the username as the
// initial response and then accepts exactly one challenge, the literal
// "Password:", returning ErrUnexpectedServerChallenge for anything else. A
// server that does not advertise SASL-IR has nowhere to put an initial response
// and opens with "Username:" — which is what Dovecot, Courier and Exchange all
// send — so go-sasl's client cannot authenticate to the servers AUTH=LOGIN
// exists for. This one answers whichever prompt arrives.
type imapLoginClient struct {
	username, password string
	sentUsername       bool
}

func newIMAPLoginClient(username, password string) *imapLoginClient {
	return &imapLoginClient{username: username, password: password}
}

// Start offers the username as the initial response, which a SASL-IR server
// puts on the command line and a server without it ignores.
func (a *imapLoginClient) Start() (mech string, ir []byte, err error) {
	return mechLogin, []byte(a.username), nil
}

// Next answers one prompt. A prompt that looks like a password prompt gets the
// password; the first of anything else (including the empty continuation) gets
// the username; anything after that gets the password, since LOGIN has exactly
// two steps and a second unrecognised prompt can only be the second one.
func (a *imapLoginClient) Next(challenge []byte) ([]byte, error) {
	if isPasswordPrompt(challenge) || a.sentUsername {
		return []byte(a.password), nil
	}
	a.sentUsername = true
	return []byte(a.username), nil
}

// isPasswordPrompt recognises the password step of the LOGIN mechanism. Matching
// on a prefix rather than the exact "Password:" string is deliberate: the
// mechanism was never standardised beyond a draft, and servers differ on the
// colon and on capitalisation.
func isPasswordPrompt(challenge []byte) bool {
	return strings.HasPrefix(strings.ToLower(string(challenge)), "pass")
}
