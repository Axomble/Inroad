-- Reply conditions on branched sequences (20260923110214_sequence_step_branches)
-- look for an inbound message on an enrollment's campaign and contact. Every
-- earlier inbox_threads index leads with workspace_id and a mailbox or a sort key,
-- so that lookup would otherwise walk every thread in the workspace, once per
-- waiting enrollment per re-check.
--
-- CONCURRENTLY, so the build takes no lock that blocks inbox writes on a live
-- table. That is only possible because this file is ONE statement: the pgx/v5
-- golang-migrate driver sends a file as a single simple-protocol query, which
-- runs a lone statement outside any transaction block but wraps several in an
-- implicit one — where CREATE INDEX CONCURRENTLY is refused (see
-- 20260827185855_inbox_messages_mailbox_id). Do not add a second statement here.
--
-- A concurrent build that fails leaves an INVALID index behind rather than
-- rolling back; IF NOT EXISTS would then skip it on a re-run. The integration
-- test TestInboxThreadsCampaignContactIndexIsValid asserts pg_index.indisvalid.
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_inbox_threads_campaign_contact
    ON inbox_threads (campaign_id, contact_id) WHERE campaign_id IS NOT NULL;
