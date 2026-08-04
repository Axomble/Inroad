import { useEffect, useRef } from 'react'

/**
 * Ambient product visualization for the auth split-screen: mailboxes (left)
 * warming up and emitting messages that arc into the inbox landing zone
 * (right). It's the product's thesis — deliverability — rendered as motion,
 * not decoration. Canvas keeps it cheap; honors prefers-reduced-motion by
 * painting a single static frame.
 *
 * Colors mirror the "Volt" design tokens (see globals.css). A canvas can't
 * cheaply read CSS custom properties, so the values are inlined — keep them in
 * sync with the tokens. `palette()` follows the active theme (light default,
 * `.dark` opt-in); a classList check per frame is cheap and auto-adapts if a
 * theme toggle is added. On light the lime is deepened from --primary so the
 * moving dots stay legible on the white ground.
 */
const ORANGE = '#ff7a1a' // warmup "heat" — bright in both themes
const OK = 'rgba(23,184,119,0.85)' // inbox arrival flash

function palette() {
  const dark = document.documentElement.classList.contains('dark')
  return dark
    ? {
        top: '#0f130a',
        bot: '#0b0e0a',
        grid: 'rgba(234,240,222,0.05)',
        zone: 'rgba(195,245,60,0.5)',
        accent: '#c3f53c',
        caption: 'rgba(234,240,222,0.4)',
      }
    : {
        top: '#ffffff',
        bot: '#eef2e2',
        grid: 'rgba(18,22,11,0.05)',
        zone: 'rgba(139,190,0,0.55)',
        accent: '#8bbe00',
        caption: 'rgba(17,21,13,0.4)',
      }
}

interface Particle {
  from: number // node index
  t: number // 0..1 progress
  speed: number
  cp: number // control-point vertical offset for the arc
}

