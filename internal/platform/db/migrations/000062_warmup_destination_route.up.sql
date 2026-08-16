-- Warmup destination routes, Phase 2 slice C. See
-- docs/superpowers/specs/2026-08-15-warmup-route-matrix-design.md.
--
-- Every placement rate in the engine is pooled across destinations, so a mailbox
-- whose mail reaches Google cleanly and lands in Microsoft's junk half the time
-- reports one blended number that is wrong in both directions: it understates the
-- Microsoft problem and slanders the Google route. This column is the split.
--
-- It records where the mail was DELIVERED, which is decided by the recipient
-- domain's MX and NOT by the recipient mailbox's outbound relay — an smtp mailbox
-- can submit through SendGrid while its inbound MX is Google Workspace. The
-- resolution lives in esp.FromRecipient (provider, or the recipient_domains MX
-- cache), never in esp.FromMailbox, which reads smtp_host.
ALTER TABLE warmup_observations
    ADD COLUMN IF NOT EXISTS destination_esp TEXT NOT NULL DEFAULT 'unknown';

-- The vocabulary is esp.ESP's, unchanged and not re-invented: a fifth spelling of
-- "Google" would split one destination into two rows of the same matrix. It is
-- closed by a CHECK rather than by the writer alone because the read side GROUPs
-- BY this column, so an unrecognised value does not fail loudly — it silently
-- becomes a route of its own.
--
-- 'unknown' is the DEFAULT, and it is a first-class state distinct from 'other'
-- (esp's judgement 3): 'other' means "resolved, and it is neither Google nor
-- Microsoft", 'unknown' means "not resolved". Every pre-existing row therefore
-- states honestly that its destination was never recorded, rather than defaulting
-- into a route it was never measured on.
ALTER TABLE warmup_observations
    ADD CONSTRAINT warmup_observations_destination_esp_check
    CHECK (destination_esp IN ('google','microsoft','other','unknown'));
