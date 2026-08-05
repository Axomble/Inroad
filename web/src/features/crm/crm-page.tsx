import { memo, useId, useRef, useState } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Building2, CircleDollarSign, GitBranch, Loader2, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { EmptyBlock, Page, PageBody, PageTopbar, SectionBar, Stat, StatStrip } from '@/components/layout/page'
import { cn } from '@/lib/utils'
import {
  useCrmCreateCompanyMutation,
  useCrmCreateDealMutation,
  useCrmCreatePipelineMutation,
  useCrmGetSettingsQuery,
  useCrmListCompaniesQuery,
  useCrmListDealsQuery,
  useCrmListPipelinesQuery,
  useCrmUpdateSettingsMutation,
  type AutoCapturePolicy,
  type CrmCompany,
  type CrmDeal,
  type CrmPipeline,
} from './api'
import { crmErrorMessage } from './error-copy'
import { formatMoney, formatTotal, toMicros } from './money'

type View = 'deals' | 'companies' | 'pipelines'

const tabs: ReadonlyArray<{ id: View; label: string; icon: typeof Building2 }> = [
  { id: 'deals', label: 'Deals', icon: CircleDollarSign },
  { id: 'companies', label: 'Companies', icon: Building2 },
  { id: 'pipelines', label: 'Pipelines', icon: GitBranch },
]

// `data?.items ?? []` would hand every render a fresh array while a query is
// uninitialised or erroring, which defeats the `memo()` on the list components.
const noCompanies: readonly CrmCompany[] = Object.freeze([])
const noDeals: readonly CrmDeal[] = Object.freeze([])
const noPipelines: readonly CrmPipeline[] = Object.freeze([])

/**
 * The CRM console: deals, companies and pipelines behind one set of tabs.
 *
 * Deliberately omitted in this slice (see spec §11): the kanban board and deal
 * detail live on their own routes (`/app/deals`), and editing/deleting records
 * is not surfaced here yet — this page creates and lists.
 */
export function CRMPage() {
  const [view, setView] = useState<View>('deals')
  const [creating, setCreating] = useState(false)
  const tabsId = useId()
  const companiesQuery = useCrmListCompaniesQuery()
  const pipelinesQuery = useCrmListPipelinesQuery()
  const dealsQuery = useCrmListDealsQuery()
  const settingsQuery = useCrmGetSettingsQuery()
  const [updateSettings, settingsState] = useCrmUpdateSettingsMutation()

  const companies = companiesQuery.data?.items ?? noCompanies
  const pipelines = pipelinesQuery.data?.items ?? noPipelines
  const deals = dealsQuery.data?.items ?? noDeals

  // Each tab reads exactly one list, so loading and failure are scoped to the
  // active tab: a 500 on companies must not make Deals unreachable.
  const activeQuery = view === 'companies' ? companiesQuery : view === 'pipelines' ? pipelinesQuery : dealsQuery
  const openPipeline = summariseOpenPipeline(deals)

  return (
    <Page>
      <PageTopbar
        eyebrow="CRM"
        title="Revenue workspace"
        subtitle="Companies, deal stages, and follow-up context in one workspace."
        actions={
          <div className="flex flex-wrap items-center justify-end gap-2">
            <label className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
              Positive reply capture
              <Select
                wrapperClassName="w-auto"
                className="h-8 w-auto min-w-32 text-xs"
                value={settingsQuery.data?.auto_capture_policy ?? 'sent'}
                disabled={settingsQuery.isLoading || settingsState.isLoading}
                onChange={(event) =>
                  void updateSettings({
                    crmSettingsInput: { auto_capture_policy: event.target.value as AutoCapturePolicy },
                  })
                }
              >
                <option value="sent">Sent campaigns</option>
                <option value="sent_and_received">Sent and received</option>
                <option value="off">Off</option>
              </Select>
            </label>
            {settingsState.isError ? (
              <span role="alert" className="text-xs text-danger">
                {crmErrorMessage(settingsState.error, 'The capture policy could not be updated.')}
              </span>
            ) : null}
            <Button variant="primary" size="sm" onClick={() => setCreating((open) => !open)} aria-expanded={creating}>
              <Plus aria-hidden="true" />
              New {view === 'companies' ? 'company' : view === 'pipelines' ? 'pipeline' : 'deal'}
            </Button>
          </div>
        }
      />

      <StatStrip>
        <Stat label="Open pipeline" value={openPipeline.value} sub={openPipeline.sub} />
        <Stat
          label="Companies"
          value={companiesQuery.isError ? '—' : companies.length}
          sub="in this workspace"
        />
        <Stat label="Won" value={dealsQuery.isError ? '—' : deals.filter((deal) => deal.stage_is_won).length} sub="closed deals" />
        <Stat
          label="Pipelines"
          value={pipelinesQuery.isError ? '—' : pipelines.length}
          sub="including default"
        />
      </StatStrip>

      <ViewTabs baseId={tabsId} view={view} onSelect={(next) => { setView(next); setCreating(false) }} />

      {creating ? (
        <CreateForm view={view} companies={companies} pipelines={pipelines} pipelinesQuery={pipelinesQuery} onDone={() => setCreating(false)} />
      ) : null}

      <PageBody>
        <div role="tabpanel" id={`${tabsId}-panel-${view}`} aria-labelledby={`${tabsId}-tab-${view}`} tabIndex={0} className="outline-none">
          {activeQuery.isError ? (
            <QueryErrorBanner
              error={activeQuery.error}
              fallback="This list could not be loaded."
              onRetry={() => void activeQuery.refetch()}
              retrying={activeQuery.isFetching}
            />
          ) : activeQuery.isLoading ? (
            <LoadingRows />
          ) : view === 'companies' ? (
            <CompaniesList companies={companies} />
          ) : view === 'pipelines' ? (
            <PipelinesList pipelines={pipelines} />
          ) : (
            <DealsList deals={deals} />
          )}
        </div>
      </PageBody>
    </Page>
  )
}