export function AuthShowcase() {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return

    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches
    let width = 0
    let height = 0
    let dpr = Math.min(window.devicePixelRatio || 1, 2)

    // Sender mailboxes sit in the left band; inbox target on the right.
    const nodes: { x: number; y: number; warmth: number; phase: number }[] = []
    const particles: Particle[] = []

    function layout() {
      const rect = canvas!.getBoundingClientRect()
      width = rect.width
      height = rect.height
      dpr = Math.min(window.devicePixelRatio || 1, 2)
      canvas!.width = Math.round(width * dpr)
      canvas!.height = Math.round(height * dpr)
      ctx!.setTransform(dpr, 0, 0, dpr, 0, 0)

      nodes.length = 0
      const count = 6
      const bandX = width * 0.16
      const top = height * 0.24
      const span = height * 0.52
      for (let i = 0; i < count; i++) {
        nodes.push({
          x: bandX,
          y: top + (span * i) / (count - 1),
          warmth: 0.25 + (i % 3) * 0.25,
          phase: i * 1.1,
        })
      }
    }

    const inbox = () => ({ x: width * 0.82, y: height * 0.5 })

    function spawn() {
      if (particles.length > 14) return
      const from = Math.floor(Math.random() * nodes.length)
      particles.push({
        from,
        t: 0,
        speed: 0.0032 + Math.random() * 0.0022,
        cp: (Math.random() - 0.5) * height * 0.5,
      })
    }

    function bezier(p0: number, p1: number, p2: number, t: number) {
      const mt = 1 - t
      return mt * mt * p0 + 2 * mt * t * p1 + t * t * p2
    }

    /**
     * A tracked-uppercase mono caption, the canvas echo of the app's eyebrow
     * labels. `letterSpacing` is newer canvas API — where absent the caption
     * just renders untracked, which is fine.
     */
    function caption(text: string, x: number, y: number, color: string) {
      const c = ctx as CanvasRenderingContext2D & { letterSpacing?: string }
      ctx!.save()
      ctx!.font = '600 9px "Geist Mono Variable", ui-monospace, Menlo, monospace'
      if ('letterSpacing' in c) c.letterSpacing = '2px'
      ctx!.textAlign = 'center'
      ctx!.fillStyle = color
      ctx!.fillText(text, x, y)
      ctx!.restore()
    }

    function drawGrid(grid: string) {
      ctx!.save()
      ctx!.globalAlpha = 0.5
      ctx!.fillStyle = grid
      const gap = 26
      for (let x = gap; x < width; x += gap) {
        for (let y = gap; y < height; y += gap) {
          ctx!.beginPath()
          ctx!.arc(x, y, 1, 0, Math.PI * 2)
          ctx!.fill()
        }
      }
      ctx!.restore()
    }

    function frame(elapsed: number) {
      const pal = palette()
      ctx!.clearRect(0, 0, width, height)

      // ground
      const bg = ctx!.createLinearGradient(0, 0, width, height)
      bg.addColorStop(0, pal.top)
      bg.addColorStop(1, pal.bot)
      ctx!.fillStyle = bg
      ctx!.fillRect(0, 0, width, height)

      drawGrid(pal.grid)

      const target = inbox()

      // Inbox landing zone. Filled and furnished — a bare stroked rectangle
      // reads as an unfinished element in the reduced-motion still frame, so
      // it gets a faint fill, stacked "message" rows, and a caption that name
      // what the arcs are flying into.
      const zone = { x: target.x - 30, y: target.y - 34, w: 60, h: 68 }
      ctx!.save()
      ctx!.fillStyle = pal.accent
      ctx!.globalAlpha = 0.07
      ctx!.beginPath()
      ctx!.roundRect(zone.x, zone.y, zone.w, zone.h, 10)
      ctx!.fill()
      ctx!.globalAlpha = 1
      ctx!.strokeStyle = pal.zone
      ctx!.lineWidth = 1.5
      ctx!.beginPath()
      ctx!.roundRect(zone.x, zone.y, zone.w, zone.h, 10)
      ctx!.stroke()
      ctx!.globalAlpha = 0.45
      ctx!.lineWidth = 1
      for (let row = 0; row < 3; row++) {
        const rowY = zone.y + 16 + row * 13
        ctx!.beginPath()
        ctx!.moveTo(zone.x + 11, rowY)
        ctx!.lineTo(zone.x + zone.w - 11, rowY)
        ctx!.stroke()
      }
      ctx!.restore()

      caption('INBOX', target.x, zone.y + zone.h + 18, pal.caption)
      caption('SENDERS', width * 0.16, height * 0.24 + height * 0.52 + 26, pal.caption)

      // connections + particles
      for (let i = particles.length - 1; i >= 0; i--) {
        const p = particles[i]
        if (!p) continue
        p.t += p.speed * (reduced ? 0 : 1)
        const n = nodes[p.from]
        if (!n) continue
        const midX = (n.x + target.x) / 2
        const midY = (n.y + target.y) / 2 + p.cp
        const x = bezier(n.x, midX, target.x, p.t)
        const y = bezier(n.y, midY, target.y, p.t)

        // faint trail line
        ctx!.save()
        ctx!.globalAlpha = 0.16 * (1 - p.t)
        ctx!.strokeStyle = pal.accent
        ctx!.lineWidth = 1
        ctx!.beginPath()
        ctx!.moveTo(n.x, n.y)
        ctx!.quadraticCurveTo(midX, midY, x, y)
        ctx!.stroke()
        ctx!.restore()

        // the message
        ctx!.save()
        ctx!.shadowBlur = 10
        ctx!.shadowColor = pal.accent
        ctx!.fillStyle = pal.accent
        ctx!.globalAlpha = Math.min(1, p.t * 3)
        ctx!.beginPath()
        ctx!.arc(x, y, 2.6, 0, Math.PI * 2)
        ctx!.fill()
        ctx!.restore()

        if (p.t >= 1) {
          particles.splice(i, 1)
          // arrival flash
          ctx!.save()
          ctx!.strokeStyle = OK
          ctx!.lineWidth = 2
          ctx!.beginPath()
          ctx!.arc(target.x, target.y, 8, 0, Math.PI * 2)
          ctx!.stroke()
          ctx!.restore()
        }
      }

      // sender mailboxes with warmth pulse
      for (const n of nodes) {
        const pulse = reduced ? 0.6 : 0.5 + 0.5 * Math.sin(elapsed / 700 + n.phase)
        const glow = n.warmth * pulse
        ctx!.save()
        ctx!.shadowBlur = 14 * glow
        ctx!.shadowColor = ORANGE
        ctx!.fillStyle = `rgba(255,122,26,${0.4 + 0.5 * glow})`
        ctx!.beginPath()
        ctx!.roundRect(n.x - 5, n.y - 5, 10, 10, 3)
        ctx!.fill()
        ctx!.restore()
      }
    }

    let raf = 0
    let start = 0
    let lastSpawn = 0
    function loop(ts: number) {
      if (!start) start = ts
      const elapsed = ts - start
      if (elapsed - lastSpawn > 780) {
        spawn()
        lastSpawn = elapsed
      }
      frame(elapsed)
      raf = requestAnimationFrame(loop)
    }

    layout()
    if (reduced) {
      // static frame: seed a few in-flight messages, paint once.
      for (let i = 0; i < 4; i++) particles.push({ from: i % 6, t: 0.2 + i * 0.2, speed: 0, cp: (i - 2) * 40 })
      frame(0)
    } else {
      raf = requestAnimationFrame(loop)
    }

    const ro = new ResizeObserver(() => {
      layout()
      if (reduced) frame(0)
    })
    ro.observe(canvas)

    return () => {
      cancelAnimationFrame(raf)
      ro.disconnect()
    }
  }, [])

  return <canvas ref={canvasRef} className="absolute inset-0 size-full" aria-hidden="true" />
}
