-- Remaining FK-maintenance paths whose leading child column was not covered by
-- an index. Composite tenant FKs whose first column is a globally unique UUID
-- are already served by that UUID's index and intentionally are not duplicated.
CREATE INDEX idx_sequence_steps_workspace ON sequence_steps(workspace_id);
CREATE INDEX idx_tracking_events_send ON tracking_events(send_id);
CREATE INDEX idx_tracking_events_workspace ON tracking_events(workspace_id);
CREATE INDEX idx_mailbox_worker_assignments_workspace ON mailbox_worker_assignments(workspace_id);
CREATE INDEX idx_oauth_clients_created_by ON oauth_clients(created_by_user_id)
    WHERE created_by_user_id IS NOT NULL;
