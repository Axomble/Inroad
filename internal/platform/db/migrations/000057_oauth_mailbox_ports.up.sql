-- 000028 hardened smtp_port/imap_port with unconditional BETWEEN 1 AND 65535
-- checks, but OAuth mailboxes (gmail/m365) have never had SMTP/IMAP endpoints:
-- completeOAuth persists them with the fields zeroed ("their zero values are
-- fine" — internal/app/mailbox/oauth.go). The two shipped in different phases
-- and first met on a real Gmail connect, which died on
-- mailboxes_imap_port_check before the row ever existed.
--
-- Replace both checks with provider-aware ones: an smtp mailbox must carry a
-- real port; an OAuth mailbox must carry exactly 0, so a half-filled hybrid
-- (an OAuth row smuggling SMTP credentials past the connection test) stays
-- unrepresentable.
ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_smtp_port_check;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_smtp_port_check
    CHECK (CASE WHEN provider = 'smtp' THEN smtp_port BETWEEN 1 AND 65535 ELSE smtp_port = 0 END);

ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_imap_port_check;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_imap_port_check
    CHECK (CASE WHEN provider = 'smtp' THEN imap_port BETWEEN 1 AND 65535 ELSE imap_port = 0 END);
