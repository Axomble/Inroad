import { createFileRoute } from '@tanstack/react-router'
import { LoginForm } from '../features/auth/login-form'

export const Route = createFileRoute('/')({
  component: () => <LoginForm />,
})
