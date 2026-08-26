-- Restore the single-column references, then drop the indexes they no longer need.
ALTER TABLE inbox_pending_composes
    DROP CONSTRAINT IF EXISTS inbox_pending_composes_mailbox_workspace_fkey,
    ADD CONSTRAINT inbox_pending_composes_mailbox_id_fkey
        FOREIGN KEY (mailbox_id) REFERENCES mailboxes (id) ON DELETE CASCADE;

ALTER TABLE inbox_pending_replies
    DROP CONSTRAINT IF EXISTS inbox_pending_replies_thread_workspace_fkey,
    ADD CONSTRAINT inbox_pending_replies_thread_id_fkey
        FOREIGN KEY (thread_id) REFERENCES inbox_threads (id) ON DELETE CASCADE;

ALTER TABLE inbox_thread_labels
    DROP CONSTRAINT IF EXISTS inbox_thread_labels_label_workspace_fkey,
    ADD CONSTRAINT inbox_thread_labels_label_id_fkey
        FOREIGN KEY (label_id) REFERENCES inbox_labels (id) ON DELETE CASCADE;

ALTER TABLE inbox_thread_labels
    DROP CONSTRAINT IF EXISTS inbox_thread_labels_thread_workspace_fkey,
    ADD CONSTRAINT inbox_thread_labels_thread_id_fkey
        FOREIGN KEY (thread_id) REFERENCES inbox_threads (id) ON DELETE CASCADE;

ALTER TABLE inbox_thread_snoozes
    DROP CONSTRAINT IF EXISTS inbox_thread_snoozes_thread_workspace_fkey,
    ADD CONSTRAINT inbox_thread_snoozes_thread_id_fkey
        FOREIGN KEY (thread_id) REFERENCES inbox_threads (id) ON DELETE CASCADE;

DROP INDEX IF EXISTS uq_mailboxes_id_workspace;
DROP INDEX IF EXISTS uq_inbox_labels_id_workspace;
DROP INDEX IF EXISTS uq_inbox_threads_id_workspace;
