-- The index is owned by the table and goes with it; dropping the table drops it.
-- Nothing else references recipient_domains, so a rollback loses only cached DNS
-- answers, which the sweep rebuilds.
DROP TABLE IF EXISTS recipient_domains;
