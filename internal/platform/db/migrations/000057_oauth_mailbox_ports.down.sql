-- Restore 000028's unconditional port checks. This fails if OAuth mailboxes
-- (which carry port 0 by design) exist — delete them first; that data loss is
-- inherent to reverting to a schema that cannot represent them.
ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_smtp_port_check;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_smtp_port_check CHECK (smtp_port BETWEEN 1 AND 65535);

ALTER TABLE mailboxes DROP CONSTRAINT mailboxes_imap_port_check;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_imap_port_check CHECK (imap_port BETWEEN 1 AND 65535);
