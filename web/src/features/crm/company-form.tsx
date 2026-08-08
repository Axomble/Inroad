import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Input } from '@/components/ui/input'
import { useCrmCreateCompanyMutation } from './api'
import { Field, FormShell } from './form-shell'
import { currencyField, optionalMoney } from './form-schema'
import { toMicros } from './money'

const companySchema = z.object({
  name: z.string().trim().min(1, 'Company name is required').max(200),
  domain: z.string().trim(),
  currency: currencyField,
  revenue: optionalMoney,
})
type CompanyValues = z.infer<typeof companySchema>

export function CompanyForm({ onDone }: { onDone: () => void }) {
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
