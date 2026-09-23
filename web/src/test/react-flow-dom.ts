/**
 * The browser APIs React Flow measures with, which jsdom doesn't implement —
 * the recipe from React Flow's own testing guide. Without them nodes never
 * report dimensions, edges (and so the edge add buttons) never render, and the
 * viewport transform throws.
 *
 * Call from a test file's `beforeAll`; each vitest file gets its own jsdom, so
 * nothing leaks into suites that don't render a canvas.
 */
export function installReactFlowDom(): void {
  class ResizeObserverStub {
    private readonly callback: ResizeObserverCallback
    constructor(callback: ResizeObserverCallback) {
      this.callback = callback
    }
    observe(target: Element) {
      this.callback([{ target, contentRect: target.getBoundingClientRect() } as ResizeObserverEntry], this)
    }
    unobserve() {}
    disconnect() {}
  }

  class DOMMatrixReadOnlyStub {
    m22: number
    constructor(transform?: string) {
      const scale = transform?.match(/scale\(([1-9.]+)\)/)?.[1]
      this.m22 = scale !== undefined ? Number(scale) : 1
    }
  }

  globalThis.ResizeObserver = ResizeObserverStub
  Object.defineProperty(globalThis, 'DOMMatrixReadOnly', { configurable: true, value: DOMMatrixReadOnlyStub })

  Object.defineProperties(HTMLElement.prototype, {
    offsetHeight: {
      configurable: true,
      get(this: HTMLElement) {
        return Number.parseFloat(this.style.height) || 1
      },
    },
    offsetWidth: {
      configurable: true,
      get(this: HTMLElement) {
        return Number.parseFloat(this.style.width) || 1
      },
    },
  })

  Object.defineProperty(SVGElement.prototype, 'getBBox', {
    configurable: true,
    value: () => ({ x: 0, y: 0, width: 0, height: 0 }),
  })
}
