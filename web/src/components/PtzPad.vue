<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { get, post, del, ApiError } from '../api/client'
import type { PtzMovePayload, PtzPreset, PtzPresetsResponse } from '../api/types'

const props = defineProps<{
  cameraId: string
}>()

const MOVE_TIMEOUT_MS = 1000
const TAP_MS = 400

interface Direction {
  label: string
  pan: number
  tilt: number
}

const directions: Direction[] = [
  { label: '↖', pan: -0.7, tilt: 0.7 },
  { label: '↑', pan: 0, tilt: 1 },
  { label: '↗', pan: 0.7, tilt: 0.7 },
  { label: '←', pan: -1, tilt: 0 },
  { label: '→', pan: 1, tilt: 0 },
  { label: '↙', pan: -0.7, tilt: -0.7 },
  { label: '↓', pan: 0, tilt: -1 },
  { label: '↘', pan: 0.7, tilt: -0.7 },
]

const error = ref('')
const presets = ref<PtzPreset[]>([])
const presetName = ref('')
const savingPreset = ref(false)
const actingPreset = ref('')

let stopTimer: ReturnType<typeof setTimeout> | null = null
let downAt = 0

function move(pan: number, tilt: number, zoom: number) {
  error.value = ''
  if (stopTimer) {
    clearTimeout(stopTimer)
    stopTimer = null
  }
  downAt = Date.now()
  const payload: PtzMovePayload = { pan, tilt, zoom, timeout_ms: MOVE_TIMEOUT_MS }
  post(`/cameras/${props.cameraId}/ptz/move`, payload).catch((err) => {
    error.value = err instanceof ApiError ? err.message : 'PTZ move failed'
  })
}

function release() {
  // Guarantee the camera moves at least TAP_MS so a single tap works.
  const elapsed = Date.now() - downAt
  const delay = Math.max(0, TAP_MS - elapsed)
  if (stopTimer) clearTimeout(stopTimer)
  stopTimer = setTimeout(() => {
    stopTimer = null
    post(`/cameras/${props.cameraId}/ptz/stop`).catch(() => {})
  }, delay)
}

function stopNow() {
  if (stopTimer) {
    clearTimeout(stopTimer)
    stopTimer = null
  }
  post(`/cameras/${props.cameraId}/ptz/stop`).catch((err) => {
    error.value = err instanceof ApiError ? err.message : 'PTZ stop failed'
  })
}

async function fetchPresets() {
  try {
    const res = await get<PtzPresetsResponse>(`/cameras/${props.cameraId}/ptz/presets`)
    presets.value = res.presets ?? []
  } catch {
    // Presets unavailable — leave the list empty.
  }
}

async function gotoPreset(p: PtzPreset) {
  error.value = ''
  actingPreset.value = p.token
  try {
    await post(`/cameras/${props.cameraId}/ptz/presets/${p.token}/goto`)
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : 'Goto preset failed'
  } finally {
    actingPreset.value = ''
  }
}

async function savePreset() {
  const name = presetName.value.trim()
  if (!name) return
  error.value = ''
  savingPreset.value = true
  try {
    await post(`/cameras/${props.cameraId}/ptz/presets`, { name })
    presetName.value = ''
    await fetchPresets()
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : 'Save preset failed'
  } finally {
    savingPreset.value = false
  }
}

async function deletePreset(p: PtzPreset) {
  error.value = ''
  try {
    await del(`/cameras/${props.cameraId}/ptz/presets/${p.token}`)
    await fetchPresets()
  } catch (err) {
    error.value = err instanceof ApiError ? err.message : 'Delete preset failed'
  }
}

onMounted(fetchPresets)

onBeforeUnmount(() => {
  if (stopTimer) clearTimeout(stopTimer)
})
</script>

