-- The company page's contact roster (GET /crm/companies/{id}/contacts) had no
-- index to seek with. `contacts.company_id` was added in 000042 with a tenant FK
-- and no index behind it, so the listing fell back to the workspace-wide
-- idx_contacts_search bitmap: on a 25,000-contact workspace, EXPLAIN ANALYZE
-- showed 25,000 rows scanned and 24,950 discarded by the company filter, 576
-- buffer reads and 11.3 ms, to return the first 50 rows. The cost was set by the
-- size of the WORKSPACE rather than the size of the company, which is what makes
-- it a scan and not a lookup.
--
-- Column order matches the query exactly: the workspace pin first (invariant 4),
-- then the company, then the (lower(email), id) pair the ORDER BY and the keyset
-- comparison both use — so the seek, the sort and the pagination are all served
-- by this one index and a deep page costs the same as the first.
--
-- Partial on company_id IS NOT NULL because a contact with no company is never
-- a row of this listing; that keeps the index off every contact in a workspace
-- that does not use companies. It incidentally also backs the
-- contacts_company_tenant_fkey ON DELETE RESTRICT check, which equality-matches
-- both leading columns.
CREATE INDEX idx_contacts_ws_company_email
    ON contacts (workspace_id, company_id, lower(email), id)
    WHERE company_id IS NOT NULL;
