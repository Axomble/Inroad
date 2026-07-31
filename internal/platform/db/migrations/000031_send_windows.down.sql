DROP TABLE IF EXISTS campaign_send_windows;

ALTER TABLE campaigns
    DROP COLUMN IF EXISTS timezone;

-- btree_gist is left installed: dropping an extension another migration or a
-- later feature may depend on is not this migration's business to undo.