<template>
  <div class="ptz">
    <div class="ptz-pad-row">
      <div class="ptz-pad">
        <button
          v-for="d in directions"
          :key="d.label"
          class="ptz-btn"
          :class="{ 'ptz-mid': d.pan === 0 && d.tilt === 0 }"
          type="button"
          @pointerdown.prevent="move(d.pan, d.tilt, 0)"
          @pointerup.prevent="release"
          @pointerleave="release"
          @pointercancel="release"
          @contextmenu.prevent
        >
          {{ d.label }}
        </button>
        <button class="ptz-btn ptz-stop" type="button" title="Stop" @click="stopNow">■</button>
      </div>
      <div class="ptz-zoom">
        <button
          class="ptz-btn"
          type="button"
          title="Zoom in"
          @pointerdown.prevent="move(0, 0, 1)"
          @pointerup.prevent="release"
          @pointerleave="release"
          @pointercancel="release"
          @contextmenu.prevent
        >
          +
        </button>
        <button
          class="ptz-btn"
          type="button"
          title="Zoom out"
          @pointerdown.prevent="move(0, 0, -1)"
          @pointerup.prevent="release"
          @pointerleave="release"
          @pointercancel="release"
          @contextmenu.prevent
        >
          −
        </button>
      </div>
    </div>

    <p v-if="error" class="error-text">{{ error }}</p>

    <div class="ptz-presets">
      <h3 class="ptz-subtitle">Presets</h3>
      <div v-if="presets.length" class="preset-list">
        <div v-for="p in presets" :key="p.token" class="preset-row">
          <button
            class="btn btn-ghost btn-sm preset-goto"
            type="button"
            :disabled="actingPreset === p.token"
            @click="gotoPreset(p)"
          >
            {{ p.name || p.token }}
          </button>
          <button
            class="btn btn-ghost btn-sm preset-del"
            type="button"
            title="Delete preset"
            @click="deletePreset(p)"
          >
            ×
          </button>
        </div>
      </div>
      <p v-else class="muted">No presets saved.</p>
      <div class="preset-save">
        <input v-model="presetName" type="text" maxlength="64" placeholder="Preset name" />
        <button
          class="btn btn-ghost btn-sm"
          type="button"
          :disabled="savingPreset || !presetName.trim()"
          @click="savePreset"
        >
          {{ savingPreset ? 'Saving…' : 'Save current' }}
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ptz {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

.ptz-pad-row {
  display: flex;
  align-items: center;
  gap: 1rem;
}

.ptz-pad {
  display: grid;
  grid-template-columns: repeat(4, 44px);
  grid-template-rows: repeat(2, 44px);
  gap: 6px;
}

.ptz-btn {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 44px;
  height: 44px;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg-elevated);
  color: var(--text);
  font-size: 1.05rem;
  cursor: pointer;
  user-select: none;
  touch-action: none;
  transition: background 0.1s ease, border-color 0.1s ease;
}

.ptz-btn:hover {
  border-color: var(--text-muted);
}

.ptz-btn:active {
  background: var(--accent);
  border-color: var(--accent);
  color: #fff;
}

.ptz-stop {
  color: var(--danger);
}

.ptz-stop:active {
  background: var(--danger);
  border-color: var(--danger);
  color: #fff;
}

.ptz-zoom {
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.ptz-subtitle {
  margin: 0 0 0.5rem;
  font-size: 0.85rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
}

.preset-list {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  margin-bottom: 0.6rem;
  max-height: 180px;
  overflow-y: auto;
}

.preset-row {
  display: flex;
  gap: 0.35rem;
}

.preset-goto {
  flex: 1;
  text-align: left;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.preset-del {
  color: var(--danger);
}

.preset-save {
  display: flex;
  gap: 0.5rem;
}

.preset-save input {
  flex: 1;
  padding: 0.45rem 0.7rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 0.9rem;
  font-family: inherit;
  outline: none;
}

.preset-save input:focus {
  border-color: var(--accent);
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
}

.muted {
  color: var(--text-muted);
  font-size: 0.9rem;
  margin: 0 0 0.6rem;
}
</style>
