-- Persist the SMTP TLS policy so the connect-test and the send path apply the
-- SAME rule (send-path hardening MAJOR 2). Before this, allow_plaintext gated
-- only the connect-test and was never stored, while the send path always forced
-- TLSMandatory — so a self-hoster who connect-tested a plaintext relay with
-- allow_plaintext=true saved a mailbox whose every send then failed. Persisting
-- allow_plaintext lets the send job read the same policy the test validated.
-- Default false = TLS enforced (security Invariant 6): an omitted value can
-- never silently downgrade to cleartext.
ALTER TABLE mailboxes ADD COLUMN allow_plaintext BOOLEAN NOT NULL DEFAULT FALSE;

-- Drop the dead use_tls flag (MINOR 3): it was never consulted by the send path
-- (which unconditionally enforced TLS) and contradicted allow_plaintext. Its
-- request DTO field and coreapi job carriers are removed alongside this column.
ALTER TABLE mailboxes DROP COLUMN use_tls;
