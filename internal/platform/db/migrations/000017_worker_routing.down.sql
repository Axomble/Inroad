-- Reverse 000018. Dropping the tables drops mailbox_worker_assignments_worker
-- with mailbox_worker_assignments. Order: the assignments table first (it has no
-- dependents), then the workers registry. up/down/up is clean.
DROP TABLE mailbox_worker_assignments;
DROP TABLE workers;
