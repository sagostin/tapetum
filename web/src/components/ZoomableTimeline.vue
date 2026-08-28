<script setup lang="ts">
import { computed, ref } from 'vue'
import { formatTime } from '../utils/format'

/**
 * UI3-style zoomable timeline scrubber.
 *
 * Renders a horizontal track of recorded segments (color-coded by `color`
 * per segment when provided), event pips, density strip (optional), and a
 * playhead.
 *
 * Interactions:
 *   - Click: emit `seek(timeMs)` (or `select(timeMs, camId?)`).
 *   - Drag: pan the window; emits `window-change(from, to)` as the user
 *           drags so the parent can refetch timelines at the new range.
 *   - Wheel: zoom around the cursor; emits `window-change(from, to)`.
 *   - Double-click: `open(timeMs, camId?)` (navigate to dedicated view).
 *   - Pip click: `select(timeMs, camId)`.
 *
 * `camId` is optional and only matters for the multi-camera (overlay)
 * variant where a single timeline row aggregates all cameras; the parent
 * uses it to figure out which camera to load when the user clicks.
 */

export interface TimelineRange {
  start: string | number | Date
  end: string | number | Date
  camId?: string
}

export interface TimelineEvent {
  id: string
  ts: string | number | Date
  type: string
  label?: string
  camId?: string
}

const props = withDefaults(
  defineProps<{
    fromMs: number
    toMs: number
    recorded?: TimelineRange[]
    events?: TimelineEvent[]
    density?: number[]
    playheadMs?: number | null
    /** Aggregate (multi-cam) mode: a single row that shows segments from
     * all cameras with per-segment color. When false, all segments render
     * the default color. */
    aggregate?: boolean
    /** When true, emit `window-change` on drag (pan mode). */
    pan?: boolean
    /** When true, emit `window-change` on wheel (zoom mode). */
    zoomable?: boolean
    /** Min/max span (ms) for wheel zoom. */
    minSpanMs?: number
    maxSpanMs?: number
  }>(),
  {
    recorded: () => [],
    events: () => [],
    density: () => [],
    playheadMs: null,
    aggregate: false,
    pan: true,
    zoomable: true,
    minSpanMs: 15 * 60_000,
    maxSpanMs: 7 * 86_400_000,
  },
)

const emit = defineEmits<{
  (e: 'seek', timeMs: number): void
  (e: 'select', timeMs: number, camId?: string): void
  (e: 'open', timeMs: number, camId?: string): void
  (e: 'window-change', fromMs: number, toMs: number): void
}>()

const trackEl = ref<HTMLElement | null>(null)
const dragging = ref(false)
const panning = ref(false)
const panMoved = ref(false)
const panStartX = ref(0)
const panStartView = ref({ from: 0, to: 0 })

const span = computed(() => Math.max(1, props.toMs - props.fromMs))

function pct(ms: number): number {
  return Math.min(100, Math.max(0, ((ms - props.fromMs) / span.value) * 100))
}

function eventMs(e: TimelineEvent): number {
  return new Date(e.ts).getTime()
}
function rangeStartMs(r: TimelineRange): number {
  return new Date(r.start).getTime()
}
function rangeEndMs(r: TimelineRange): number {
  return new Date(r.end).getTime()
}

function hueFor(camId: string): number {
  let hash = 0
  for (let i = 0; i < camId.length; i++) {
    hash = (hash * 31 + camId.charCodeAt(i)) >>> 0
  }
  return hash % 360
}

const segments = computed(() => {
  return props.recorded
    .map((r) => {
      const start = rangeStartMs(r)
      const end = rangeEndMs(r)
      const left = pct(start)
      const right = pct(end)
      return {
        left,
        width: Math.max(0.3, right - left),
        camId: r.camId,
      }
    })
    .filter((s) => s.left < 100 && s.left + s.width > 0)
})

const pips = computed(() =>
  props.events
    .map((e) => ({ ...e, ms: eventMs(e), left: pct(eventMs(e)) }))
    .filter((e) => e.left >= 0 && e.left <= 100),
)

const playheadPct = computed(() =>
  props.playheadMs == null ? null : pct(props.playheadMs),
)

const densityBars = computed(() => {
  if (!props.density.length) return []
  const max = Math.max(...props.density, 0)
  return props.density.map((d) => (max > 0 ? d / max : 0))
})

const NICE_INTERVALS = [
  60_000,
  5 * 60_000,
  10 * 60_000,
  15 * 60_000,
  30 * 60_000,
  3_600_000,
  2 * 3_600_000,
  4 * 3_600_000,
  6 * 3_600_000,
  12 * 3_600_000,
  86_400_000,
]

const ticks = computed(() => {
  const target = span.value / 6
  const interval = NICE_INTERVALS.find((i) => i >= target) ?? 86_400_000
  const showDate = span.value > 86_400_000
  const result: { ms: number; label: string; left: number }[] = []
  const first = Math.ceil(props.fromMs / interval) * interval
  for (let t = first; t <= props.toMs; t += interval) {
    const d = new Date(t)
    const label = showDate
      ? `${d.getMonth() + 1}/${d.getDate()} ${formatTime(t).slice(0, 5)}`
      : formatTime(t).slice(0, 5)
    result.push({ ms: t, label, left: pct(t) })
  }
  return result
})

function pipTitle(p: TimelineEvent): string {
  const what = p.type === 'ai' && p.label ? p.label : p.type
  return `${what} — ${formatTime(eventMs(p))}`
}

function msFromEvent(e: PointerEvent): number {
  const el = trackEl.value
  if (!el) return props.fromMs
  const rect = el.getBoundingClientRect()
  const ratio = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
  return props.fromMs + ratio * span.value
}

