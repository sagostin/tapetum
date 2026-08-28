<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { get, post, put, ApiError } from '../api/client'
import type {
  Camera,
  CameraStats,
  ImagingSettings,
  OnvifSyncResponse,
} from '../api/types'
import { useAuthStore } from '../stores/auth'
import { useCamerasStore } from '../stores/cameras'
import { formatBytes, formatDuration } from '../utils/format'
import StatusBadge from '../components/StatusBadge.vue'
import LivePlayer from '../components/LivePlayer.vue'
import CameraStage from '../components/CameraStage.vue'
import PtzPad from '../components/PtzPad.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const cams = useCamerasStore()
const cameraId = computed(() => route.params.id as string)

const canPlayback = computed(() => auth.user?.permissions.includes('playback') ?? false)
const canPtz = computed(() => auth.user?.permissions.includes('ptz') ?? false)
const canWrite = computed(() => auth.user?.permissions.includes('cameras:write') ?? false)

// Camera + stats: fetched on mount and when the route changes. The camera
// store is the source of truth for camera metadata (it gets WS-driven status
// updates); this view only fetches detail bits not in the store. Stats poll
// is throttled to 15s since bitrate/fps aren't on the WS yet — once they
// are, drop the poll entirely.
const camera = ref<Camera | null>(null)
const stats = ref<CameraStats | null>(null)
const allCameras = computed<Camera[]>(() => cams.list)
const loadError = ref('')
const streamSource = ref<'main' | 'sub'>('main')

// ---- Cycle ----------------------------------------------------------------
// Auto-advance through the camera list every N seconds. The cycling lives
// here (not on the dashboard wall) because the dashboard shows every camera
// at once — there's nothing to cycle through — whereas this detail view is
// a single-pane view that benefits from a screensaver-like rotation.
const cycleEnabled = ref(false)
const cycleSeconds = 10
let cycleTimer: ReturnType<typeof setInterval> | null = null

let pollTimer: ReturnType<typeof setInterval> | null = null

const status = computed(() => {
  const fromWs = cams.byId(cameraId.value)?.status
  return stats.value?.status ?? fromWs ?? camera.value?.status ?? 'offline'
})

const showPtz = computed(() => canPtz.value && camera.value?.has_ptz === true)
const showImaging = computed(() => canPtz.value && !!camera.value?.onvif_endpoint)
const showSync = computed(() => canWrite.value && !!camera.value?.onvif_endpoint)

// ---- ONVIF sync ----
const syncing = ref(false)
const syncError = ref('')
const syncSummary = ref('')

// ---- Imaging ----
const imagingOpen = ref(false)
const imagingLoading = ref(false)
const imagingError = ref('')
const imagingSaved = ref(false)
const imaging = reactive<ImagingSettings>({})
let imagingOriginal: ImagingSettings = {}

const NUMERIC_IMAGING_FIELDS = [
  { key: 'brightness', label: 'Brightness' },
  { key: 'contrast', label: 'Contrast' },
  { key: 'color_saturation', label: 'Saturation' },
  { key: 'sharpness', label: 'Sharpness' },
  { key: 'wdr_level', label: 'WDR level' },
] as const

async function refresh() {
  const id = cameraId.value
  try {
    const [cam, st] = await Promise.all([
      get<Camera>(`/cameras/${id}`),
      get<CameraStats>(`/cameras/${id}/stats`),
    ])
    camera.value = cam
    stats.value = st
    loadError.value = ''
  } catch {
    if (!camera.value) loadError.value = 'Failed to load camera'
  }
}

async function ensureCamerasLoaded() {
  if (!cams.loaded) await cams.refresh()
}

const currentIndex = computed(() =>
  allCameras.value.findIndex((c) => c.id === cameraId.value),
)
const previousCamera = computed(() => {
  if (currentIndex.value <= 0) return null
  return allCameras.value[currentIndex.value - 1] ?? null
})
const nextCamera = computed(() => {
  if (currentIndex.value < 0) return null
  return allCameras.value[currentIndex.value + 1] ?? null
})

function gotoCamera(id: string | null | undefined) {
  if (!id || id === cameraId.value) return
  router.push(`/cameras/${id}`)
}

function cycleCamera(direction: -1 | 1) {
  const target = direction === -1 ? previousCamera.value : nextCamera.value
  gotoCamera(target?.id)
}

function toggleCycle() {
  cycleEnabled.value = !cycleEnabled.value
}