/**
 * The tabs, implemented as the full WAI-ARIA pattern: roving tabindex (one tab
 * stop for the set), arrow/Home/End selection, and `aria-controls` pointing at
 * the panel the page renders. Declaring the roles without the keyboard
 * behaviour would promise a screen-reader user something that then does nothing.
 *
 * Kept as tabs rather than three routes: the topbar, stat strip and create form
 * are shared across all three views, so splitting them into routes would
 * duplicate that shell three times for no URL the spec asks for (§11 defines
 * routes only for the board and the deal page, which already exist).
 */
function ViewTabs({ baseId, view, onSelect }: { baseId: string; view: View; onSelect: (next: View) => void }) {
  const tabRefs = useRef(new Map<View, HTMLButtonElement>())

  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const current = tabs.findIndex((tab) => tab.id === view)
    let index = current
    switch (event.key) {
      case 'ArrowRight':
        index = (current + 1) % tabs.length
        break
      case 'ArrowLeft':
        index = (current - 1 + tabs.length) % tabs.length
        break
      case 'Home':
        index = 0
        break
      case 'End':
        index = tabs.length - 1
        break
      default:
        return
    }
    const next = tabs[index]
    if (!next) return
    event.preventDefault()
    onSelect(next.id)
    tabRefs.current.get(next.id)?.focus()
  }

  return (
    <div
      role="tablist"
      aria-label="CRM views"
      onKeyDown={onKeyDown}
      className="flex min-h-11 items-center gap-1 border-b border-border px-4 sm:px-5"
    >
      {tabs.map(({ id, label, icon: Icon }) => (
        <button
          key={id}
          ref={(node) => {
            if (node) tabRefs.current.set(id, node)
            else tabRefs.current.delete(id)
          }}
          type="button"
          role="tab"
          id={`${baseId}-tab-${id}`}
          aria-selected={view === id}
          aria-controls={`${baseId}-panel-${id}`}
          tabIndex={view === id ? 0 : -1}
          onClick={() => onSelect(id)}
          className={cn(
            'flex min-h-9 items-center gap-2 rounded-md px-3 text-sm text-muted-foreground outline-none transition-colors hover:bg-surface-2 hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring',
            view === id && 'bg-surface-2 font-medium text-foreground',
          )}
        >
          <Icon className="size-4" aria-hidden="true" />
          {label}
        </button>
      ))}
    </div>
  )
}

