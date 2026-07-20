ALTER TABLE sends DROP COLUMN attempts;

ALTER TABLE sends DROP CONSTRAINT sends_campaign_id_contact_id_created_at_key;
ALTER TABLE sends ADD CONSTRAINT sends_campaign_id_contact_id_key
    UNIQUE (campaign_id, contact_id);

ALTER TABLE sends DROP CONSTRAINT sends_pkey;
ALTER TABLE sends ADD PRIMARY KEY (id);
