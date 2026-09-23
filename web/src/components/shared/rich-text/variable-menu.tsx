import { cn } from '@/lib/utils'
import { tokenText } from './merge-tags'
import { optionId, type SuggestionMenuState } from './variable-suggestion'

/**
 * The `{{` suggestion list. Focus never leaves the editor — it points here via
 * aria-activedescendant — so options take clicks on mousedown-prevented
 * buttons rather than focus.
 */
export function VariableMenu({ id, menu }: { id: string; menu: SuggestionMenuState }) {
  return (
    <div
      id={id}
      role="listbox"
      aria-label="Merge fields"
      className="absolute z-50 w-64 overflow-hidden rounded-md border border-border bg-popover p-1 text-popover-foreground shadow-md"
      style={{ top: menu.position.top, left: menu.position.left }}
    >
      {menu.items.map((item, index) => (
        <div
          key={item.name}
          id={optionId(id, index)}
          role="option"
          aria-selected={index === menu.activeIndex}
          className={cn(
            'flex cursor-pointer items-center justify-between gap-3 rounded-sm px-2 py-1.5 text-sm',
            index === menu.activeIndex ? 'bg-accent text-accent-foreground' : 'hover:bg-accent/60',
          )}
          onMouseDown={(event) => event.preventDefault()}
          onClick={() => menu.select(item)}
        >
          <span className="truncate">{item.label}</span>
          <span className="shrink-0 font-mono text-[11px] text-muted-foreground">{tokenText(item.name)}</span>
        </div>
      ))}
    </div>
  )
}
