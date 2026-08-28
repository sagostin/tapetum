<script setup lang="ts">
import { computed, ref } from 'vue'
import type { MotionZone } from '../api/types'

// ZoneEditor draws motion zones (normalized polygons) over a camera snapshot.
// Click to add points, double-click or click the first point to close.
const props = defineProps<{
  cameraId: string
  zones: MotionZone[]
}>()

const emit = defineEmits<{
  (e: 'update:zones', zones: MotionZone[]): void
}>()

const drawing = ref<[number, number][]>([])
const newName = ref('')
const newMode = ref<'include' | 'exclude'>('include')
const stageEl = ref<HTMLElement | null>(null)

const snapshotUrl = computed(() => `/api/v1/cameras/${props.cameraId}/snapshot`)

function pointsAttr(poly: [number, number][]): string {
  return poly.map(([x, y]) => `${x * 100},${y * 100}`).join(' ')
}

function onClick(e: MouseEvent) {
  const el = stageEl.value
  if (!el) return
  const rect = el.getBoundingClientRect()
  const x = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
  const y = Math.min(1, Math.max(0, (e.clientY - rect.top) / rect.height))
  // click near the first point closes the polygon
  if (drawing.value.length >= 3) {
    const [fx, fy] = drawing.value[0]
    const dx = (fx - x) * rect.width
    const dy = (fy - y) * rect.height
    if (Math.hypot(dx, dy) < 14) {
      closeZone()
      return
    }
  }
  drawing.value = [...drawing.value, [x, y]]
}

function onDblClick() {
  if (drawing.value.length >= 3) closeZone()
}

function closeZone() {
  const zone: MotionZone = {
    name: newName.value.trim() || `Zone ${props.zones.length + 1}`,
    polygon: drawing.value,
    mode: newMode.value,
  }
  emit('update:zones', [...props.zones, zone])
  drawing.value = []
  newName.value = ''
}

function cancelDrawing() {
  drawing.value = []
}

function removeZone(i: number) {
  emit('update:zones', props.zones.filter((_, j) => j !== i))
}
</script>

<template>
  <div class="zone-editor">
    <div
      ref="stageEl"
      class="stage"
      @click="onClick"
      @dblclick.prevent="onDblClick"
    >
      <img :src="snapshotUrl" class="stage-img" alt="camera snapshot" draggable="false" />
      <svg class="overlay" viewBox="0 0 100 100" preserveAspectRatio="none">
        <polygon
          v-for="(z, i) in zones"
          :key="i"
          :points="pointsAttr(z.polygon)"
          :class="z.mode === 'exclude' ? 'zone-exclude' : 'zone-include'"
        />
        <polyline
          v-if="drawing.length"
          :points="pointsAttr(drawing)"
          class="zone-drawing"
        />
        <circle
          v-for="(p, i) in drawing"
          :key="`p${i}`"
          :cx="p[0] * 100"
          :cy="p[1] * 100"
          r="1.4"
          class="zone-point"
        />
      </svg>
    </div>

    <div class="zone-controls">
      <input v-model="newName" type="text" placeholder="Zone name" class="zone-name" />
      <select v-model="newMode">
        <option value="include">include</option>
        <option value="exclude">exclude</option>
      </select>
      <button
        v-if="drawing.length >= 3"
        class="btn btn-ghost btn-sm"
        type="button"
        @click="closeZone"
      >
        Close zone
      </button>
      <button v-if="drawing.length" class="btn btn-ghost btn-sm" type="button" @click="cancelDrawing">
        Cancel
      </button>
      <span class="hint">Click the image to add points; double-click to finish a zone.</span>
    </div>

    <ul v-if="zones.length" class="zone-list">
      <li v-for="(z, i) in zones" :key="i">
        <span class="zone-chip" :class="z.mode === 'exclude' ? 'zone-exclude-text' : 'zone-include-text'">
          {{ z.mode }}
        </span>
        {{ z.name }}
        <button class="btn btn-ghost btn-sm btn-danger" type="button" @click="removeZone(i)">✕</button>
      </li>
    </ul>
  </div>
</template>

<style scoped>
.stage {
  position: relative;
  cursor: crosshair;
  border-radius: 6px;
  overflow: hidden;
  background: #000;
}

.stage-img {
  width: 100%;
  display: block;
  user-select: none;
}

.overlay {
  position: absolute;
  inset: 0;
  width: 100%;
  height: 100%;
}

.zone-include {
  fill: rgba(74, 222, 128, 0.25);
  stroke: #4ade80;
  stroke-width: 0.4;
}

.zone-exclude {
  fill: rgba(239, 68, 68, 0.25);
  stroke: #ef4444;
  stroke-width: 0.4;
}

.zone-drawing {
  fill: none;
  stroke: var(--accent);
  stroke-width: 0.4;
  stroke-dasharray: 1 0.6;
}

.zone-point {
  fill: var(--accent);
}

.zone-controls {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-top: 0.6rem;
  flex-wrap: wrap;
}

.zone-name {
  width: 140px;
  padding: 0.45rem 0.6rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 0.88rem;
  font-family: inherit;
}

.zone-controls select {
  padding: 0.45rem 0.6rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 0.88rem;
  font-family: inherit;
}

.hint {
  font-size: 0.78rem;
  color: var(--text-muted);
}

.zone-list {
  list-style: none;
  margin: 0.6rem 0 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  font-size: 0.88rem;
}

.zone-list li {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.zone-chip {
  padding: 0.1rem 0.5rem;
  border-radius: 999px;
  font-size: 0.72rem;
  font-weight: 600;
}

.zone-include-text {
  color: #4ade80;
}

.zone-exclude-text {
  color: #ef4444;
}

.btn-sm {
  padding: 0.3rem 0.6rem;
  font-size: 0.82rem;
  width: auto;
}

.btn-danger {
  color: var(--danger);
}
</style>
