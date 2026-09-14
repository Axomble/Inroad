-- F3: worker identity + liveness. A worker's id can now come from three
-- sources (an explicit INROAD_WORKER_ID pin, the host's public IP, or the OS
-- hostname when no public IP is found) — see internal/platform/workerid.
-- id_family records WHICH one produced worker_id on the most recent
-- heartbeat, so an operator inspecting `workers` can tell a NAT'd or
-- hostname-derived worker from an IP-derived one without cross-referencing
-- application logs.
--
-- NOT NULL with a DEFAULT rather than nullable: every row is written by
-- UpsertWorker, which always has a family to report, so NULL would only ever
-- mean "written before this column existed" — a fact the default expresses
-- more usefully than NULL would (a pre-existing row reads as the value that
-- WAS true for every worker before this migration: hostname-derived).
ALTER TABLE workers
    ADD COLUMN id_family TEXT NOT NULL DEFAULT 'hostname'
        CHECK (id_family IN ('ipv4', 'ipv6', 'hostname', 'override'));
