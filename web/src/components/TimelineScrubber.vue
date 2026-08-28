<script setup lang="ts">
import { computed, ref } from 'vue'
import { formatTime } from '../utils/format'

interface Range {
  startMs: number
  endMs: number
}

export interface TimelinePip {
  id: string
  ms: number
  type: string
  label?: string
}

const props = defineProps<{
  fromMs: number
  toMs: number
  recorded: Range[]
  density: number[]
  events?: TimelinePip[]
  playheadMs: number | null
}>()

const emit = defineEmits<{
  (e: 'seek', epochMs: number): void
}>()

const trackEl = ref<HTMLElement | null>(null)
const dragging = ref(false)

const span = computed(() => Math.max(1, props.toMs - props.fromMs))

function pct(ms: number): number {
  return Math.min(100, Math.max(0, ((ms - props.fromMs) / span.value) * 100))
}

const segments = computed(() =>
  props.recorded
    .map((r) => {
      const left = pct(r.startMs)
      const right = pct(r.endMs)
      return { left, width: Math.max(0.3, right - left) }
    })
    .filter((s) => s.left < 100 && s.left + s.width > 0),
)

const playheadPct = computed(() => (props.playheadMs == null ? null : pct(props.playheadMs)))

const pips = computed(() =>
  (props.events ?? [])
    .map((e) => ({ ...e, left: pct(e.ms) }))
    .filter((e) => e.left >= 0 && e.left <= 100),
)

function pipTitle(p: TimelinePip): string {
  const what = p.type === 'ai' && p.label ? p.label : p.type
  return `${what} — ${formatTime(p.ms)}`
}

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

function msFromEvent(e: PointerEvent): number {
  const el = trackEl.value
  if (!el) return props.fromMs
  const rect = el.getBoundingClientRect()
  const ratio = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
  return props.fromMs + ratio * span.value
}

function onPointerDown(e: PointerEvent) {
  dragging.value = true
  trackEl.value?.setPointerCapture(e.pointerId)
  emit('seek', msFromEvent(e))
}

function onPointerMove(e: PointerEvent) {
  if (dragging.value) emit('seek', msFromEvent(e))
}

function onPointerUp() {
  dragging.value = false
}
</script>

<template>
  <div class="scrubber">
    <div
      ref="trackEl"
      class="track"
      :class="{ dragging }"
      @pointerdown="onPointerDown"
      @pointermove="onPointerMove"
      @pointerup="onPointerUp"
      @pointercancel="onPointerUp"
    >
      <div class="density-strip">
        <div
          v-for="(v, i) in densityBars"
          :key="i"
          class="density-bar"
          :style="{ opacity: 0.15 + v * 0.85 }"
        ></div>
      </div>
      <div class="coverage">
        <div
          v-for="(s, i) in segments"
          :key="i"
          class="segment"
          :style="{ left: s.left + '%', width: s.width + '%' }"
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
        @click.stop="emit('seek', p.ms)"
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
.scrubber {
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

.track.dragging {
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
  opacity: 0.15;
}

.coverage {
  position: absolute;
  inset: 45% 0 0 0;
}

.segment {
  position: absolute;
  top: 0;
  bottom: 0;
  background: rgba(79, 140, 255, 0.55);
  border-top: 1px solid var(--accent);
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
