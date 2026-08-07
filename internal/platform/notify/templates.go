package notify

import (
	"fmt"
	"html"
)

// Every constructor here takes the recipient as its first argument rather than
// leaving Message.To for the caller to fill in: an unaddressed transactional
// email is undeliverable, and making the recipient a parameter means a new
// template (or a new call site) cannot forget it without failing to compile.

// VerifyEmail renders the "confirm your email address" message to to. link
// should be the full verify-email URL (including token query param).
func VerifyEmail(to, link string) Message {
	return Message{
		To:       to,
		Subject:  "Verify your email",
		TextBody: fmt.Sprintf("Confirm your email address:\n\n%s\n\nThis link expires in 24 hours.", link),
		HTMLBody: fmt.Sprintf(`<p>Confirm your email address:</p><p><a href=%q>Verify email</a></p><p>This link expires in 24 hours.</p>`, link),
	}
}

// ResetEmail renders the "reset your password" message to to. link should be
// the full reset-password URL (including token query param).
func ResetEmail(to, link string) Message {
	return Message{
		To:       to,
		Subject:  "Reset your password",
		TextBody: fmt.Sprintf("Reset your password:\n\n%s\n\nThis link expires in 1 hour. If you didn't request this, ignore this email.", link),
		HTMLBody: fmt.Sprintf(`<p>Reset your password:</p><p><a href=%q>Reset password</a></p><p>Expires in 1 hour. If you didn't request this, ignore this email.</p>`, link),
	}
}

// LoginCodeEmail renders the passwordless login one-time-code message to to.
// code is the raw numeric code the user just requested; it is single-use and
// short-lived. It is embedded verbatim (a fixed-format numeric string, never
// user-controlled) so there is nothing to escape.
func LoginCodeEmail(to, code string) Message {
	return Message{
		To:       to,
		Subject:  "Your login code",
		TextBody: fmt.Sprintf("Your login code is:\n\n%s\n\nIt expires in 10 minutes. If you didn't request it, ignore this email.", code),
		HTMLBody: fmt.Sprintf(`<p>Your login code is:</p><p style="font-size:1.5em"><b>%s</b></p><p>Expires in 10 minutes. If you didn't request it, ignore this email.</p>`, code),
	}
}

// InviteEmail renders the "you're invited to a workspace" message to to, which
// must be the INVITEE's address (the invite row's email), never the inviter's -
// the link it carries is a bearer credential granting workspace membership.
// link should be the full accept-invite URL (including token query param).
// workspaceName is user-controlled (workspace display name), so it is HTML-
// escaped before interpolation into HTMLBody; TextBody keeps it literal.
func InviteEmail(to, workspaceName, link string) Message {
	return Message{
		To:       to,
		Subject:  fmt.Sprintf("You're invited to %s", workspaceName),
		TextBody: fmt.Sprintf("You've been invited to join %s:\n\n%s\n\nThis link expires in 72 hours.", workspaceName, link),
		HTMLBody: fmt.Sprintf(`<p>You've been invited to join <b>%s</b>:</p><p><a href=%q>Accept invite</a></p><p>Expires in 72 hours.</p>`, html.EscapeString(workspaceName), link),
	}
}
