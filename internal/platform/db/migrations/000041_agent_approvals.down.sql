DROP TABLE IF EXISTS pending_action_audit;
DROP TABLE IF EXISTS pending_actions;
ALTER TABLE agent_message_parts DROP CONSTRAINT IF EXISTS uq_agent_message_parts_id_workspace;
