-- Dropping each table drops its indexes with it. webhook_deliveries first: it
-- has a FK to webhook_endpoints.
DROP TABLE IF EXISTS webhook_deliveries;
DROP TABLE IF EXISTS webhook_endpoints;
