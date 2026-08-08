import { useId } from 'react'
import { Button, type ButtonProps } from '@/components/ui/button'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

/**
 * A Button that the app knows the server would reject right now, with the
 * reason attached. Drop-in for `<Button>` — pass `blocked` and the `reason`
 * sentence and every gated action in the app looks and announces the same way.
 *
 * Presentation only, and deliberately unaware of *why* it's blocked (email not
 * verified today; a plan limit or missing scope tomorrow), so it can live in
 * `components/shared` without a shared→feature dependency. The feature supplies
 * both props — for email verification, from `features/auth/use-email-verified`.
 *
 * Accessibility: blocked means `aria-disabled`, NOT `disabled`. A truly
 * disabled button can't be focused or hovered, so its tooltip is unreachable by
 * keyboard and its reason invisible to screen readers — the exact failure this
 * component exists to prevent. Instead the button stays focusable (so the
 * tooltip opens on focus as well as hover), swallows its own click, and is
 * `aria-describedby` a screen-reader-only copy of the reason, so the
 * explanation is programmatically associated rather than hover-only.
 */
export function GatedActionButton({
  blocked,
  reason,
  className,
  onClick,
  disabled,
  children,
  ...rest
}: ButtonProps & { blocked: boolean; reason: string }) {
  const hintId = useId()

  if (!blocked) {
    return (
      <Button className={className} onClick={onClick} disabled={disabled} {...rest}>
        {children}
      </Button>
    )
  }

  return (
    // Nested TooltipProvider: the app shell mounts one, but a provider here
    // keeps the component self-contained wherever it's rendered (dialogs,
    // tests). Radix allows nesting — metrics-panel.tsx does the same.
    <TooltipProvider>
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            aria-disabled
            aria-describedby={hintId}
            // A caller's own `disabled` (pending spinner) is dropped while
            // blocked: the gate outranks it, and `disabled` would take the
            // button out of the tab order along with its explanation.
            className={cn('cursor-not-allowed opacity-45', className)}
            // Not `disabled`, so the click has to be refused here. `preventDefault`
            // also stops a `type="submit"` button from submitting its form.
            onClick={(e) => e.preventDefault()}
            {...rest}
          >
            {children}
          </Button>
        </TooltipTrigger>
        <TooltipContent className="max-w-72">{reason}</TooltipContent>
      </Tooltip>
      <span id={hintId} className="sr-only">
        {reason}
      </span>
    </TooltipProvider>
  )
}
