<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useRoute } from 'vue-router'
import { get, post, put, ApiError } from '../api/client'
import type {
  Camera,
  CameraStats,
  ImagingSettings,
  OnvifSyncResponse,
} from '../api/types'
import { useAuthStore } from '../stores/auth'
import { formatBytes, formatDuration } from '../utils/format'
import StatusBadge from '../components/StatusBadge.vue'
import LivePlayer from '../components/LivePlayer.vue'
import PtzPad from '../components/PtzPad.vue'

const route = useRoute()
const auth = useAuthStore()
const cameraId = route.params.id as string

const canPlayback = computed(() => auth.user?.permissions.includes('playback') ?? false)
const canPtz = computed(() => auth.user?.permissions.includes('ptz') ?? false)
const canWrite = computed(() => auth.user?.permissions.includes('cameras:write') ?? false)

const camera = ref<Camera | null>(null)
const stats = ref<CameraStats | null>(null)
const loadError = ref('')

let pollTimer: ReturnType<typeof setInterval> | null = null

const status = computed(() => stats.value?.status ?? camera.value?.status ?? 'offline')

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
  try {
    const [cam, st] = await Promise.all([
      get<Camera>(`/cameras/${cameraId}`),
      get<CameraStats>(`/cameras/${cameraId}/stats`),
    ])
    camera.value = cam
    stats.value = st
    loadError.value = ''
  } catch {
    if (!camera.value) loadError.value = 'Failed to load camera'
  }
}

async function syncOnvif() {
  syncing.value = true
  syncError.value = ''
  syncSummary.value = ''
  try {
    const res = await post<OnvifSyncResponse>(`/cameras/${cameraId}/onvif/sync`)
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
    const res = await get<ImagingSettings>(`/cameras/${cameraId}/imaging`)
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
    await put(`/cameras/${cameraId}/imaging`, changed)
    imagingOriginal = { ...imaging }
    imagingSaved.value = true
  } catch (err) {
    imagingError.value = err instanceof ApiError ? err.message : 'Failed to apply imaging settings'
  }
}

onMounted(() => {
  refresh()
  pollTimer = setInterval(refresh, 5000)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
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

      <div class="player-wrap">
        <div class="player-frame">
          <LivePlayer :camera-id="cameraId" stream="main" />
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
          <span class="stat-value">{{ stats ? `${stats.bitrate_kbps.toFixed(0)} kbps` : '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">FPS</span>
          <span class="stat-value">{{ stats ? stats.fps.toFixed(1) : '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">Uptime</span>
          <span class="stat-value">{{ stats ? formatDuration(stats.uptime) : '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">Recorded</span>
          <span class="stat-value">{{ stats ? formatBytes(stats.recorded_bytes) : '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">Codec</span>
          <span class="stat-value">{{ stats?.codec?.toUpperCase() || '—' }}</span>
        </div>
        <div class="stat card">
          <span class="stat-label">Last frame</span>
          <span class="stat-value">
            {{ stats?.running ? `${stats.last_frame_age_s.toFixed(1)}s ago` : '—' }}
          </span>
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
  max-width: 960px;
  margin-bottom: 1.5rem;
}

.player-frame {
  position: relative;
  aspect-ratio: 16 / 9;
  background: #000;
  border-radius: var(--radius);
  overflow: hidden;
}

.player-frame :deep(.live-player) {
  position: absolute;
  inset: 0;
}

.controls-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 0.9rem;
  max-width: 960px;
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
  max-width: 960px;
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
