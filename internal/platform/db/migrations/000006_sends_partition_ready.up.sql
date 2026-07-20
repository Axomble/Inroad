-- Partition-readiness on `sends`. A future range-partition by created_at
-- requires the partition key to be part of every unique constraint —
-- promoting (id, created_at) as PK and rebuilding the campaign/contact
-- uniqueness constraint to include created_at is the smallest change that
-- makes the table partitionable without touching call sites (id still
-- uniquely identifies a row in practice; created_at DEFAULT now() means
-- inserts don't have to know about it).
--
-- Pre-production, no data to migrate: the rebuild is a straight
-- constraint swap. If this ever runs against a populated table the
-- rebuild is still safe because both new constraints are supersets of
-- the old ones for existing rows.

ALTER TABLE sends DROP CONSTRAINT sends_pkey;
ALTER TABLE sends ADD PRIMARY KEY (id, created_at);

ALTER TABLE sends DROP CONSTRAINT sends_campaign_id_contact_id_key;
ALTER TABLE sends ADD CONSTRAINT sends_campaign_id_contact_id_created_at_key
    UNIQUE (campaign_id, contact_id, created_at);

-- attempts counter used by the worker to cap runaway cap-exceeded re-enqueues.
ALTER TABLE sends ADD COLUMN attempts INT NOT NULL DEFAULT 0;
