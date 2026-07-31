import { useEffect, useState } from 'react'

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
  const [svgSrc, setSvgSrc] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let cancelled = false
    setSvgSrc(null)
    setFailed(false)
    void (async () => {
      try {
        const { default: QRCode } = await import('qrcode')
        const svg = await QRCode.toString(value, { type: 'svg', margin: 1, width: size })
        if (!cancelled) {
          // Encode the generated (non-user) SVG into a data URL for a plain
          // <img>, avoiding dangerouslySetInnerHTML entirely.
          setSvgSrc(`data:image/svg+xml,${encodeURIComponent(svg)}`)
        }
      } catch {
        if (!cancelled) setFailed(true)
      }
    })()
    return () => {
      cancelled = true
    }
  }, [value, size])

  if (failed) return null

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
