import type { ReactNode } from 'react'

/**
 * Stops a click anywhere inside `children` — including portalled dropdown/
 * dialog content, which React still bubbles through this component's tree —
 * from reaching an ancestor's own `onClick`. Every row in this feature's
 * lists navigates on click; every trigger a row also renders (the overflow
 * menu, its confirm dialogs, the launch preflight dialog) needs this guard so
 * confirming an action inside a portal doesn't also fire the row's own
 * navigation.
 *
 * A third literal copy of this wrapper (`campaigns-page.tsx`'s Launch
 * button + `PreflightDialog`, joining `LifecycleMenu`'s own two) is what
 * crossed the "don't abstract on the second occurrence" line — extracted
 * here rather than duplicated again.
 */
export function StopClickBubble({ children }: { children: ReactNode }) {
  return (
    <div className="relative inline-flex" onClick={(e) => e.stopPropagation()}>
      {children}
    </div>
  )
}
