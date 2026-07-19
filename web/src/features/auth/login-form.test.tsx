import { render, screen } from '@testing-library/react'
import { Provider } from 'react-redux'
import { vi } from 'vitest'
import { store } from '../../store'
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
  render(
    <Provider store={store}>
      <LoginForm />
    </Provider>,
  )
  expect(screen.getByLabelText('Email')).toBeInTheDocument()
  expect(screen.getByLabelText('Password')).toBeInTheDocument()
})
