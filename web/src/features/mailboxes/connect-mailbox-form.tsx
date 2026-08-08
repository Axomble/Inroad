import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Flame, Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { httpStatus, isEmailNotVerified } from '@/lib/rtk-error'
// The email-verification gate is the auth feature's own concern: its button
// wrapper for the submit, and the raw flag for the Enter-key guard below.
import { emailVerificationHint, useEmailVerified } from '@/features/auth/use-email-verified'
import { VerifiedGateButton } from '@/features/auth/verified-gate-button'
// Cross-feature hook reuse, the established exception (see mailboxes-page.tsx
// pulling the warmup overview) — extended here to warmup's enable MUTATION,
// because warming a mailbox is part of connecting one, and the alternative is
// duplicating the endpoint. Warmup still owns its own screens, settings form and
// cache tags; this only fires the default-settings enable.
import { useEnableMailboxWarmupMutation } from '@/features/warmup/api'
import { useConnectMailboxMutation } from './api'

const PORT_ERROR = 'Port must be between 1 and 65535'

/** Names the gated action once, for the control and the 403's copy alike. */
const GATED_ACTION = 'connect a mailbox'

const schema = z.object({
  email: z.email('Enter a valid email'),
  display_name: z.string().optional(),
  smtp_host: z.string().min(1, 'Required'),
  smtp_port: z.number({ message: PORT_ERROR }).int().min(1, PORT_ERROR).max(65535, PORT_ERROR),
  smtp_username: z.string().optional(),
  imap_host: z.string().min(1, 'Required'),
  imap_port: z.number({ message: PORT_ERROR }).int().min(1, PORT_ERROR).max(65535, PORT_ERROR),
  imap_username: z.string().optional(),
  secret: z.string().min(1, 'Required'),
  allow_plaintext: z.boolean(),
  // Client-only: warmup is enabled by a separate PUT once the mailbox exists,
  // so this is stripped from the connect body (see onSubmit).
  start_warmup: z.boolean(),
})
type Values = z.infer<typeof schema>

function connectErrorMessage(error: unknown): string {
  // Before the status ladder: POST /mailboxes sits behind
  // `auth.RequireVerified`, and this 403 has an answer the operator can act on,
  // which "couldn't connect the mailbox" hid.
  if (isEmailNotVerified(error)) return emailVerificationHint(GATED_ACTION)
  const status = httpStatus(error)
  if (status === 409) return 'A mailbox with this email is already connected.'
  if (status === 422) return 'Connection test failed — check host, port, and credentials.'
  if (status === 400) return 'Please fill in all required fields.'
  return "Couldn't connect the mailbox. Please try again."
}

/**
 * Reports how a connect finished. `warmupFailed` means the mailbox IS connected
 * but the follow-up warmup enable didn't land — the caller owns that copy,
 * because this form has already closed by the time it needs saying.
 */
export type ConnectOutcome = { warmupFailed: boolean }