/** The one alert surface for a failed CRM list, with the retry the user needs. */
function QueryErrorBanner({
  error,
  fallback,
  onRetry,
  retrying,
}: {
  error: unknown
  fallback: string
  onRetry: () => void
  retrying: boolean
}) {
  return (
    <div
      role="alert"
      className="m-5 flex flex-wrap items-center gap-3 rounded-md border border-danger/30 bg-danger/10 p-4 text-sm text-danger"
    >
      <span className="min-w-0 flex-1">{crmErrorMessage(error, fallback)}</span>
      <Button size="sm" onClick={onRetry} disabled={retrying}>
        {retrying ? <Loader2 className="animate-spin" aria-hidden="true" /> : null}
        Try again
      </Button>
    </div>
  )
}

const CompaniesList = memo(function CompaniesList({ companies }: { companies: readonly CrmCompany[] }) {
  if (companies.length === 0) {
    return (
      <EmptyBlock
        title="No companies yet"
        description="Create a company to connect deals and contacts to an account."
      />
    )
  }
  return (
    <div className="divide-y divide-border [content-visibility:auto]">
      {companies.map((company) => (
        <article
          key={company.id}
          className="grid min-h-16 grid-cols-[minmax(0,1fr)_auto] items-center gap-4 px-5 py-3 hover:bg-surface/60"
        >
          <div className="min-w-0">
            <h2 className="truncate text-sm font-medium">{company.name}</h2>
            <p className="truncate text-xs text-muted-foreground">{company.domain || 'No domain added'}</p>
          </div>
          <div className="text-right">
            <p className="font-mono text-xs tabular-nums">{company.deal_count} deals</p>
            <p className="text-[11px] text-muted-foreground">
              {company.annual_revenue_micros == null
                ? 'Revenue not set'
                : formatMoney(company.annual_revenue_micros, company.currency)}
            </p>
          </div>
        </article>
      ))}
    </div>
  )
})

const DealsList = memo(function DealsList({ deals }: { deals: readonly CrmDeal[] }) {
  if (deals.length === 0) {
    return (
      <EmptyBlock
        title="No deals in the pipeline"
        description="Create a deal and place it in a stage to begin tracking revenue."
      />
    )
  }
  return (
    <div className="divide-y divide-border [content-visibility:auto]">
      {deals.map((deal) => (
        <article
          key={deal.id}
          className="grid min-h-16 grid-cols-[minmax(0,1fr)_auto] items-center gap-4 px-5 py-3 hover:bg-surface/60 sm:grid-cols-[minmax(0,1fr)_minmax(8rem,0.5fr)_auto]"
        >
          <div className="min-w-0">
            <h2 className="truncate text-sm font-medium">{deal.name}</h2>
            <p className="truncate text-xs text-muted-foreground">{deal.company_name || 'Unlinked company'}</p>
          </div>
          <div className="hidden min-w-0 sm:block">
            <span className="inline-flex items-center gap-1.5 text-xs">
              <span className="size-2 rounded-full" style={{ backgroundColor: deal.stage_color }} aria-hidden="true" />
              {deal.stage_label}
            </span>
            <p className="truncate text-[11px] text-muted-foreground">{deal.pipeline_name}</p>
          </div>
          <p className="font-mono text-sm tabular-nums">
            {deal.amount_micros == null ? '—' : formatMoney(deal.amount_micros, deal.currency)}
          </p>
        </article>
      ))}
    </div>
  )
})