function findCamAt(ms: number): string | null {
  for (const s of segments.value) {
    const startMs = props.fromMs + (s.left / 100) * span.value
    const endMs = props.fromMs + ((s.left + s.width) / 100) * span.value
    if (ms >= startMs && ms <= endMs && s.camId) return s.camId
  }
  return null
}

function onPointerDown(e: PointerEvent) {
  dragging.value = true
  panning.value = false
  panMoved.value = false
  panStartX.value = e.clientX
  panStartView.value = { from: props.fromMs, to: props.toMs }
  trackEl.value?.setPointerCapture(e.pointerId)
}

function onPointerMove(e: PointerEvent) {
  if (!dragging.value) return
  const dxPx = e.clientX - panStartX.value
  if (Math.abs(dxPx) > 3) {
    panning.value = true
    panMoved.value = true
  }
  if (panning.value && props.pan) {
    const el = trackEl.value
    if (!el) return
    const rect = el.getBoundingClientRect()
    const ratio = dxPx / rect.width
    const newFrom = panStartView.value.from - ratio * span.value
    const newTo = panStartView.value.to - ratio * span.value
    emit('window-change', newFrom, newTo)
  }
}

function onPointerUp(e: PointerEvent) {
  dragging.value = false
  const wasPanning = panning.value
  panning.value = false
  if (wasPanning) return
  // Pure click — pick a time, find the camera it belongs to (if any), emit.
  const ms = msFromEvent(e)
  const camId = findCamAt(ms) ?? undefined
  emit('seek', ms)
  emit('select', ms, camId)
}

function onPointerCancel() {
  dragging.value = false
  panning.value = false
}

function onDoubleClick(e: MouseEvent) {
  const ms = msFromEvent(e as unknown as PointerEvent)
  const camId = findCamAt(ms) ?? undefined
  emit('open', ms, camId)
}

function onWheel(e: WheelEvent) {
  if (!props.zoomable) return
  e.preventDefault()
  const el = trackEl.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const ratio = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
  const anchorMs = props.fromMs + ratio * span.value
  const factor = e.deltaY < 0 ? 0.7 : 1 / 0.7
  const newSpan = Math.min(
    props.maxSpanMs,
    Math.max(props.minSpanMs, span.value * factor),
  )
  const newFrom = anchorMs - ratio * newSpan
  const newTo = anchorMs + (1 - ratio) * newSpan
  emit('window-change', newFrom, newTo)
}
</script>

<template>
  <div class="zoomable-timeline">
    <div
      ref="trackEl"
      class="track"
      :class="{ dragging, panning }"
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerCancel"
      @dblclick="onDoubleClick"
      @wheel="onWheel"
    >
      <div v-if="densityBars.length" class="density-strip">
        <div
          v-for="(v, i) in densityBars"
          :key="`d${i}`"
          class="density-bar"
          :style="{ opacity: 0.12 + v * 0.55 }"
        ></div>
      </div>
      <div class="coverage">
        <div
          v-for="(s, i) in segments"
          :key="`s${i}`"
          class="segment"
          :style="{
            left: s.left + '%',
            width: s.width + '%',
            background: aggregate && s.camId
              ? `hsl(${hueFor(s.camId)} 70% 55% / 0.55)`
              : 'rgba(79, 140, 255, 0.55)',
          }"
        ></div>
      </div>
      <button
        v-for="p in pips"
        :key="p.id"
        type="button"
        class="pip"
        :class="{ 'pip-ai': p.type === 'ai' }"
        :style="{ left: p.left + '%' }"
        :title="pipTitle(p)"
        @pointerdown.stop
        @click.stop="emit('select', p.ms, p.camId)"
      ></button>
      <div v-if="playheadPct != null" class="playhead" :style="{ left: playheadPct + '%' }"></div>
    </div>
    <div class="ruler">
      <span
        v-for="t in ticks"
        :key="t.ms"
        class="tick"
        :style="{ left: t.left + '%' }"
      >{{ t.label }}</span>
    </div>
  </div>
</template>

<style scoped>
.zoomable-timeline {
  user-select: none;
}

.track {
  position: relative;
  height: 44px;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
  cursor: crosshair;
  touch-action: none;
}

.track.dragging:not(.panning) {
  cursor: grabbing;
}

.track.panning {
  cursor: grabbing;
}

.density-strip {
  position: absolute;
  inset: 0 0 60% 0;
  display: flex;
}

.density-bar {
  flex: 1;
  background: var(--accent);
  opacity: 0.12;
}

.coverage {
  position: absolute;
  inset: 45% 0 0 0;
}

.segment {
  position: absolute;
  top: 0;
  bottom: 0;
  border-radius: 2px;
}

.pip {
  position: absolute;
  top: 2px;
  width: 8px;
  height: 8px;
  margin-left: -4px;
  padding: 0;
  border: none;
  border-radius: 50%;
  background: #f59e0b;
  cursor: pointer;
  z-index: 2;
}

.pip-ai {
  background: #ef4444;
}

.playhead {
  position: absolute;
  top: 0;
  bottom: 0;
  width: 2px;
  margin-left: -1px;
  background: #fff;
  box-shadow: 0 0 6px rgba(255, 255, 255, 0.6);
  pointer-events: none;
}

.ruler {
  position: relative;
  height: 1.2rem;
  margin-top: 0.25rem;
}

.tick {
  position: absolute;
  transform: translateX(-50%);
  font-size: 0.72rem;
  font-family: 'SF Mono', 'Menlo', monospace;
  color: var(--text-muted);
  white-space: nowrap;
}
</style>
