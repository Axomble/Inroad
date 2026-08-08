import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Input } from '@/components/ui/input'
import { Select } from '@/components/ui/select'
import { Skeleton } from '@/components/ui/skeleton'
import { useCrmCreateDealMutation, useCrmListCompaniesQuery, useCrmListPipelinesQuery } from './api'
import { QueryErrorBanner } from './record-parts'
import { Field, FormShell } from './form-shell'
import { currencyField, optionalMoney } from './form-schema'
import { listPageSize } from './query-args'
import { toMicros } from './money'

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

/**
 * Creates a deal. A deal cannot name a stage without its pipelines, and offers
 * an account without the company list, so this form owns both queries and gates
 * *itself* on them — a 500 on pipelines must never take the surrounding page's
 * own list down with it.
 *
 * `companyId` preselects the account, so the same form serves "new deal" from
 * the deals page and "new deal for this company" from a company record.
 */
export function DealForm({ companyId = '', onDone }: { companyId?: string; onDone: () => void }) {
  const pipelinesQuery = useCrmListPipelinesQuery()
  const companiesQuery = useCrmListCompaniesQuery({ limit: listPageSize })
  const [create, state] = useCrmCreateDealMutation()
  const nameId = useId()
  const pipelineId = useId()
  const stageId = useId()
  const companyFieldId = useId()
  const amountId = useId()
  const currencyId = useId()
  const closeId = useId()
  const { register, handleSubmit, watch, setValue, formState: { errors } } = useForm<DealValues>({
    resolver: zodResolver(dealSchema),
    defaultValues: { currency: 'USD', company_id: companyId, amount: '', close_date: '', pipeline_id: '', stage_id: '' },
  })
  const pipelines = pipelinesQuery.data?.items ?? []
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
        // The date input yields `YYYY-MM-DD`; the API takes an RFC 3339 instant,
        // and the close date is a whole day, so it anchors at UTC midnight.
        close_date: values.close_date ? `${values.close_date}T00:00:00Z` : undefined,
      },
    })
    if ('data' in result) onDone()
  })

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
      <Field
        id={companyFieldId}
        label="Company"
        hint={companiesQuery.isError ? 'Unavailable' : 'Optional'}
      >
        {/* A failed company list is not a reason to block the deal: the field
            degrades to "no company" and says so, rather than offering an empty
            menu that reads as "this workspace has no companies". */}
        <Select id={companyFieldId} disabled={companiesQuery.isError} {...register('company_id')}>
          <option value="">No company</option>
          {(companiesQuery.data?.items ?? []).map((company) => (
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
