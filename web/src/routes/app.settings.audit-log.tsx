import { createFileRoute } from '@tanstack/react-router'
import { AuditLogPage } from '@/features/audit-log/audit-log-page'
import { parseAuditLogSearch, type AuditLogSearch } from '@/features/audit-log/audit-log-search'

/**
 * The audit log's filters live in the URL (`?action=`, `?actor=`, `?from=`,
 * `?to=`) so an investigation is linkable. `validateSearch` is authoritative — a
 * param it does not return is stripped on the next navigation — so the contract
 * is defined once, in `features/audit-log/audit-log-search.ts`, and applied here.
 */
export const Route = createFileRoute('/app/settings/audit-log')({
  validateSearch: (search: Record<string, unknown>): AuditLogSearch => parseAuditLogSearch(search),
  component: AuditLogPage,
})
