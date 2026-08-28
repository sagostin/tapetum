import { computed, type ComputedRef, type Ref, ref } from 'vue'

export interface ZoomPanOptions {
  minZoom?: number
  maxZoom?: number
}

export interface ZoomPanState {
  zoom: Ref<number>
  offsetX: Ref<number>
  offsetY: Ref<number>
  transform: ComputedRef<string>
  label: ComputedRef<string>
  isZoomed: ComputedRef<boolean>
  isPanning: ComputedRef<boolean>
  canZoomIn: ComputedRef<boolean>
  canZoomOut: ComputedRef<boolean>
  zoomAt: (
    cx: number,
    cy: number,
    containerW: number,
    containerH: number,
    factor: number,
  ) => void
  startPan: (clientX: number, clientY: number, container: HTMLElement | null) => void
  reset: () => void
}

/**
 * Shared digital-zoom / pan state for any surface that needs to magnify and
 * drag around its content (UI3-style). The composable is UI-agnostic; callers
 * apply the resulting CSS transform to whatever element wraps the content.
 */
export function useZoomPan(opts: ZoomPanOptions = {}): ZoomPanState {
  const minZoom = opts.minZoom ?? 1
  const maxZoom = opts.maxZoom ?? 8

  const zoom = ref(1)
  const offsetX = ref(0)
  const offsetY = ref(0)

  let panning = false
  let panOrigin = { x: 0, y: 0, offsetX: 0, offsetY: 0 }
  let activePointerId: number | null = null
  let activeContainer: HTMLElement | null = null

  function clamp(v: number, lo: number, hi: number) {
    return Math.min(hi, Math.max(lo, v))
  }

  /**
   * Apply a zoom factor anchored at (cx, cy) in container-local pixels. The
   * offset is adjusted so the point under the cursor stays under the cursor.
   */
  function zoomAt(
    cx: number,
    cy: number,
    containerW: number,
    containerH: number,
    factor: number,
  ) {
    const newZoom = clamp(zoom.value * factor, minZoom, maxZoom)
    if (newZoom === zoom.value) return
    // screenX = offsetX + cx * zoom. To keep screenX constant across the
    // change: offsetX_new = offsetX + cx*(zoom - newZoom)
    const dz = zoom.value - newZoom
    offsetX.value += cx * dz
    offsetY.value += cy * dz
    zoom.value = newZoom
    clampOffset(containerW, containerH)
  }

  function clampOffset(containerW: number, containerH: number) {
    if (zoom.value <= 1) {
      offsetX.value = 0
      offsetY.value = 0
      return
    }
    const maxX = ((zoom.value - 1) * containerW) / 2
    const maxY = ((zoom.value - 1) * containerH) / 2
    offsetX.value = clamp(offsetX.value, -maxX, maxX)
    offsetY.value = clamp(offsetY.value, -maxY, maxY)
  }

  function startPan(clientX: number, clientY: number, container: HTMLElement | null) {
    if (zoom.value <= 1) return
    panning = true
    panOrigin = { x: clientX, y: clientY, offsetX: offsetX.value, offsetY: offsetY.value }
    // Grab the most recent pointerId from the triggering event via the
    // shared PointerEvent prototype — we don't have it on the args list.
    activePointerId = (window.event as PointerEvent | undefined)?.pointerId ?? null
    activeContainer = container
    container?.setPointerCapture?.(activePointerId ?? 0)
    window.addEventListener('pointermove', onPanMove)
    window.addEventListener('pointerup', onPanEnd, { once: true })
    window.addEventListener('pointercancel', onPanEnd, { once: true })
  }

  function onPanMove(e: PointerEvent) {
    if (!panning) return
    offsetX.value = panOrigin.offsetX + (e.clientX - panOrigin.x)
    offsetY.value = panOrigin.offsetY + (e.clientY - panOrigin.y)
    const rect = activeContainer?.getBoundingClientRect()
    if (rect) clampOffset(rect.width, rect.height)
  }

  function onPanEnd() {
    panning = false
    activeContainer?.releasePointerCapture?.(activePointerId ?? 0)
    activePointerId = null
    activeContainer = null
    window.removeEventListener('pointermove', onPanMove)
  }

  function reset() {
    zoom.value = 1
    offsetX.value = 0
    offsetY.value = 0
  }

  const transform = computed(
    () => `translate(${offsetX.value}px, ${offsetY.value}px) scale(${zoom.value})`,
  )
  const isZoomed = computed(() => zoom.value > 1.0001)
  const isPanning = computed(() => panning)
  const canZoomIn = computed(() => zoom.value < maxZoom - 0.001)
  const canZoomOut = computed(() => zoom.value > minZoom + 0.001)
  const label = computed(() => {
    const z = zoom.value
    return z < 1.05 ? 'Fit' : `${z.toFixed(z < 2 ? 2 : 1)}x`
  })

  return {
    zoom,
    offsetX,
    offsetY,
    transform,
    label,
    isZoomed,
    isPanning,
    canZoomIn,
    canZoomOut,
    zoomAt,
    startPan,
    reset,
  }
}
