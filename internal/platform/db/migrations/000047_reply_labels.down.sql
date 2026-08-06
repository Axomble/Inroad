-- Reverse of 000047, in reverse order. Every object created up there is dropped
-- here and every constraint/index dropped up there is restored to its EXACT
-- prior definition, so up→down→up leaves nothing behind and nothing changed.

-- Restore the 000014 reply_class CHECK verbatim. This validates existing rows:
-- a workspace that classified replies with a CUSTOM label key while 000047 was
-- applied will fail this rollback loudly rather than silently dropping the
-- constraint's guarantee. That is the correct failure — clear the offending
-- reply_class values first if you really need to roll back.
ALTER TABLE sequence_enrollments
    ADD CONSTRAINT sequence_enrollments_reply_class_chk
    CHECK (reply_class IS NULL OR reply_class IN
        ('positive','negative','neutral','auto_reply','out_of_office','unsubscribe','unknown'));

-- Restore the 000046 unread index pair.
DROP INDEX idx_inbox_threads_workspace_unread_any;
CREATE INDEX idx_inbox_threads_workspace_unread_positive
    ON inbox_threads (workspace_id) WHERE unread AND last_reply_class = 'positive';

DROP TRIGGER workspaces_seed_reply_labels ON workspaces;
DROP FUNCTION seed_new_workspace_reply_labels();
DROP FUNCTION seed_reply_labels(UUID);
DROP TRIGGER reply_labels_touch_updated_at ON reply_labels;
DROP TABLE reply_labels;