function setStreamSource(s: 'main' | 'sub') {
  streamSource.value = s
}

function syncCycle() {
  if (cycleEnabled.value) {
    if (cycleTimer) return
    cycleTimer = setInterval(() => cycleCamera(1), cycleSeconds * 1000)
  } else if (cycleTimer) {
    clearInterval(cycleTimer)
    cycleTimer = null
  }
}

// Keyboard navigation: ←/→ step, Ctrl/Cmd+C toggles cycle.
function onKeyDown(e: KeyboardEvent) {
  const tag = (e.target as HTMLElement | null)?.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA') return
  if (e.key === 'ArrowLeft') cycleCamera(-1)
  else if (e.key === 'ArrowRight') cycleCamera(1)
  else if ((e.key === 'c' || e.key === 'C') && (e.ctrlKey || e.metaKey)) {
    e.preventDefault()
    toggleCycle()
  }
}

async function syncOnvif() {
  syncing.value = true
  syncError.value = ''
  syncSummary.value = ''
  try {
    const res = await post<OnvifSyncResponse>(`/cameras/${cameraId.value}/onvif/sync`)
    camera.value = res.camera
    const profiles = res.probe?.profiles?.length ?? 0
    syncSummary.value = `${profiles} profile${profiles === 1 ? '' : 's'} found · PTZ: ${
      res.probe?.has_ptz ? 'yes' : 'no'
    }`
  } catch (err) {
    syncError.value = err instanceof ApiError ? err.message : 'ONVIF sync failed'
  } finally {
    syncing.value = false
  }
}

async function toggleImaging() {
  imagingOpen.value = !imagingOpen.value
  if (!imagingOpen.value) return
  imagingLoading.value = true
  imagingError.value = ''
  imagingSaved.value = false
  try {
    const res = await get<ImagingSettings>(`/cameras/${cameraId.value}/imaging`)
    for (const key of Object.keys(imaging) as (keyof ImagingSettings)[]) {
      delete imaging[key]
    }
    Object.assign(imaging, res)
    imagingOriginal = { ...res }
  } catch (err) {
    imagingError.value = err instanceof ApiError ? err.message : 'Failed to load imaging settings'
  } finally {
    imagingLoading.value = false
  }
}

async function applyImaging() {
  imagingError.value = ''
  imagingSaved.value = false
  const changed: ImagingSettings = {}
  for (const key of Object.keys(imaging) as (keyof ImagingSettings)[]) {
    if (imaging[key] !== imagingOriginal[key]) {
      ;(changed as Record<string, unknown>)[key] = imaging[key]
    }
  }
  if (Object.keys(changed).length === 0) return
  try {
    await put(`/cameras/${cameraId.value}/imaging`, changed)
    imagingOriginal = { ...imaging }
    imagingSaved.value = true
  } catch (err) {
    imagingError.value = err instanceof ApiError ? err.message : 'Failed to apply imaging settings'
  }
}

onMounted(() => {
  ensureCamerasLoaded()
  refresh()
  // 15s stats poll — bitrate/fps aren't on the WS yet. The camera store
  // handles status transitions over WS, so this only refreshes the stats
  // panel (bitrate/fps/uptime).
  pollTimer = setInterval(refresh, 15_000)
  window.addEventListener('keydown', onKeyDown)
})

// Switching cameras via the rail: reset state and refetch.
watch(cameraId, (_, prev) => {
  if (prev && prev !== cameraId.value) {
    camera.value = null
    stats.value = null
    imagingOpen.value = false
    streamSource.value = 'main'
    refresh()
  }
})

// Re-arm the cycle interval whenever the toggle flips so the timer reflects
// the current value without waiting for the next tick.
watch(cycleEnabled, () => syncCycle())

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
  if (cycleTimer) clearInterval(cycleTimer)
  window.removeEventListener('keydown', onKeyDown)
})
</script>

