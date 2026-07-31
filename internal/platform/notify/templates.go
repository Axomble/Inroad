package notify

import (
	"fmt"
	"html"
)

// VerifyEmail renders the "confirm your email address" message. link should
// be the full verify-email URL (including token query param).
func VerifyEmail(link string) Message {
	return Message{
		Subject:  "Verify your email",
		TextBody: fmt.Sprintf("Confirm your email address:\n\n%s\n\nThis link expires in 24 hours.", link),
		HTMLBody: fmt.Sprintf(`<p>Confirm your email address:</p><p><a href=%q>Verify email</a></p><p>This link expires in 24 hours.</p>`, link),
	}
}

// ResetEmail renders the "reset your password" message. link should be the
// full reset-password URL (including token query param).
func ResetEmail(link string) Message {
	return Message{
		Subject:  "Reset your password",
		TextBody: fmt.Sprintf("Reset your password:\n\n%s\n\nThis link expires in 1 hour. If you didn't request this, ignore this email.", link),
		HTMLBody: fmt.Sprintf(`<p>Reset your password:</p><p><a href=%q>Reset password</a></p><p>Expires in 1 hour. If you didn't request this, ignore this email.</p>`, link),
	}
}

// LoginCodeEmail renders the passwordless login one-time-code message. code is
// the raw numeric code the user just requested; it is single-use and short-lived.
// It is embedded verbatim (a fixed-format numeric string, never user-controlled)
// so there is nothing to escape.
func LoginCodeEmail(code string) Message {
	return Message{
		Subject:  "Your login code",
		TextBody: fmt.Sprintf("Your login code is:\n\n%s\n\nIt expires in 10 minutes. If you didn't request it, ignore this email.", code),
		HTMLBody: fmt.Sprintf(`<p>Your login code is:</p><p style="font-size:1.5em"><b>%s</b></p><p>Expires in 10 minutes. If you didn't request it, ignore this email.</p>`, code),
	}
}

// InviteEmail renders the "you're invited to a workspace" message. link
// should be the full accept-invite URL (including token query param).
// workspaceName is user-controlled (workspace display name), so it is HTML-
// escaped before interpolation into HTMLBody; TextBody keeps it literal.
func InviteEmail(workspaceName, link string) Message {
	return Message{
		Subject:  fmt.Sprintf("You're invited to %s", workspaceName),
		TextBody: fmt.Sprintf("You've been invited to join %s:\n\n%s\n\nThis link expires in 72 hours.", workspaceName, link),
		HTMLBody: fmt.Sprintf(`<p>You've been invited to join <b>%s</b>:</p><p><a href=%q>Accept invite</a></p><p>Expires in 72 hours.</p>`, html.EscapeString(workspaceName), link),
	}
}
