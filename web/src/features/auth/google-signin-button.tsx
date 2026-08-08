import { Button } from '@/components/ui/button'
import { GoogleIcon } from '@/components/shared/google-icon'
import { googleSigninUrl } from './google-signin-url'

/**
 * The Google entry point on the login and register screens. It is a navigation,
 * not a mutation: clicking hands the top-level browser to the backend's start
 * route (see `google-signin-url.ts`), so there is no loading state to render and
 * no error to catch here — a failure comes back as a `?google_error=` param on the
 * login screen and is surfaced by `GoogleCallbackBanner`.
 *
 * Presented as the primary action by position and weight, not by colour: the lime
 * `primary` fill stays with "Log in", and Google's branding guidelines want their
 * mark on a neutral surface. `secondary` gives it the same tactile lip and
 * full-width footprint as the submit button one section below.
 */
export function GoogleSigninButton({ label, returnTo }: { label: string; returnTo?: string }) {
  return (
    <Button
      type="button"
      variant="secondary"
      size="lg"
      className="w-full"
      onClick={() => window.location.assign(googleSigninUrl(returnTo))}
    >
      <GoogleIcon className="size-[18px]" />
      {label}
    </Button>
  )
}
