import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useConnectMailboxMutation } from './api'

const PORT_ERROR = 'Port must be between 1 and 65535'

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
  use_tls: z.boolean(),
})
type Values = z.infer<typeof schema>

function connectErrorMessage(error: unknown): string {
  const status = (error as { status?: number | string })?.status
  if (status === 409) return 'A mailbox with this email is already connected.'
  if (status === 422) return 'Connection test failed — check host, port, and credentials.'
  if (status === 400) return 'Please fill in all required fields.'
  return "Couldn't connect the mailbox. Please try again."
}

export function ConnectMailboxForm({ onDone, onCancel }: { onDone: () => void; onCancel: () => void }) {
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<Values>({
    resolver: zodResolver(schema),
    defaultValues: { smtp_port: 587, imap_port: 993, use_tls: true },
  })
  const [connect, { isLoading, error }] = useConnectMailboxMutation()
  const tlsId = useId()

  async function onSubmit(values: Values) {
    const result = await connect({ connectMailboxRequest: values })
    if ('data' in result && result.data) onDone()
  }

  return (
    <div className="border-b border-border bg-surface/40">
      <div className="flex h-10 items-center border-b border-border px-5">
        <span className="font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">Connect a mailbox</span>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} noValidate className="grid gap-4 p-5">
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

        <label htmlFor={tlsId} className="flex items-center gap-2 text-[13px] text-muted-foreground">
          <input id={tlsId} type="checkbox" className="size-4 accent-primary" {...register('use_tls')} />
          Require TLS (recommended)
        </label>

        {error && (
          <p role="alert" className="rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
            {connectErrorMessage(error)}
          </p>
        )}

        <div className="flex items-center justify-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" size="sm" disabled={isLoading}>
            {isLoading && <Loader2 className="animate-spin" />}
            {isLoading ? 'Testing connection…' : 'Connect mailbox'}
          </Button>
        </div>
      </form>
    </div>
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
