import { useId, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2, Plus, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Skeleton } from '@/components/ui/skeleton'
import { StatusPill } from '@/components/shared/status-pill'
import { Page, PageTopbar, PageBody, EmptyBlock } from '@/components/layout/page'
import { useAppSelector } from '@/store/hooks'
import type { Invite } from '@/store/api'
import { useCreateWorkspaceInviteMutation, useListWorkspaceInvitesQuery, useRevokeWorkspaceInviteMutation } from './api'

const field =
  'h-9 w-full rounded-md border border-border-strong bg-surface-2 px-3 text-[13px] text-foreground outline-none focus-visible:ring-2 focus-visible:ring-ring'

const schema = z.object({
  email: z.email('Enter a valid email address'),
  role: z.enum(['admin', 'member']),
})
type FormValues = z.infer<typeof schema>

/**
 * Team settings: pending workspace invites + an invite form, admin-gated by
 * the caller's role in the active workspace (mirrors `RequireRole(admin)`
 * on the backend — this is a UX guard, not the security boundary).
 */
export function InvitesPanel() {
  const workspaceId = useAppSelector((s) => s.auth.activeWorkspaceId)
  const role = useAppSelector((s) => s.auth.role)
  const isAdmin = role === 'owner' || role === 'admin'
  const [showInvite, setShowInvite] = useState(false)

  const { data, isLoading } = useListWorkspaceInvitesQuery({ id: workspaceId ?? '' }, { skip: !workspaceId || !isAdmin })
  const invites = data ?? []

  if (!isAdmin) {
    return (
      <Page>
        <PageTopbar eyebrow="Team" title="Invites" />
        <EmptyBlock
          title="Admins only"
          description="Ask a workspace owner or admin to invite teammates or manage pending invites."
        />
      </Page>
    )
  }

  return (
    <Page>
      <PageTopbar
        eyebrow="Team"
        title="Invites"
        actions={
          <Button variant="primary" size="sm" onClick={() => setShowInvite((v) => !v)}>
            <Plus className="size-4" />
            Invite teammate
          </Button>
        }
      />

      <PageBody>
        {showInvite && workspaceId && (
          <InviteForm workspaceId={workspaceId} onDone={() => setShowInvite(false)} onCancel={() => setShowInvite(false)} />
        )}

        {isLoading ? (
          <LoadingRows />
        ) : invites.length === 0 && !showInvite ? (
          <EmptyBlock
            title="No pending invites"
            description="Invite a teammate to give them access to this workspace."
            action={
              <Button variant="primary" size="sm" onClick={() => setShowInvite(true)}>
                <Plus className="size-4" />
                Invite teammate
              </Button>
            }
          />
        ) : (
          <ul>
            {invites.map((invite) => (
              <InviteRow key={invite.id ?? invite.email} invite={invite} workspaceId={workspaceId ?? ''} />
            ))}
          </ul>
        )}
      </PageBody>
    </Page>
  )
}

function InviteRow({ invite, workspaceId }: { invite: Invite; workspaceId: string }) {
  const [revoke, { isLoading }] = useRevokeWorkspaceInviteMutation()

  async function onRevoke() {
    if (!invite.id) return
    await revoke({ id: workspaceId, inviteId: invite.id })
  }

  return (
    <li className="flex items-center gap-4 border-b border-border px-5 py-3">
      <div className="min-w-0 flex-1">
        <span className="truncate text-[13.5px] font-medium text-foreground">{invite.email}</span>
        <div className="mt-0.5 font-mono text-[11px] text-faint">Invited as {invite.role ?? 'member'}</div>
      </div>

      {/* GET /workspaces/{id}/invites only ever returns pending invites (see
          ListPendingInvites) — no accepted/revoked branch to render here. */}
      <StatusPill tone="draft">pending</StatusPill>

      <Button
        variant="ghost"
        size="icon-sm"
        aria-label={`Revoke invite for ${invite.email}`}
        disabled={isLoading}
        onClick={() => void onRevoke()}
      >
        <X className="size-4" />
      </Button>
    </li>
  )
}

function InviteForm({
  workspaceId,
  onDone,
  onCancel,
}: {
  workspaceId: string
  onDone: () => void
  onCancel: () => void
}) {
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<FormValues>({ resolver: zodResolver(schema), defaultValues: { role: 'member' } })
  const [createInvite, { isLoading, error }] = useCreateWorkspaceInviteMutation()
  const emailId = useId()
  const roleId = useId()

  async function onSubmit(values: FormValues) {
    const result = await createInvite({ id: workspaceId, createInviteRequest: values })
    if ('data' in result && result.data) onDone()
  }

  function errorMessage() {
    const status = (error as { status?: number })?.status
    if (status === 409) return 'An invite is already pending for that email.'
    if (status === 400) return 'Please enter a valid email address.'
    return "Couldn't send the invite. Please try again."
  }

  return (
    <div className="border-b border-border bg-surface/40">
      <div className="flex h-10 items-center border-b border-border px-5">
        <span className="font-mono text-[10.5px] uppercase tracking-[0.14em] text-faint">Invite a teammate</span>
      </div>

      <form onSubmit={handleSubmit(onSubmit)} noValidate className="grid gap-4 p-5 md:grid-cols-[1fr_auto_auto]">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor={emailId}>Email</Label>
          <Input
            id={emailId}
            type="email"
            autoFocus
            placeholder="teammate@company.com"
            aria-invalid={!!errors.email}
            {...register('email')}
          />
          {errors.email && (
            <span role="alert" className="text-xs text-danger">
              {errors.email.message}
            </span>
          )}
        </div>

        <div className="flex flex-col gap-1.5">
          <Label htmlFor={roleId}>Role</Label>
          <select id={roleId} className={cn(field)} {...register('role')}>
            <option value="member">Member</option>
            <option value="admin">Admin</option>
          </select>
        </div>

        <div className="flex items-end gap-2">
          <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" size="sm" disabled={isLoading}>
            {isLoading && <Loader2 className="animate-spin" />}
            {isLoading ? 'Sending…' : 'Send invite'}
          </Button>
        </div>

        {error && (
          <p
            role="alert"
            className="md:col-span-3 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger"
          >
            {errorMessage()}
          </p>
        )}
      </form>
    </div>
  )
}

function LoadingRows() {
  return (
    <ul>
      {[0, 1].map((i) => (
        <li key={i} className="flex items-center gap-4 border-b border-border px-5 py-3.5">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-48" />
            <Skeleton className="h-2.5 w-32" />
          </div>
          <Skeleton className="h-4 w-16" />
        </li>
      ))}
    </ul>
  )
}