const PipelinesList = memo(function PipelinesList({ pipelines }: { pipelines: readonly CrmPipeline[] }) {
  if (pipelines.length === 0) {
    return (
      <EmptyBlock
        title="No pipelines"
        description="Create a pipeline to define how deals move from lead to won or lost."
      />
    )
  }
  return (
    <div className="grid gap-4 p-4 md:grid-cols-2 xl:grid-cols-3">
      {pipelines.map((pipeline) => (
        <article key={pipeline.id} className="rounded-lg border border-border bg-surface p-4">
          <div className="flex items-center justify-between gap-3">
            <h2 className="truncate text-sm font-semibold">{pipeline.name}</h2>
            {pipeline.is_default && (
              <span className="rounded bg-primary/10 px-2 py-0.5 font-mono text-[10px] uppercase text-primary">
                Default
              </span>
            )}
          </div>
          <ol className="mt-4 space-y-2">
            {pipeline.stages.map((stage) => (
              <li key={stage.id} className="flex min-h-8 items-center gap-2 rounded-md bg-surface-2 px-3 text-xs">
                <span className="size-2 rounded-full" style={{ backgroundColor: stage.color }} aria-hidden="true" />
                <span className="truncate">{stage.label}</span>
                {(stage.is_won || stage.is_lost) && (
                  <span className="ml-auto text-[10px] uppercase text-muted-foreground">
                    {stage.is_won ? 'Won' : 'Lost'}
                  </span>
                )}
              </li>
            ))}
          </ol>
        </article>
      ))}
    </div>
  )
})

/**
 * Picks the create form for the active tab. The deal form is the only one with
 * a cross-query dependency — it cannot offer a stage without the pipelines —
 * so only that form is gated on the pipelines query, never the deal *list*.
 */
function CreateForm({
  view,
  companies,
  pipelines,
  pipelinesQuery,
  onDone,
}: {
  view: View
  companies: readonly CrmCompany[]
  pipelines: readonly CrmPipeline[]
  pipelinesQuery: { isLoading: boolean; isError: boolean; error?: unknown; isFetching: boolean; refetch: () => unknown }
  onDone: () => void
}) {
  if (view === 'companies') return <CompanyForm onDone={onDone} />
  if (view === 'pipelines') return <PipelineForm onDone={onDone} />
  if (pipelinesQuery.isError) {
    return (
      <QueryErrorBanner
        error={pipelinesQuery.error}
        fallback="Pipelines could not be loaded, so a deal cannot be created yet."
        onRetry={() => void pipelinesQuery.refetch()}
        retrying={pipelinesQuery.isFetching}
      />
    )
  }
  if (pipelinesQuery.isLoading) {
    return (
      <div className="border-b border-border p-5">
        <Skeleton className="h-20 w-full" />
      </div>
    )
  }
  return <DealForm companies={companies} pipelines={pipelines} onDone={onDone} />
}

// The API rejects anything but three letters (`^[A-Z]{3}$`) and the form
// upper-cases before sending, so the client mirrors that rule rather than
// merely counting characters — `12a` used to pass here and 400 at the server.
const currencyField = z
  .string()
  .trim()
  .regex(/^[A-Za-z]{3}$/, 'Use a three-letter currency code')
const optionalMoney = z
  .string()
  .refine((value) => value === '' || (Number.isFinite(Number(value)) && Number(value) >= 0), 'Enter a positive amount')

const companySchema = z.object({
  name: z.string().trim().min(1, 'Company name is required').max(200),
  domain: z.string().trim(),
  currency: currencyField,
  revenue: optionalMoney,
})
type CompanyValues = z.infer<typeof companySchema>

function CompanyForm({ onDone }: { onDone: () => void }) {
  const [create, state] = useCrmCreateCompanyMutation()
  const nameId = useId()
  const domainId = useId()
  const revenueId = useId()
  const currencyId = useId()
  const { register, handleSubmit, formState: { errors } } = useForm<CompanyValues>({
    resolver: zodResolver(companySchema),
    defaultValues: { currency: 'USD', domain: '', revenue: '' },
  })

  const submit = handleSubmit(async (values) => {
    const result = await create({
      crmCompanyInput: {
        name: values.name,
        domain: values.domain,
        currency: values.currency.toUpperCase(),
        annual_revenue_micros: toMicros(values.revenue),
      },
    })
    // On failure the reason stays on `state.error`, which FormShell renders.
    if ('data' in result) onDone()
  })

  return (
    <FormShell title="New company" onSubmit={submit} busy={state.isLoading} error={state.isError ? state.error : undefined}>
      <Field id={nameId} label="Company name" error={errors.name?.message}>
        <Input id={nameId} autoFocus aria-invalid={!!errors.name} {...register('name')} />
      </Field>
      <Field id={domainId} label="Domain" hint="example.com" error={errors.domain?.message}>
        <Input id={domainId} inputMode="url" placeholder="example.com" {...register('domain')} />
      </Field>
      <Field id={revenueId} label="Annual revenue" hint="Whole currency units" error={errors.revenue?.message}>
        <Input id={revenueId} type="number" min="0" step="0.01" inputMode="decimal" {...register('revenue')} />
      </Field>
      <Field id={currencyId} label="Currency" error={errors.currency?.message}>
        <Input id={currencyId} maxLength={3} className="uppercase" aria-invalid={!!errors.currency} {...register('currency')} />
      </Field>
    </FormShell>
  )
}

