import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Input } from '@/components/ui/input'
import { useCrmCreatePipelineMutation } from './api'
import { Field, FormShell } from './form-shell'

const pipelineSchema = z.object({ name: z.string().trim().min(1, 'Pipeline name is required').max(120) })

export function PipelineForm({ onDone }: { onDone: () => void }) {
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
