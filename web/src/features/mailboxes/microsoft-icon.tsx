/**
 * The official Microsoft logo — four equal squares (red, green, blue, yellow)
 * from Microsoft's brand guidelines. Purely decorative when paired with a text
 * label, so it defaults to aria-hidden; callers rendering it alone must pass an
 * accessible name.
 */
export function MicrosoftIcon({ className, ...props }: React.SVGProps<SVGSVGElement>) {
  return (
    <svg
      viewBox="0 0 23 23"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
      focusable="false"
      className={className}
      {...props}
    >
      <rect x="1" y="1" width="10" height="10" fill="#F25022" />
      <rect x="12" y="1" width="10" height="10" fill="#7FBA00" />
      <rect x="1" y="12" width="10" height="10" fill="#00A4EF" />
      <rect x="12" y="12" width="10" height="10" fill="#FFB900" />
    </svg>
  )
}
