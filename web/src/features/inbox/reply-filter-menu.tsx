import { ListFilter, Check } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

export interface ReplyFilterOption {
  id: string
  label: string
}

/**
 * The list header's "Filter" control, as a mail client has one — here it
 * narrows by reply class. Its own control rather than the shared SortMenu
 * because this doesn't order anything: calling a filter "Sort by" would teach
 * the operator the wrong model of what the menu does.
 *
 * The trigger names the ACTIVE filter, not the word "Filter", once one is
 * applied — the control's first job is saying what the list is currently
 * showing.
 */
export function ReplyFilterMenu({
  options,
  value,
  onChange,
}: {
  options: readonly ReplyFilterOption[]
  value: string
  onChange: (id: string) => void
}) {
  const active = options.find((o) => o.id === value)
  const filtered = value !== ''

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="xs"
          aria-label={`Filter by reply class: ${active?.label ?? 'All replies'}`}
          className={filtered ? 'text-accent-ink' : undefined}
        >
          <ListFilter className="size-3.5" />
          {filtered ? active?.label : 'Filter'}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>Reply class</DropdownMenuLabel>
        {options.map((option) => (
          <DropdownMenuItem key={option.id} onSelect={() => onChange(option.id)}>
            <Check
              className={option.id === value ? 'size-4 opacity-100' : 'size-4 opacity-0'}
              aria-hidden="true"
            />
            {option.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
