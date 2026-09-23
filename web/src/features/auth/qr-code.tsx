import { useEffect, useState } from 'react'

/** A settled render for one input: the SVG data URL, or null when it failed. */
type QrResult = { value: string; size: number; svgSrc: string | null }

/**
 * Renders an otpauth:// URI as a QR code, client-side. The `qrcode` renderer is
 * pulled in with a dynamic `import()` so it lands in its own bundle chunk rather
 * than the main app graph — it's only needed on the brief 2FA enrollment step.
 *
 * We render an SVG (via `toString`, not the canvas `toDataURL`) so it stays
 * crisp at any size and needs no `<canvas>` at runtime. The QR is a convenience:
 * the plaintext secret is always shown alongside as the authoritative fallback,
 * so a render failure here is non-fatal — we just show nothing.
 */
export function QrCode({ value, size = 184 }: { value: string; size?: number }) {
  // The render result remembers the input it was rendered for, so a new
  // `value`/`size` shows the placeholder again without an effect having to reset
  // state first — a stale result is simply not the current one.
  const [rendered, setRendered] = useState<QrResult | null>(null)
  const current = rendered !== null && rendered.value === value && rendered.size === size ? rendered : null

  useEffect(() => {
    let cancelled = false
    void (async () => {
      try {
        const { default: QRCode } = await import('qrcode')
        const svg = await QRCode.toString(value, { type: 'svg', margin: 1, width: size })
        if (!cancelled) {
          // Encode the generated (non-user) SVG into a data URL for a plain
          // <img>, avoiding dangerouslySetInnerHTML entirely.
          setRendered({ value, size, svgSrc: `data:image/svg+xml,${encodeURIComponent(svg)}` })
        }
      } catch {
        if (!cancelled) setRendered({ value, size, svgSrc: null })
      }
    })()
    return () => {
      cancelled = true
    }
  }, [value, size])

  // A settled result with no SVG is a failed render.
  if (current !== null && current.svgSrc === null) return null
  const svgSrc = current?.svgSrc

  return (
    <div
      className="flex items-center justify-center rounded-lg border border-border bg-white p-3"
      style={{ width: size + 24, height: size + 24 }}
    >
      {svgSrc ? (
        <img src={svgSrc} width={size} height={size} alt="QR code for two-factor authenticator setup" />
      ) : (
        <div className="size-full animate-pulse rounded bg-surface-2" aria-hidden="true" />
      )}
    </div>
  )
}
