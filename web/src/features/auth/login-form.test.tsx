import { render, screen } from '@testing-library/react'
import { Provider } from 'react-redux'
import { store } from '../../store'
import { LoginForm } from './login-form'

test('renders email and password fields', () => {
  render(
    <Provider store={store}>
      <LoginForm />
    </Provider>,
  )
  expect(screen.getByLabelText(/email/i)).toBeInTheDocument()
  expect(screen.getByLabelText(/password/i)).toBeInTheDocument()
})