<template>
  <div>
    <p v-if="loadError" class="error-text">{{ loadError }}</p>

    <template v-else-if="camera">
      <div class="detail-header">
        <div class="detail-title">
          <router-link to="/cameras" class="back-link">Cameras</router-link>
          <h1 class="page-title">{{ camera.name }}</h1>
          <StatusBadge :status="status" />
        </div>
        <div class="header-actions">
          <button
            v-if="showSync"
            class="btn btn-ghost"
            type="button"
            :disabled="syncing"
            @click="syncOnvif"
          >
            {{ syncing ? 'Syncing…' : 'Sync from ONVIF' }}
          </button>
          <router-link
            v-if="canPlayback"
            :to="`/playback/${cameraId}`"
            class="btn btn-primary btn-inline"
          >
            Playback
          </router-link>
        </div>
      </div>

      <p v-if="syncError" class="error-text">{{ syncError }}</p>
      <p v-if="syncSummary" class="sync-ok">ONVIF sync: {{ syncSummary }}</p>

      <div class="camera-layout">
        <aside class="camera-rail">
          <div class="rail-header">
            <span class="muted small">Cameras</span>
            <span class="muted small">{{ currentIndex + 1 }} / {{ allCameras.length }}</span>
          </div>
          <div class="rail-list">
            <button
              v-for="c in allCameras"
              :key="c.id"
              type="button"
              class="rail-item"
              :class="{ 'rail-item-active': c.id === cameraId, 'rail-item-offline': c.status === 'offline' }"
              :title="`${c.name} — ${c.status}`"
              @click.stop="gotoCamera(c.id)"
            >
              <div class="rail-thumb">
                <LivePlayer
                  v-if="c.enabled && c.status !== 'offline'"
                  :camera-id="c.id"
                  stream="sub"
                />
                <div v-else class="rail-placeholder">
                  <span>{{ c.enabled ? c.status : 'disabled' }}</span>
                </div>
              </div>
              <span class="rail-name">{{ c.name }}</span>
              <StatusBadge :status="c.status" compact />
            </button>
          </div>
        </aside>

        <div class="camera-main">
          <div class="player-wrap">
            <div class="player-frame">
              <CameraStage
                v-if="camera.enabled && camera.status !== 'offline'"
                mode="stage"
                :camera="camera"
                :stream="streamSource"
              />
              <div v-else class="player-placeholder">
                <span>{{ camera.enabled ? camera.status : 'disabled' }}</span>
              </div>
            </div>
          </div>

          <div class="player-info">
            <div class="player-info-left">
              <button
                class="btn btn-ghost btn-sm"
                type="button"
                :disabled="!previousCamera"
                @click="cycleCamera(-1)"
                title="Previous camera (←)"
              >←</button>
              <span class="player-cam-name">{{ camera.name }}</span>
              <button
                class="btn btn-ghost btn-sm"
                type="button"
                :disabled="!nextCamera"
                @click="cycleCamera(1)"
                title="Next camera (→)"
              >→</button>
            </div>
            <div class="player-info-right">
              <button
                class="cycle-btn"
                :class="{ 'cycle-btn-active': cycleEnabled }"
                type="button"
                @click="toggleCycle"
                :title="cycleEnabled ? `Stop auto-cycle (every ${cycleSeconds}s)` : `Auto-cycle every ${cycleSeconds}s (⌘/Ctrl+C)`"
                :disabled="allCameras.length < 2"
              >
                <span class="cycle-dot" :class="{ 'cycle-dot-on': cycleEnabled }" aria-hidden="true"></span>
                {{ cycleEnabled ? 'Cycle on' : 'Cycle' }}
              </button>
              <div class="stream-toggle" v-if="camera.enabled && camera.status !== 'offline'">
                <button
                  class="stream-btn"
                  :class="{ 'stream-btn-active': streamSource === 'sub' }"
                  type="button"
                  @click="setStreamSource('sub')"
                >Sub</button>
                <button
                  class="stream-btn"
                  :class="{ 'stream-btn-active': streamSource === 'main' }"
                  type="button"
                  @click="setStreamSource('main')"
                >Main</button>
              </div>
              <router-link
                v-if="canPlayback"
                :to="`/playback/${cameraId}`"
                class="btn btn-primary btn-inline btn-sm"
              >
                Open playback →
              </router-link>
            </div>
          </div>

          <div v-if="showPtz || showImaging" class="controls-grid">
        <div v-if="showPtz" class="card control-card">
          <h2 class="control-title">PTZ</h2>
          <PtzPad :camera-id="cameraId" />
        </div>

        <div v-if="showImaging" class="card control-card">
          <button class="imaging-toggle" type="button" @click="toggleImaging">
            <h2 class="control-title">Imaging</h2>
            <span class="imaging-caret">{{ imagingOpen ? '▾' : '▸' }}</span>
          </button>
          <template v-if="imagingOpen">
            <p v-if="imagingLoading" class="muted">Loading imaging settings…</p>
            <p v-else-if="imagingError" class="error-text">{{ imagingError }}</p>
            <template v-else>
              <div
                v-for="f in NUMERIC_IMAGING_FIELDS"
                :key="f.key"
                class="imaging-row"
              >
                <template v-if="imaging[f.key] !== undefined">
                  <label class="imaging-label">{{ f.label }}</label>
                  <input
                    v-model.number="imaging[f.key]"
                    type="range"
                    min="0"
                    max="100"
                    step="1"
                  />
                  <span class="imaging-value mono">{{ imaging[f.key] }}</span>
                </template>
              </div>
              <div v-if="imaging.wdr_enabled !== undefined" class="imaging-row">
                <label class="imaging-label">WDR</label>
                <input v-model="imaging.wdr_enabled" type="checkbox" />
              </div>
              <div v-if="imaging.ir_cut_filter !== undefined" class="imaging-row">
                <label class="imaging-label">IR cut filter</label>
                <select v-model="imaging.ir_cut_filter" class="imaging-select">
                  <option value="ON">ON</option>
                  <option value="OFF">OFF</option>
                  <option value="AUTO">AUTO</option>
                </select>
              </div>
              <div class="imaging-actions">
                <button class="btn btn-ghost btn-sm" type="button" @click="applyImaging">
                  Apply
                </button>
                <span v-if="imagingSaved" class="imaging-saved">Applied</span>
              </div>
            </template>
          </template>
        </div>
      </div>

      <div class="stats-grid">
        <div class="stat card">
          <span class="stat-label">Bitrate</span>
          <span class="stat-value">{{ stats && stats.bitrate_kbps != null ? `${stats.bitrate_kbps.toFixed(0)} kbps` : '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">FPS</span>
          <span class="stat-value">{{ stats && stats.fps != null ? stats.fps.toFixed(1) : '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">Uptime</span>
          <span class="stat-value">{{ stats && stats.uptime != null ? formatDuration(stats.uptime) : '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">Recorded</span>
          <span class="stat-value">{{ stats && stats.recorded_bytes != null ? formatBytes(stats.recorded_bytes) : '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">Codec</span>
          <span class="stat-value">{{ stats?.codec?.toUpperCase() || '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">Last frame</span>
          <span class="stat-value">
            {{ stats && stats.running && stats.last_frame_age_s != null ? `${stats.last_frame_age_s.toFixed(1)}s ago` : '—' }}
          </span>
        </div>
      </div>
        </div>
      </div>
    </template>

    <p v-else class="muted">Loading camera…</p>
  </div>
</template>

<style scoped>
.page-title {
  margin: 0;
  font-size: 1.3rem;
  font-weight: 600;
}

.muted {
  color: var(--text-muted);
}

.mono {
  font-family: 'SF Mono', 'Menlo', monospace;
}

.detail-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1.25rem;
}

.detail-title {
  display: flex;
  align-items: center;
  gap: 0.9rem;
}

.header-actions {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}

.back-link {
  color: var(--text-muted);
  text-decoration: none;
  font-size: 0.9rem;
}

.back-link:hover {
  color: var(--text);
}

.back-link::after {
  content: '/';
  margin-left: 0.9rem;
  color: var(--border);
}

.btn-inline {
  width: auto;
  text-decoration: none;
}

.sync-ok {
  color: #4ade80;
  font-size: 0.9rem;
  margin: 0 0 1rem;
}

.player-wrap {
  width: 100%;
  margin-bottom: 1rem;
}

.player-frame {
  position: relative;
  width: 100%;
  aspect-ratio: 16 / 9;
  background: #000;
  border-radius: var(--radius);
  overflow: hidden;
}

/* CameraStage fills .player-frame; no extra styling needed. */

.player-placeholder {
  width: 100%;
  aspect-ratio: 16 / 9;
  display: flex;
  align-items: center;
  justify-content: center;
  background: #000;
  border-radius: var(--radius);
  color: var(--text-muted);
  font-size: 1rem;
  text-transform: uppercase;
  letter-spacing: 0.1em;
}

.player-info {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1rem;
  flex-wrap: wrap;
}

.player-info-left {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.player-info-right {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.player-cam-name {
  font-weight: 600;
}

/* ---- Cycle button (auto-advance through camera list) ---- */

.cycle-btn {
  display: inline-flex;
  align-items: center;
  gap: 0.4rem;
  padding: 0.35rem 0.7rem;
  background: var(--bg-card);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 4px;
  font: inherit;
  font-size: 0.8rem;
  cursor: pointer;
  transition: border-color 0.12s ease, background 0.12s ease;
}

.cycle-btn:hover:not(:disabled) {
  border-color: var(--accent);
}

.cycle-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}

.cycle-btn-active {
  border-color: var(--accent);
  background: rgba(79, 140, 255, 0.12);
}

.cycle-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--text-muted);
}

.cycle-dot-on {
  background: #4ade80;
  box-shadow: 0 0 8px rgba(74, 222, 128, 0.6);
}

.stream-toggle {
  display: inline-flex;
  border: 1px solid var(--border);
  border-radius: 4px;
  overflow: hidden;
}

.stream-btn {
  background: transparent;
  color: var(--text-muted);
  border: none;
  padding: 0.35rem 0.75rem;
  font: inherit;
  font-size: 0.8rem;
  cursor: pointer;
}

.stream-btn:hover {
  color: var(--text);
}

.stream-btn-active {
  background: var(--bg-elevated);
  color: var(--text);
}

/* ---- UI3-style camera rail (sidebar) ---- */

.camera-layout {
  display: grid;
  grid-template-columns: 200px 1fr;
  gap: 1rem;
  margin-bottom: 1.5rem;
}

.camera-rail {
  position: sticky;
  top: 1rem;
  align-self: start;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  max-height: calc(100vh - 2rem);
}

.rail-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0 0.25rem;
}

.rail-list {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
  overflow-y: auto;
  padding-right: 0.2rem;
}

.rail-item {
  display: flex;
  flex-direction: column;
  gap: 0.35rem;
  padding: 0.35rem;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 6px;
  font: inherit;
  color: inherit;
  cursor: pointer;
  text-align: left;
  transition: border-color 0.12s ease, background 0.12s ease;
}

.rail-item:hover {
  border-color: var(--accent);
}

.rail-item-active {
  border-color: var(--accent);
  background: rgba(79, 140, 255, 0.12);
}

.rail-item-offline {
  opacity: 0.55;
}

.rail-thumb {
  position: relative;
  width: 100%;
  aspect-ratio: 16 / 9;
  background: #000;
  border-radius: 4px;
  overflow: hidden;
}

.rail-thumb :deep(.live-player),
.rail-thumb :deep(.live-media),
.rail-thumb :deep(video),
.rail-thumb :deep(img) {
  pointer-events: none;
}

.rail-thumb :deep(.live-player) {
  position: absolute;
  inset: 0;
}

.rail-thumb :deep(.live-media) {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.rail-placeholder {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 0.7rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
}

.rail-name {
  font-size: 0.82rem;
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.camera-main {
  min-width: 0;
  display: flex;
  flex-direction: column;
}

@media (max-width: 920px) {
  .camera-layout {
    grid-template-columns: 1fr;
  }
  .camera-rail {
    position: static;
    max-height: none;
  }
  .rail-list {
    flex-direction: row;
    overflow-x: auto;
    overflow-y: hidden;
    padding-bottom: 0.2rem;
  }
  .rail-item {
    flex: 0 0 160px;
  }
}

.controls-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 0.9rem;
  margin-bottom: 1.5rem;
}

.control-card {
  padding: 1.25rem;
}

.control-title {
  margin: 0;
  font-size: 0.85rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
}

.control-card > .control-title {
  margin-bottom: 0.9rem;
}

.imaging-toggle {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  margin-bottom: 0.9rem;
  padding: 0;
  background: none;
  border: none;
  color: inherit;
  font: inherit;
  cursor: pointer;
}

.imaging-toggle .control-title {
  margin: 0;
}

.imaging-caret {
  color: var(--text-muted);
}

.imaging-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 0.6rem;
}

.imaging-label {
  width: 110px;
  flex-shrink: 0;
  font-size: 0.88rem;
  color: var(--text-muted);
}

.imaging-row input[type='range'] {
  flex: 1;
  accent-color: var(--accent);
}

.imaging-value {
  width: 2.2rem;
  text-align: right;
  font-size: 0.85rem;
  color: var(--text-muted);
}

.imaging-select {
  padding: 0.4rem 0.6rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 0.9rem;
  font-family: inherit;
  outline: none;
}

.imaging-actions {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  margin-top: 0.75rem;
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
  width: auto;
}

.imaging-saved {
  color: #4ade80;
  font-size: 0.85rem;
}

.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
  gap: 0.9rem;
}

.stat {
  padding: 0.9rem 1.1rem;
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
}

.stat-label {
  font-size: 0.75rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
}

.stat-value {
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 1.05rem;
}
</style>
