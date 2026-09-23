import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { CLEAR_FILTERS, hasFilters, type AuditLogSearch, type AuditSearchPatch } from './audit-log-search'
import { ACTION_GROUPS, ACTOR_TYPES } from './audit-vocabulary'

/**
 * The filter bar. Every control writes straight to the URL through `onChange`;
 * nothing is held locally, so what the bar shows and what was requested cannot
 * disagree.
 *
 * The action filter lists each category above its own actions because the API
 * matches a category as a prefix: "Campaigns" is one request for every campaign
 * event, not a client-side union of three.
 */
export function AuditLogFilters({
  search,
  onChange,
}: {
  search: AuditLogSearch
  onChange: (patch: AuditSearchPatch) => void
}) {
  return (
    <div
      role="group"
      aria-label="Filter the audit log"
      className="flex flex-wrap items-end gap-3 border-b border-border px-4 py-3 sm:px-5"
    >
      <Field id="audit-filter-action" label="Action">
        <Select
          id="audit-filter-action"
          wrapperClassName="w-56"
          value={search.action ?? ''}
          onChange={(e) => onChange({ action: e.target.value })}
        >
          <option value="">All actions</option>
          {ACTION_GROUPS.map((group) => (
            <optgroup key={group.category} label={group.label}>
              <option value={group.category}>{group.label}: all events</option>
              {group.actions.map(({ action, label }) => (
                <option key={action} value={action}>
                  {label}
                </option>
              ))}
            </optgroup>
          ))}
        </Select>
      </Field>

      <Field id="audit-filter-actor" label="Actor">
        <Select
          id="audit-filter-actor"
          wrapperClassName="w-44"
          value={search.actor ?? ''}
          onChange={(e) => onChange({ actor: e.target.value })}
        >
          <option value="">Anyone</option>
          {ACTOR_TYPES.map(({ actorType, label }) => (
            <option key={actorType} value={actorType}>
              {label}
            </option>
          ))}
        </Select>
      </Field>

      <Field id="audit-filter-from" label="From">
        <Input
          id="audit-filter-from"
          type="date"
          className="w-40"
          value={search.from ?? ''}
          max={search.to}
          onChange={(e) => onChange({ from: e.target.value })}
        />
      </Field>

      <Field id="audit-filter-to" label="To">
        <Input
          id="audit-filter-to"
          type="date"
          className="w-40"
          value={search.to ?? ''}
          min={search.from}
          onChange={(e) => onChange({ to: e.target.value })}
        />
      </Field>

      {hasFilters(search) && (
        <Button variant="ghost" size="sm" onClick={() => onChange(CLEAR_FILTERS)}>
          Clear filters
        </Button>
      )}
    </div>
  )
}

function Field({ id, label, children }: { id: string; label: string; children: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor={id} className="text-[11px] text-muted-foreground">
        {label}
      </Label>
      {children}
    </div>
  )
}