const pipelineSchema = z.object({ name: z.string().trim().min(1, 'Pipeline name is required').max(120) })

function PipelineForm({ onDone }: { onDone: () => void }) {
  const [create, state] = useCrmCreatePipelineMutation()
  const id = useId()
  const { register, handleSubmit, formState: { errors } } = useForm<z.infer<typeof pipelineSchema>>({
    resolver: zodResolver(pipelineSchema),
  })

  const submit = handleSubmit(async (values) => {
    const result = await create({ crmPipelineInput: values })
    if ('data' in result) onDone()
  })

  return (
    <FormShell title="New pipeline" onSubmit={submit} busy={state.isLoading} error={state.isError ? state.error : undefined}>
      <Field
        id={id}
        label="Pipeline name"
        hint="Five practical stages are added automatically."
        error={errors.name?.message}
      >
        <Input id={id} autoFocus placeholder="Sales pipeline" aria-invalid={!!errors.name} {...register('name')} />
      </Field>
    </FormShell>
  )
}

const dealSchema = z.object({
  name: z.string().trim().min(1, 'Deal name is required').max(200),
  pipeline_id: z.string().uuid('Select a pipeline'),
  stage_id: z.string().uuid('Select a stage'),
  company_id: z.string(),
  amount: optionalMoney,
  currency: currencyField,
  close_date: z.string(),
})
type DealValues = z.infer<typeof dealSchema>

function DealForm({
  companies,
  pipelines,
  onDone,
}: {
  companies: readonly CrmCompany[]
  pipelines: readonly CrmPipeline[]
  onDone: () => void
}) {
  const [create, state] = useCrmCreateDealMutation()
  const nameId = useId()
  const pipelineId = useId()
  const stageId = useId()
  const companyId = useId()
  const amountId = useId()
  const currencyId = useId()
  const closeId = useId()
  const { register, handleSubmit, watch, setValue, formState: { errors } } = useForm<DealValues>({
    resolver: zodResolver(dealSchema),
    defaultValues: { currency: 'USD', company_id: '', amount: '', close_date: '', pipeline_id: '', stage_id: '' },
  })
  const selectedPipeline = pipelines.find((pipeline) => pipeline.id === watch('pipeline_id'))

  const submit = handleSubmit(async (values) => {
    const result = await create({
      crmDealInput: {
        name: values.name,
        pipeline_id: values.pipeline_id,
        stage_id: values.stage_id,
        company_id: values.company_id || undefined,
        amount_micros: toMicros(values.amount),
        currency: values.currency.toUpperCase(),
        // The date input yields `YYYY-MM-DD`; the API takes an RFC 3339
        // instant, and the close date is a whole day, so it anchors at UTC
        // midnight.
        close_date: values.close_date ? `${values.close_date}T00:00:00Z` : undefined,
      },
    })
    if ('data' in result) onDone()
  })

  return (
    <FormShell title="New deal" onSubmit={submit} busy={state.isLoading} error={state.isError ? state.error : undefined}>
      <Field id={nameId} label="Deal name" error={errors.name?.message}>
        <Input id={nameId} autoFocus aria-invalid={!!errors.name} {...register('name')} />
      </Field>
      <Field id={pipelineId} label="Pipeline" error={errors.pipeline_id?.message}>
        {/* A stage belongs to exactly one pipeline, so changing the pipeline
            clears the stage — otherwise the form would submit a stage from the
            previous pipeline. */}
        <Select
          id={pipelineId}
          aria-invalid={!!errors.pipeline_id}
          {...register('pipeline_id', { onChange: () => setValue('stage_id', '') })}
        >
          <option value="">Select pipeline…</option>
          {pipelines.map((pipeline) => (
            <option key={pipeline.id} value={pipeline.id}>{pipeline.name}</option>
          ))}
        </Select>
      </Field>
      <Field id={stageId} label="Stage" error={errors.stage_id?.message}>
        <Select id={stageId} disabled={!selectedPipeline} aria-invalid={!!errors.stage_id} {...register('stage_id')}>
          <option value="">Select stage…</option>
          {selectedPipeline?.stages.map((stage) => (
            <option key={stage.id} value={stage.id}>{stage.label}</option>
          ))}
        </Select>
      </Field>
      <Field id={companyId} label="Company" hint="Optional">
        <Select id={companyId} {...register('company_id')}>
          <option value="">No company</option>
          {companies.map((company) => (
            <option key={company.id} value={company.id}>{company.name}</option>
          ))}
        </Select>
      </Field>
      <Field id={amountId} label="Amount" hint="Whole currency units" error={errors.amount?.message}>
        <Input id={amountId} type="number" min="0" step="0.01" inputMode="decimal" {...register('amount')} />
      </Field>
      <Field id={currencyId} label="Currency" error={errors.currency?.message}>
        <Input id={currencyId} maxLength={3} className="uppercase" aria-invalid={!!errors.currency} {...register('currency')} />
      </Field>
      <Field id={closeId} label="Expected close" hint="Optional">
        <Input id={closeId} type="date" {...register('close_date')} />
      </Field>
    </FormShell>
  )
}

