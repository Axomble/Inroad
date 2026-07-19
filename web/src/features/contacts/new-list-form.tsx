import { useId } from 'react'
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { Loader2 } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useCreateListMutation } from './api'

const schema = z.object({
  name: z.string().min(1, 'Required'),
})
type Values = z.infer<typeof schema>

function createListErrorMessage(error: unknown): string {
  const status = (error as { status?: number | string })?.status
  if (status === 409) return 'A list with this name already exists.'
  if (status === 400) return 'Please enter a list name.'
  return "Couldn't create the list. Please try again."
}

export function NewListForm({ onDone, onCancel }: { onDone: (id: string) => void; onCancel: () => void }) {
  const {
    register,
    handleSubmit,
    formState: { errors },
  } = useForm<Values>({ resolver: zodResolver(schema) })
  const [createList, { isLoading, error }] = useCreateListMutation()
  const nameId = useId()

  async function onSubmit(values: Values) {
    const result = await createList({ body: values })
    if ('data' in result && result.data) onDone(result.data.id ?? '')
  }

  return (
    <form onSubmit={handleSubmit(onSubmit)} noValidate className="border-b border-border bg-surface/40 p-3">
      <Label htmlFor={nameId}>List name</Label>
      <div className="mt-1.5 flex items-center gap-2">
        <Input
          id={nameId}
          autoFocus
          placeholder="Q3 outbound"
          aria-invalid={!!errors.name}
          {...register('name')}
        />
        <Button type="submit" variant="primary" size="sm" disabled={isLoading}>
          {isLoading && <Loader2 className="animate-spin" />}
          Create
        </Button>
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          Cancel
        </Button>
      </div>
      {errors.name && (
        <span role="alert" className="mt-1 block text-xs text-danger">
          {errors.name.message}
        </span>
      )}
      {error && (
        <p role="alert" className="mt-2 rounded-md border border-danger/30 bg-danger/10 px-3 py-2 text-xs text-danger">
          {createListErrorMessage(error)}
        </p>
      )}
    </form>
  )
}
