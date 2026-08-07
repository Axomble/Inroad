import type { ReplyLabelInput } from './api'

/**
 * The five automation role flags, in the order the form and the row badges
 * render them. One definition so the badge text, the form copy, and the API
 * field stay in step. `field` names the (snake_case) API property — the one
 * boundary where snake_case is correct.
 */
export const LABEL_FLAGS = [
  {
    field: 'stops_enrollment',
    badge: 'Stops sequence',
    title: 'Stops the sequence',
    description: 'Halt the enrollment when a reply gets this label.',
  },
  {
    field: 'is_automated',
    badge: 'Automated',
    title: 'Automated mail',
    description: 'Machine-generated mail (out-of-office / auto-reply), never a human reply.',
  },
  {
    field: 'suppresses_contact',
    badge: 'Suppresses',
    title: 'Suppresses the contact',
    description: 'Suppress the address workspace-wide, then stop (compliance).',
  },
  {
    field: 'captures_deal',
    badge: 'Captures deal',
    title: 'Captures a deal',
    description: 'Open or update a CRM deal from this reply.',
  },
  {
    field: 'defers_enrollment',
    badge: 'Defers',
    title: 'Defers the sequence',
    description:
      'Reschedule the next step past a return date stated in the body (capped at 30 days) instead of stopping.',
  },
] as const satisfies readonly {
  field: keyof Pick<
    ReplyLabelInput,
    'stops_enrollment' | 'is_automated' | 'suppresses_contact' | 'captures_deal' | 'defers_enrollment'
  >
  badge: string
  title: string
  description: string
}[]
