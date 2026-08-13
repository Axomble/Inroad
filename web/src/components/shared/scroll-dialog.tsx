import { cn } from '@/lib/utils'
import { AlertDialogContent } from '@/components/ui/alert-dialog'

/**
 * Tall-form dialog scaffolding. A plain AlertDialogContent given
 * `max-h + overflow-y-auto` scrolls as a whole, which pushes the header and —
 * worse — the action buttons out of view on long forms. This pair pins both:
 *
 *   <ScrollDialogContent>
 *     <AlertDialogHeader>…</AlertDialogHeader>
 *     <ScrollDialogBody>…the fields…</ScrollDialogBody>
 *     <AlertDialogFooter>…</AlertDialogFooter>
 *   </ScrollDialogContent>
 *
 * Only the body scrolls; title and actions stay reachable however long the
 * form grows. Pass a width override (e.g. `sm:max-w-2xl`) via className when
 * the content earns it — the default stays AlertDialogContent's `max-w-md`.
 */
export function ScrollDialogContent({
  className,
  ...props
}: React.ComponentProps<typeof AlertDialogContent>) {
  return <AlertDialogContent className={cn('flex max-h-[85vh] flex-col', className)} {...props} />
}

/**
 * The scrollable middle of a ScrollDialogContent. The negative-margin/padding
 * pair keeps the scrollbar off the fields without shifting them relative to
 * the pinned header and footer.
 */
export function ScrollDialogBody({ className, ...props }: React.HTMLAttributes<HTMLDivElement>) {
  return (
    <div
      data-slot="scroll-dialog-body"
      className={cn('-mr-2 flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto pr-2', className)}
      {...props}
    />
  )
}
