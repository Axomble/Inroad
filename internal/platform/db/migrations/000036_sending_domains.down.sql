-- The table is a cache of DNS answers derived from mailboxes.email; dropping it
-- loses no domain the operator entered, only when we last looked each one up.
DROP TABLE IF EXISTS sending_domains;
