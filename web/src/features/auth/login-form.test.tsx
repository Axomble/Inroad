import { render, screen } from '@testing-library/react'
import { Provider } from 'react-redux'
import { vi } from 'vitest'
import { store } from '../../store'
import { LoginForm } from './login-form'

// LoginForm uses the router's useNavigate on success; stub it for the unit test.
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => () => {},
}))

test('renders email and password fields', () => {
  render(
    <Provider store={store}>
      <LoginForm />
    </Provider>,
  )
  expect(screen.getByLabelText(/email/i)).toBeInTheDocument()
  expect(screen.getByLabelText(/password/i)).toBeInTheDocument()
})
