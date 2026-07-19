import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useLoginMutation } from './api'

const schema = z.object({
  email: z.string().email(),
  password: z.string().min(1),
})
type FormValues = z.infer<typeof schema>

export function LoginForm() {
  const { register, handleSubmit, formState: { errors } } = useForm<FormValues>({
    resolver: zodResolver(schema),
  })
  const [login, { isLoading, data }] = useLoginMutation()

  return (
    <form
      onSubmit={handleSubmit((v) => login({ loginRequest: v }))}
      className="mx-auto flex max-w-sm flex-col gap-3 p-6"
    >
      <input aria-label="email" placeholder="Email" {...register('email')} className="border p-2" />
      {errors.email && <span role="alert">Invalid email</span>}
      <input aria-label="password" type="password" placeholder="Password" {...register('password')} className="border p-2" />
      <button disabled={isLoading} className="bg-brand-500 p-2 text-white">Log in</button>
      {data && <p>Signed in as {data.user_id}</p>}
    </form>
  )
}
