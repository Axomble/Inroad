import { useId, useRef } from 'react'
import { useForm, useWatch } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useNavigate } from '@tanstack/react-router'
import { AlertCircle, ArrowRight, Building2, Loader2 } from 'lucide-react'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { serverDetail } from '@/lib/rtk-error'
import { useAppDispatch } from '@/store/hooks'
import { clearSession, renameActiveWorkspace } from '@/store/slices/auth'
import { useAuthLogoutMutation, useCompleteWorkspaceOnboardingMutation } from './api'

// Matches the server's rule for this field (1–200 characters after trimming), so
// the client rejects what the server would reject and nothing else.
const schema = z.object({
  name: z
    .string()
    .trim()
    .min(1, 'Give your workspace a name')
    .max(200, 'Keep the name under 200 characters'),
})
type FormValues = z.infer<typeof schema>

/**
 * The first-run panel itself. Split from its gate
 * (`workspace-onboarding-overlay.tsx`) for two reasons: the form's state is then
 * created once, when the dialog actually opens, so a later `/auth/me` refetch
 * can't re-seed the pre-filled default while the user is mid-edit — and this
 * module (react-hook-form + zod + Radix's dialog) stays off the app shell's eager
 * path, since it renders once in a workspace's lifetime. Default-exported so the
 * gate can `React.lazy` it.
 */
export default function OnboardingDialog({
  workspaceId,
  derivedName,
  email,
}: {
  workspaceId: string
  derivedName: string
  email: string
}) {
  const nameId = useId()
  const nameRef = useRef<HTMLInputElement | null>(null)
  const dispatch = useAppDispatch()
  const navigate = useNavigate()
  const [complete, { isLoading, error }] = useCompleteWorkspaceOnboardingMutation()
  const [logout] = useAuthLogoutMutation()

  const {
    register,
    control,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema), defaultValues: { name: derivedName } })
  const { ref: registerRef, ...nameField } = register('name')

  // Subscribes only this value, so the identity tile tracks typing without
  // re-rendering the whole dialog on every keystroke of some other field.
  const typedName = useWatch({ control, name: 'name' }) ?? ''

  async function onSubmit(values: FormValues) {
    const result = await complete({
      id: workspaceId,
      completeOnboardingRequest: { name: values.name.trim() },
    })
    if ('data' in result && result.data) {
      // The header's switcher reads workspace names off the session, which still
      // holds the name derived at signup. Use the name the SERVER returned rather
      // than the one submitted: the endpoint is idempotent, so a workspace that
      // was already named answers with its STORED name and does not rename.
      dispatch(renameActiveWorkspace({ name: result.data.name }))
    }
    // A failure leaves the dialog open; `error` below explains it. The overlay
    // itself is dismissed by the refetched flag, not by anything here.
  }

  // Signing in with the wrong Google account must not be a trap. The overlay has
  // no close affordance by design, so this is the one way out — and it has to
  // work even if the server call fails, exactly like the header's account menu.
  async function onSignOut() {
    try {
      await logout().unwrap()
    } catch {
      // ignore — the local session is cleared either way
    }
    dispatch(clearSession())
    void navigate({ to: '/' })
  }

  const serverMessage = error
    ? (serverDetail(error) ?? "Couldn't save that name. Please try again.")
    : null

  return (
    <AlertDialog open>
      <AlertDialogContent
        // `dialog`, not Radix's default `alertdialog`: this is a task to complete,
        // not an urgent interruption to acknowledge, and it owns a text field.
        role="dialog"
        aria-modal="true"
        // No Escape, no backdrop click, no close button — the workspace has to be
        // named. Radix already blocks outside interaction for an alert dialog;
        // Escape is the one that still closes, so it's cancelled explicitly.
        onEscapeKeyDown={(event) => event.preventDefault()}
        // Radix would otherwise focus its Cancel button, which this dialog
        // deliberately doesn't have — leaving focus on <body>. Put it on the one
        // field instead, so the whole step is a type-and-Enter.
        onOpenAutoFocus={() => nameRef.current?.focus()}
        className="w-full max-w-xl gap-0 rounded-xl border-border bg-surface p-8 shadow-2xl sm:p-10"
      >
        <div className="flex items-center gap-2">
          <div className="grid size-7 place-items-center rounded-lg bg-primary text-[13px] font-bold text-primary-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.25),0_2px_0_var(--primary-edge)]">
            I
          </div>
          <span className="text-sm font-bold tracking-tight text-foreground">Inroad</span>
        </div>

        <p className="mt-8 font-mono text-[11px] uppercase tracking-[0.16em] text-faint">First run</p>
        <AlertDialogTitle className="mt-2 text-2xl font-semibold tracking-tight text-foreground">
          Name your workspace
        </AlertDialogTitle>
        <AlertDialogDescription className="mt-2 text-sm leading-relaxed text-muted-foreground">
          This is how your workspace appears to everyone you work with — in the switcher, on invites, and
          across reports.
        </AlertDialogDescription>

        <form onSubmit={handleSubmit(onSubmit)} noValidate className="mt-8 flex flex-col gap-2">
          <Label htmlFor={nameId}>Workspace name</Label>
          <div className="relative">
            {/* A live preview of the workspace's mark, exactly as the header will
                render it. Decorative for a screen reader: it says nothing the
                field's own value doesn't already say. */}
            <span
              aria-hidden="true"
              className="pointer-events-none absolute left-2 top-1/2 grid size-8 -translate-y-1/2 place-items-center rounded-md bg-primary text-sm font-bold text-primary-foreground"
            >
              {typedName.trim() ? (
                typedName.trim().charAt(0).toUpperCase()
              ) : (
                <Building2 className="size-4" />
              )}
            </span>
            <Input
              id={nameId}
              autoComplete="organization"
              placeholder="Acme Outbound"
              className="h-12 rounded-lg pl-12 text-base"
              aria-invalid={!!errors.name}
              ref={(element) => {
                nameRef.current = element
                registerRef(element)
              }}
              {...nameField}
            />
          </div>
          {errors.name && (
            <span role="alert" className="text-xs text-danger">
              {errors.name.message}
            </span>
          )}

          {serverMessage && (
            <p
              role="alert"
              className="mt-1 flex items-start gap-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger"
            >
              <AlertCircle className="mt-px size-3.5 shrink-0" aria-hidden="true" />
              <span className="min-w-0 flex-1 leading-relaxed">{serverMessage}</span>
            </p>
          )}

          <Button type="submit" variant="primary" size="lg" className="mt-5 w-full" disabled={isLoading}>
            {isLoading ? <Loader2 className="animate-spin" /> : null}
            {isLoading ? 'Saving…' : 'Continue'}
            {!isLoading && <ArrowRight className="size-4" aria-hidden="true" />}
          </Button>

          <p className="mt-3 text-center text-[11px] leading-relaxed text-faint">
            That's the only thing we need. Invite your team and connect mailboxes whenever you're ready —
            both live in Settings.
          </p>
        </form>

        <div className="mt-8 flex items-center justify-between gap-3 border-t border-border pt-5">
          <p className="min-w-0 truncate text-xs text-muted-foreground">
            Signed in as <span className="font-medium text-foreground">{email}</span>
          </p>
          {/* Always reachable: the wrong-account case needs an exit, and a modal
              with no way out is a support ticket, not a conversion. */}
          <Button type="button" variant="ghost" size="sm" className="shrink-0" onClick={() => void onSignOut()}>
            Sign out
          </Button>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  )
}