export function ConnectMailboxForm({
  onDone,
  onCancel,
}: {
  onDone: (outcome: ConnectOutcome) => void
  onCancel: () => void
}) {
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<Values>({
    resolver: zodResolver(schema),
    // Warming is on by default: for cold email it's the default-correct action
    // for a new mailbox, not an extra. Opting out stays one click.
    defaultValues: { smtp_port: 587, imap_port: 993, allow_plaintext: false, start_warmup: true },
  })
  const [connect, { isLoading, error }] = useConnectMailboxMutation()
  const [enableWarmup] = useEnableMailboxWarmupMutation()
  const { verified } = useEmailVerified()
  const plaintextId = useId()
  const warmupId = useId()

  async function onSubmit(values: Values) {
    // The submit button is gated, but a form also submits on Enter from inside
    // a field, which never reaches the button's own click guard.
    if (!verified) return
    // `start_warmup` is a form field, not part of the connect contract.
    const { start_warmup, ...connectMailboxRequest } = values
    const result = await connect({ connectMailboxRequest })
    if (!('data' in result) || !result.data) return

    const id = result.data.id
    if (!start_warmup || !id) {
      onDone({ warmupFailed: false })
      return
    }
    // Empty settings = the server's defaults (every WarmupSettings field is
    // optional); the generated arg type requires the key, not its contents.
    // A failure here must not read as a failed connect — the mailbox exists.
    const warmup = await enableWarmup({ id, warmupSettings: {} })
    onDone({ warmupFailed: 'error' in warmup })
  }

  return (
    <div className="border-b border-border bg-surface/40">
      <div className="flex h-10 items-center border-b border-border px-5">
        <span className="font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">Connect a mailbox</span>
      </div>

      {/* Capped width: at full bleed these inputs stretch past 1100px, which
          reads as a settings dump rather than a form. ~48rem keeps label,
          value, and helper in one eye span. */}
      <form onSubmit={handleSubmit(onSubmit)} noValidate className="grid max-w-3xl gap-5 p-5">
        <div className="grid gap-4 md:grid-cols-2">
          <Field label="Email" error={errors.email?.message}>
            {(id) => (
              <Input
                id={id}
                type="email"
                autoComplete="off"
                placeholder="sender@company.com"
                aria-invalid={!!errors.email}
                {...register('email')}
              />
            )}
          </Field>
          <Field label="Display name" hint="optional">
            {(id) => <Input id={id} placeholder="Sales — Company" {...register('display_name')} />}
          </Field>
        </div>

        <FieldGroup label="Outgoing mail · SMTP">
          <div className="grid gap-4 md:grid-cols-3">
          <Field label="SMTP host" error={errors.smtp_host?.message} className="md:col-span-2">
            {(id) => (
              <Input
                id={id}
                placeholder="smtp.company.com"
                aria-invalid={!!errors.smtp_host}
                {...register('smtp_host')}
              />
            )}
          </Field>
          <Field label="SMTP port" error={errors.smtp_port?.message}>
            {(id) => (
              <Input
                id={id}
                type="number"
                inputMode="numeric"
                aria-invalid={!!errors.smtp_port}
                {...register('smtp_port', { valueAsNumber: true })}
              />
            )}
          </Field>
          <Field label="SMTP username" hint="defaults to email" className="md:col-span-3">
            {(id) => (
              <Input id={id} placeholder="sender@company.com" {...register('smtp_username')} />
            )}
          </Field>
          </div>
        </FieldGroup>

        <FieldGroup label="Incoming mail · IMAP">
          <div className="grid gap-4 md:grid-cols-3">
          <Field label="IMAP host" error={errors.imap_host?.message} className="md:col-span-2">
            {(id) => (
              <Input
                id={id}
                placeholder="imap.company.com"
                aria-invalid={!!errors.imap_host}
                {...register('imap_host')}
              />
            )}
          </Field>
          <Field label="IMAP port" error={errors.imap_port?.message}>
            {(id) => (
              <Input
                id={id}
                type="number"
                inputMode="numeric"
                aria-invalid={!!errors.imap_port}
                {...register('imap_port', { valueAsNumber: true })}
              />
            )}
          </Field>
          <Field label="IMAP username" hint="defaults to email" className="md:col-span-3">
            {(id) => (
              <Input id={id} placeholder="sender@company.com" {...register('imap_username')} />
            )}
          </Field>
          </div>
        </FieldGroup>

        <FieldGroup label="Credentials">
          <Field label="Password / app password" error={errors.secret?.message}>
            {(id) => (
              <Input
                id={id}
                type="password"
                autoComplete="off"
                placeholder="••••••••"
                aria-invalid={!!errors.secret}
                {...register('secret')}
              />
            )}
          </Field>

          <div>
            <label htmlFor={plaintextId} className="flex items-center gap-2 text-[13px] text-muted-foreground">
              <input
                id={plaintextId}
                type="checkbox"
                className="size-4 accent-primary"
                {...register('allow_plaintext')}
              />
              Allow plaintext (no TLS)
            </label>
            <p className="mt-1 pl-6 text-xs text-faint">
              TLS is used by default. Only check this for a local or self-hosted relay with no TLS — credentials will
              be sent without encryption.
            </p>
          </div>
        </FieldGroup>

        <FieldGroup label="Deliverability">
          <div>
            <label htmlFor={warmupId} className="flex items-center gap-2 text-[13px] text-foreground">
              <input id={warmupId} type="checkbox" className="size-4 accent-warm" {...register('start_warmup')} />
              <Flame className="size-3.5 text-warm" aria-hidden="true" />
              Start warming this mailbox
            </label>
            <p className="mt-1 pl-6 text-xs text-faint">
              Warmup exchanges a small, ramping volume of real mail between your own connected mailboxes, so this
              address builds a sending reputation before it touches a campaign. Recommended for any new mailbox; you
              can turn it off per mailbox later.
            </p>
          </div>
        </FieldGroup>

        {error && (
          <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
            {connectErrorMessage(error)}
          </p>
        )}

        <div className="flex items-center justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
            Cancel
          </Button>
          <VerifiedGateButton
            action={GATED_ACTION}
            type="submit"
            variant="primary"
            size="sm"
            disabled={isLoading}
          >
            {isLoading && <Loader2 className="animate-spin" />}
            {isLoading ? 'Testing connection…' : 'Connect mailbox'}
          </VerifiedGateButton>
        </div>
      </form>
    </div>
  )
}

/**
 * A named group of related fields. `fieldset`/`legend` rather than a styled
 * div so the grouping is announced to screen readers, not just drawn — the
 * SMTP and IMAP blocks repeat the same field names (host, port, username) and
 * the group name is what disambiguates them.
 */
function FieldGroup({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <fieldset className="min-w-0 rounded-lg border border-border p-4">
      <legend className="px-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">{label}</legend>
      <div className="grid gap-4">{children}</div>
    </fieldset>
  )
}

/**
 * Field wraps a labelled control. Uses a render-prop so the caller receives
 * the generated id and passes it to the control directly — no `cloneElement`
 * indirection, no unclear type overrides, and refs / event handlers flow
 * through the way you'd expect.
 */
function Field({
  label,
  hint,
  error,
  className,
  children,
}: {
  label: string
  hint?: string
  error?: string
  className?: string
  children: (id: string) => React.ReactNode
}) {
  const id = useId()
  return (
    <div className={className}>
      <div className="mb-1.5 flex items-center gap-2">
        <Label htmlFor={id}>{label}</Label>
        {hint && <span className="font-mono text-[10px] uppercase tracking-[0.12em] text-faint">{hint}</span>}
      </div>
      <div>{children(id)}</div>
      {error && (
        <span role="alert" className="mt-1 block text-xs text-danger">
          {error}
        </span>
      )}
    </div>
  )
}
