import { ArrowUpDown, Check } from 'lucide-react'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

export interface SortMenuOption {
  id: string
  label: string
}

/**
 * Ordering control for a list toolbar. Built on the existing DropdownMenu rather
 * than a native `<select>` so it inherits the app's menu styling, keyboard model,
 * and focus behaviour, and so the active option can carry a check mark.
 *
 * The trigger shows the *current* ordering rather than the word "Sort": the
 * control's job is to tell you how the list is ordered right now, and only
 * secondarily to change it.
 */
export function SortMenu({
  options,
  value,
  onChange,
}: {
  options: readonly SortMenuOption[]
  value: string
  onChange: (id: string) => void
}) {
  const active = options.find((o) => o.id === value)

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="secondary" size="xs" aria-label={`Sort by ${active?.label ?? 'default'}`}>
          <ArrowUpDown className="size-3.5" />
          {active?.label ?? 'Sort'}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
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
