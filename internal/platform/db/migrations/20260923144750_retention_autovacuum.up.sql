-- Autovacuum tuned for the tables the retention sweep deletes from in bulk.
--
-- The default autovacuum_vacuum_scale_factor is 0.2: a table is vacuumed once a
-- FIFTH of it is dead. On a 100M-row sends or tracking_events table that is 20M
-- dead tuples before anything is reclaimed, and the sweep's own age-index scans
-- walk those dead entries until it is. A flat threshold plus 1% keeps vacuum
-- following the sweep in steps rather than one enormous pass.
--
-- inbox_threads/inbox_messages are left at the defaults: they are orders of
-- magnitude smaller, and their deletes are whole conversations.
--
-- ALTER TABLE ... SET (storage parameters) takes SHARE UPDATE EXCLUSIVE, which
-- does not block reads or writes, and rewrites nothing. Several statements, so
-- this file runs as one implicit transaction — fine, since none of them waits
-- on anything but concurrent DDL. The settings are inert on a deployment that
-- never enables retention (few dead tuples, so nothing changes).
ALTER TABLE sends SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_vacuum_threshold = 50000);
ALTER TABLE tracking_events SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_vacuum_threshold = 50000);
ALTER TABLE deliverability_events SET (autovacuum_vacuum_scale_factor = 0.01, autovacuum_vacuum_threshold = 50000);
