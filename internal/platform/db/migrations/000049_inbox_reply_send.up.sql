-- Supports CountSentToday's extension (queries/send.sql): a manual reply sent
-- from the unified inbox is never a `sends` row (see
-- internal/app/inbox.Service.Reply's doc for why), so the daily-cap gate that
-- query backs must also see the day's outbound inbox_messages for the
-- mailbox. Those are found by mailbox_id through inbox_threads, which had no
-- index on that column alone (only the (workspace_id, mailbox_id, ...)
-- composite) — CountSentToday is keyed on mailbox_id alone, with no
-- workspace_id available at that call site (see its own doc comment).
CREATE INDEX idx_inbox_threads_mailbox ON inbox_threads (mailbox_id);