function FormShell({
  title,
  onSubmit,
  busy,
  error,
  children,
}: {
  title: string
  onSubmit: React.FormEventHandler<HTMLFormElement>
  busy: boolean
  /** The RTK error itself, not a boolean — the server's reason is the message. */
  error: unknown
  children: React.ReactNode
}) {
  return (
    <form onSubmit={onSubmit} noValidate className="border-b border-border bg-surface/50">
      <SectionBar label={title} />
      <div className="grid gap-4 p-5 sm:grid-cols-2 lg:grid-cols-4">{children}</div>
      {error !== undefined && (
        <p role="alert" className="mx-5 mb-3 text-xs text-danger">
          {crmErrorMessage(error, 'The record could not be saved. Review the fields and try again.')}
        </p>
      )}
      <div className="flex justify-end border-t border-border px-5 py-3">
        <Button type="submit" variant="primary" size="sm" disabled={busy}>
          {busy && <Loader2 className="animate-spin" aria-hidden="true" />}
          {busy ? 'Saving…' : 'Save'}
        </Button>
      </div>
    </form>
  )
}

function Field({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string
  label: string
  hint?: string
  error?: string
  children: React.ReactNode
}) {
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <div className="flex items-baseline justify-between gap-2">
        <Label htmlFor={id}>{label}</Label>
        {hint && <span className="text-[10px] text-muted-foreground">{hint}</span>}
      </div>
      {children}
      {error && <span role="alert" className="text-xs text-danger">{error}</span>}
    </div>
  )
}

function LoadingRows() {
  return (
    <div className="space-y-3 p-5" aria-label="Loading CRM">
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-14 w-full" />
      <Skeleton className="h-14 w-full" />
    </div>
  )
}

/**
 * "Open pipeline" counts only deals still in play — a won or lost deal is not
 * pipeline. Amounts are per-deal currency, so a mixed-currency workspace gets
 * an em dash rather than a fabricated total in one currency.
 */
function summariseOpenPipeline(deals: readonly CrmDeal[]): { value: string; sub: string } {
  const open = deals.filter((deal) => !deal.stage_is_won && !deal.stage_is_lost)
  const currencies = new Set(open.map((deal) => deal.currency))
  const sub =
    currencies.size > 1
      ? `${open.length} open deals across ${currencies.size} currencies`
      : `${open.length} open deal${open.length === 1 ? '' : 's'}`
  const total = open.reduce((sum, deal) => sum + (deal.amount_micros ?? 0), 0)
  return { value: formatTotal(total, open), sub }
}
