import { Moon, Sun } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { resolveDark } from '@/lib/theme'
import { useSystemDark } from '@/hooks/use-system-dark'
import { useAppDispatch, useAppSelector } from '@/store/hooks'
import { setTheme } from '@/store/slices/ui'

/**
 * Light/dark switch. The choice is dispatched to the persisted `ui` slice
 * rather than written to the DOM here — `startThemeSync` (wired in main.tsx)
 * owns the `<html class="dark">` side effect, so the preference survives a
 * reload and there is exactly one writer of the class.
 */
export function ThemeToggle() {
  const preference = useAppSelector((s) => s.ui.theme)
  const dispatch = useAppDispatch()
  const dark = resolveDark(preference, useSystemDark())

  const nextLabel = dark ? 'Use light theme' : 'Use dark theme'
  return (
    <Button
      variant="ghost"
      size="icon-sm"
      onClick={() => dispatch(setTheme(dark ? 'light' : 'dark'))}
      aria-label={nextLabel}
      title={nextLabel}
      className="text-chrome-muted hover:bg-chrome-hover hover:text-chrome-text"
    >
      {dark ? <Sun /> : <Moon />}
    </Button>
  )
}
