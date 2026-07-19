import { useState } from 'react'
import { Eye, EyeOff, ArrowBigUp } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Input } from './input'

type PasswordInputProps = Omit<React.InputHTMLAttributes<HTMLInputElement>, 'type'> & {
  ref?: React.Ref<HTMLInputElement>
}

/**
 * Password field with an accessible show/hide toggle and a Caps Lock warning.
 * Spreads through to the underlying Input, so react-hook-form's
 * `{...register('password')}` (name, onChange, onBlur, ref) works unchanged.
 */
export function PasswordInput({ className, onKeyUp, onBlur, ref, ...props }: PasswordInputProps) {
  const [visible, setVisible] = useState(false)
  const [capsOn, setCapsOn] = useState(false)

  return (
    <div className="relative">
      <Input
        ref={ref}
        type={visible ? 'text' : 'password'}
        className={cn('pr-16', className)}
        onKeyUp={(e) => {
          setCapsOn(e.getModifierState?.('CapsLock') ?? false)
          onKeyUp?.(e)
        }}
        onBlur={(e) => {
          setCapsOn(false)
          onBlur?.(e)
        }}
        {...props}
      />
      <div className="absolute inset-y-0 right-0 flex items-center">
        {capsOn && (
          <span
            className="grid w-7 place-items-center text-warm"
            role="status"
            aria-label="Caps Lock is on"
            title="Caps Lock is on"
          >
            <ArrowBigUp className="size-4" />
          </span>
        )}
        <button
          type="button"
          onClick={() => setVisible((v) => !v)}
          aria-label={visible ? 'Hide password' : 'Show password'}
          aria-pressed={visible}
          title={visible ? 'Hide password' : 'Show password'}
          className="grid w-9 place-items-center self-stretch rounded-r-md text-faint transition-colors hover:text-foreground focus-visible:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        >
          {visible ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
        </button>
      </div>
    </div>
  )
}
