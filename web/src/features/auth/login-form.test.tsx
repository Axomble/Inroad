import { screen } from '@testing-library/react'
import { vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { LoginForm } from './login-form'

// LoginForm uses the router's useNavigate + Link; stub them for the unit test.
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => () => {},
  Link: ({ to, children, ...props }: { to: string; children: React.ReactNode }) => (
    <a href={to} {...props}>
      {children}
    </a>
  ),
}))

test('renders email and password fields', () => {
  renderWithProviders(<LoginForm />)
  expect(screen.getByLabelText('Email')).toBeInTheDocument()
  expect(screen.getByLabelText('Password')).toBeInTheDocument()
})
