<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { get, post, ApiError } from '../api/client'
import type {
  Camera,
  CameraListResponse,
  ExportJob,
  ExportListResponse,
  TimelineRange as ApiTimelineRange,
  TimelineResponse,
} from '../api/types'
import {
  formatBytes,
  formatDateTime,
  fromLocalInputValue,
  toLocalInputValue,
} from '../utils/format'
import StatusBadge from '../components/StatusBadge.vue'
import LivePlayer from '../components/LivePlayer.vue'
import VideoPlayer from '../components/VideoPlayer.vue'
import ZoomableTimeline, {
  type TimelineRange,
  type TimelineEvent,
} from '../components/ZoomableTimeline.vue'

const route = useRoute()
const router = useRouter()
const cameraId = computed(() => route.params.cameraId as string)
const initialPlayheadMs = (() => {
  const t = route.query.t
  const n = typeof t === 'string' ? Number(t) : NaN
  return Number.isFinite(n) ? n : null
})()

const RANGE_OPTIONS = [
  { label: '1h', hours: 1 },
  { label: '6h', hours: 6 },
  { label: '24h', hours: 24 },
  { label: '3d', hours: 72 },
]

const camera = ref<Camera | null>(null)
const allCameras = ref<Camera[]>([])

// ---- Timeline window ----
const windowHours = ref(24)
const windowFromMs = ref(0)
const windowToMs = ref(0)
const recorded = ref<TimelineRange[]>([])
const density = ref<number[]>([])
const timelineEvents = ref<TimelineEvent[]>([])
const timelineLoading = ref(true)
const noRecordings = ref(false)

// ---- Playback ----
const playlistUrl = ref('')
const playerKey = ref(0)
const playheadMs = ref<number | null>(null)
const initialSeekForPlayer = ref<number | null>(null)
const streamSource = ref<'live' | 'playback'>('live')

// ---- Export ----
const showExport = ref(false)
const exportStartInput = ref('')
const exportEndInput = ref('')
const exportJob = ref<ExportJob | null>(null)
const exportError = ref('')

let timelineTimer: ReturnType<typeof setInterval> | null = null
let cameraTimer: ReturnType<typeof setInterval> | null = null
let exportPollTimer: ReturnType<typeof setInterval> | null = null
let seekDebounce: ReturnType<typeof setTimeout> | null = null

const hevcSupported = (() => {
  if (typeof document === 'undefined') return false
  const v = document.createElement('video')
  const mime = 'video/mp4; codecs="hvc1.1.6.L93.B0"'
  return (
    v.canPlayType(mime) !== '' ||
    (typeof MediaSource !== 'undefined' && MediaSource.isTypeSupported(mime))
  )
})()

const isH265 = computed(() => camera.value?.status_detail?.codec === 'h265')
const needsTranscode = computed(
  () => isH265.value && camera.value?.playback_transcode !== 'never' && !hevcSupported,
)
const hevcBlocked = computed(
  () => isH265.value && camera.value?.playback_transcode === 'never' && !hevcSupported,
)

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

function playlistFor(startMs: number, endMs: number): string {
  const start = new Date(startMs).toISOString()
  const end = new Date(endMs).toISOString()
  const base = `/api/v1/playback/${cameraId.value}/playlist.m3u8?start=${encodeURIComponent(start)}&end=${encodeURIComponent(end)}`
  return needsTranscode.value ? `${base}&transcode=1` : base
}

function startPlayback(startMs: number) {
  const start = Math.max(windowFromMs.value, Math.min(startMs, windowToMs.value - 1000))
  playheadMs.value = start
  initialSeekForPlayer.value = start
  playlistUrl.value = playlistFor(start, windowToMs.value)
  streamSource.value = 'playback'
  playerKey.value++
}

function switchToLive() {
  playlistUrl.value = ''
  playheadMs.value = null
  initialSeekForPlayer.value = null
  streamSource.value = 'live'
}

function onSeek(ms: number) {
  if (seekDebounce) clearTimeout(seekDebounce)
  seekDebounce = setTimeout(() => startPlayback(ms), 250)
}

function onTimelineSelect(ms: number) {
  if (recorded.value.length === 0) return
  const inRange = recorded.value.find((r) => {
    const s = new Date(r.start as string | number | Date).getTime()
    const e = new Date(r.end as string | number | Date).getTime()
    return ms >= s && ms <= e
  })
  if (inRange) startPlayback(ms)
}

function onTimelineOpen(ms: number) {
  onTimelineSelect(ms)
}

function onTimelineWindowChange(from: number, to: number) {
  windowFromMs.value = from
  windowToMs.value = to
  windowHours.value = -1
  fetchTimeline()
}

async function fetchCamera() {
  try {
    camera.value = await get<Camera>(`/cameras/${cameraId.value}`)
    if (needsTranscode.value && playlistUrl.value && !playlistUrl.value.includes('transcode=1')) {
      playlistUrl.value += '&transcode=1'
      playerKey.value++
    }
  } catch {
    // Non-fatal — header shows less info.
  }
}

async function fetchAllCameras() {
  try {
    const res = await get<CameraListResponse>('/cameras')
    allCameras.value = res.cameras ?? []
  } catch {
    // Non-fatal — rail will be empty.
  }
}

function selectCamera(id: string) {
  if (id === cameraId.value) return
  const t = playheadMs.value != null ? `?t=${playheadMs.value}` : ''
  router.push(`/playback/${id}${t}`)
}

function cycleCamera(direction: -1 | 1) {
  const target = direction === -1 ? previousCamera.value : nextCamera.value
  if (target) selectCamera(target.id)
}

async function fetchTimeline() {
  const to = Date.now()
  const from = to - windowHours.value * 3_600_000
  windowFromMs.value = from
  windowToMs.value = to
  try {
    const res = await get<TimelineResponse>(
      `/recordings/timeline?camera=${encodeURIComponent(cameraId.value)}&from=${encodeURIComponent(new Date(from).toISOString())}&to=${encodeURIComponent(new Date(to).toISOString())}&buckets=200`,
    )
    recorded.value = (res.recorded ?? []).map((r: ApiTimelineRange) => ({
      start: new Date(r.start).getTime(),
      end: new Date(r.end).getTime(),
    }))
    density.value = res.density ?? []
    timelineEvents.value = (res.events ?? []).map((e) => ({
      id: e.id,
      ts: new Date(e.ts).getTime(),
      type: e.type,
      label: e.label,
    }))
    noRecordings.value = recorded.value.length === 0
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      recorded.value = []
      density.value = []
      timelineEvents.value = []
      noRecordings.value = true
    }
  } finally {
    timelineLoading.value = false
  }

  // Auto-start playback once: use ?t= hint from aggregate drill-down if it
  // falls inside a recorded range; otherwise fall back to the tail of the
  // most recent recording.
  if (streamSource.value === 'live' && recorded.value.length > 0) {
    let start: number
    if (initialPlayheadMs != null) {
      const hint = initialPlayheadMs
      const inRange = recorded.value.find(
        (r) =>
          hint >= (r.start as number) && hint <= (r.end as number),
      )
      if (inRange) {
        start = hint
      } else {
        const nearest = recorded.value.reduce((prev, cur) =>
          Math.abs((cur.start as number) - hint) <
          Math.abs((prev.start as number) - hint)
            ? cur
            : prev,
        )
        start = Math.max(
          nearest.start as number,
          Math.min(nearest.end as number, windowToMs.value),
        )
      }
    } else {
      const last = recorded.value[recorded.value.length - 1]
      start = Math.max(
        last.start as number,
        Math.min(last.end as number, windowToMs.value) - 60_000,
      )
    }
    startPlayback(start)
  }
}

function setWindowHours(hours: number) {
  windowHours.value = hours
  timelineLoading.value = true
  fetchTimeline()
}

function hueFor(camId: string): number {
  let hash = 0
  for (let i = 0; i < camId.length; i++) {
    hash = (hash * 31 + camId.charCodeAt(i)) >>> 0
  }
  return hash % 360
}

// ---- Export ----

function openExport() {
  const center = playheadMs.value ?? Date.now()
  exportStartInput.value = toLocalInputValue(center - 30_000)
  exportEndInput.value = toLocalInputValue(center + 30_000)
  exportJob.value = null
  exportError.value = ''
  showExport.value = true
}

async function submitExport() {
  exportError.value = ''
  exportJob.value = null
  const startMs = fromLocalInputValue(exportStartInput.value)
  const endMs = fromLocalInputValue(exportEndInput.value)
  if (!startMs || !endMs || endMs <= startMs) {
    exportError.value = 'End must be after start.'
    return
  }
  try {
    const job = await post<ExportJob>('/exports', {
      camera_id: cameraId.value,
      start: new Date(startMs).toISOString(),
      end: new Date(endMs).toISOString(),
    })
    exportJob.value = job
    pollExport(job.id)
  } catch (err) {
    exportError.value = err instanceof ApiError ? err.message : 'Failed to create export'
  }
}

function pollExport(id: string) {
  if (exportPollTimer) clearInterval(exportPollTimer)
  exportPollTimer = setInterval(async () => {
    try {
      const res = await get<ExportListResponse>('/exports')
      const job = (res.exports ?? []).find((e) => e.id === id)
      if (!job) return
      exportJob.value = job
      if (job.status === 'done' || job.status === 'failed') {
        if (exportPollTimer) clearInterval(exportPollTimer)
        exportPollTimer = null
        if (job.status === 'done') triggerDownload(id)
      }
    } catch {
      // Keep polling — transient errors are fine.
    }
  }, 2000)
}

function triggerDownload(id: string) {
  const a = document.createElement('a')
  a.href = `/api/v1/exports/${id}/download`
  a.download = ''
  document.body.appendChild(a)
  a.click()
  a.remove()
}

function onKeyDown(e: KeyboardEvent) {
  const tag = (e.target as HTMLElement | null)?.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA') return
  if (e.key === 'ArrowLeft') cycleCamera(-1)
  else if (e.key === 'ArrowRight') cycleCamera(1)
}

watch(() => cameraId.value, () => {
  // Switching cameras via rail: reset everything.
  playlistUrl.value = ''
  playheadMs.value = null
  initialSeekForPlayer.value = null
  streamSource.value = 'live'
  playerKey.value++
  fetchCamera()
  fetchTimeline()
})

onMounted(() => {
  fetchCamera()
  fetchAllCameras()
  fetchTimeline()
  timelineTimer = setInterval(fetchTimeline, 30_000)
  cameraTimer = setInterval(fetchAllCameras, 30_000)
  window.addEventListener('keydown', onKeyDown)
})

onBeforeUnmount(() => {
  if (timelineTimer) clearInterval(timelineTimer)
  if (cameraTimer) clearInterval(cameraTimer)
  if (exportPollTimer) clearInterval(exportPollTimer)
  if (seekDebounce) clearTimeout(seekDebounce)
  window.removeEventListener('keydown', onKeyDown)
})
</script>

<template>
  <div>
    <header class="detail-header">
      <div class="detail-title">
        <router-link to="/" class="back-link">Dashboard</router-link>
        <h1 class="page-title">Playback{{ camera ? ` — ${camera.name}` : '' }}</h1>
        <StatusBadge v-if="camera" :status="camera.status" />
      </div>
      <div class="header-actions">
        <button
          class="btn btn-ghost btn-sm"
          type="button"
          :disabled="noRecordings && !timelineLoading"
          @click="openExport"
        >
          Export clip
        </button>
      </div>
    </header>

    <p v-if="hevcBlocked" class="hevc-note">
      This camera records H.265 and playback transcode is disabled — playback requires a
      browser with HEVC support (e.g. Safari).
    </p>

    <div class="playback-layout">
      <aside class="camera-rail">
        <div class="rail-header">
          <span class="muted small">Cameras</span>
          <span class="muted small" v-if="allCameras.length">
            {{ currentIndex + 1 }} / {{ allCameras.length }}
          </span>
        </div>
        <div class="rail-list">
          <button
            v-for="c in allCameras"
            :key="c.id"
            type="button"
            class="rail-item"
            :class="{
              'rail-item-active': c.id === cameraId,
              'rail-item-offline': c.status === 'offline',
            }"
            :title="`${c.name} — ${c.status}`"
            @click="selectCamera(c.id)"
          >
            <div class="rail-dot" :style="{ background: `hsl(${hueFor(c.id)} 70% 60%)` }" aria-hidden="true"></div>
            <span class="rail-name">{{ c.name }}</span>
            <StatusBadge :status="c.status" compact />
          </button>
        </div>
      </aside>

      <main class="playback-main">
        <div class="player-wrap">
          <VideoPlayer
            v-if="streamSource === 'playback' && playlistUrl"
            :key="playerKey"
            :src="playlistUrl"
            mode="playback"
            :initial-seek-ms="initialSeekForPlayer"
            :autoplay="false"
            :muted="true"
            @loaded="() => (initialSeekForPlayer = null)"
          />
          <div v-else-if="timelineLoading" class="player-placeholder">
            <span>Loading recordings…</span>
          </div>
          <div v-else-if="!camera || !camera.enabled || camera.status === 'offline'" class="player-placeholder">
            <span>{{ camera?.enabled ? camera.status : 'disabled' }}</span>
          </div>
          <div v-else class="player-frame">
            <LivePlayer :camera-id="cameraId" stream="sub" />
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
            <span class="player-cam-name">{{ camera?.name }}</span>
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
              v-if="streamSource === 'playback'"
              class="btn btn-ghost btn-sm"
              type="button"
              @click="switchToLive"
              title="Stop playback, return to live"
            >
              Live
            </button>
            <router-link
              :to="`/cameras/${cameraId}`"
              class="btn btn-ghost btn-sm"
            >
              Open camera →
            </router-link>
          </div>
        </div>

        <section class="timeline-section">
          <div class="range-row">
            <div class="range-buttons">
              <button
                v-for="opt in RANGE_OPTIONS"
                :key="opt.hours"
                class="btn btn-ghost btn-sm range-btn"
                :class="{ 'range-active': windowHours === opt.hours }"
                type="button"
                @click="setWindowHours(opt.hours)"
              >
                {{ opt.label }}
              </button>
            </div>
            <span v-if="playheadMs != null" class="playhead-time mono">
              {{ formatDateTime(playheadMs) }}
            </span>
            <button
              v-if="playheadMs != null && streamSource === 'playback'"
              class="btn btn-ghost btn-sm"
              type="button"
              @click="openExport"
            >
              Export at playhead
            </button>
          </div>

          <ZoomableTimeline
            v-if="windowToMs > windowFromMs"
            :from-ms="windowFromMs"
            :to-ms="windowToMs"
            :recorded="recorded"
            :events="timelineEvents"
            :playhead-ms="playheadMs"
            :density="density"
            @seek="onSeek"
            @select="onTimelineSelect"
            @open="onTimelineOpen"
            @window-change="onTimelineWindowChange"
          />
          <p v-if="noRecordings && !timelineLoading" class="muted empty-note">
            Nothing recorded in this window yet. Recordings will appear here as they are captured.
          </p>
        </section>
      </main>
    </div>

    <div v-if="showExport" class="modal-backdrop" @click.self="showExport = false">
      <div class="modal card">
        <h2 class="modal-title">Export clip</h2>
        <div class="field-row">
          <label class="field">
            <span>Start</span>
            <input v-model="exportStartInput" type="datetime-local" />
          </label>
          <label class="field">
            <span>End</span>
            <input v-model="exportEndInput" type="datetime-local" />
          </label>
        </div>

        <p v-if="exportError" class="error-text">{{ exportError }}</p>

        <div v-if="exportJob" class="export-status">
          <template v-if="exportJob.status === 'done'">
            Export complete ({{ formatBytes(exportJob.size_bytes) }}) — download started.
            <a class="text-link" :href="`/api/v1/exports/${exportJob.id}/download`">Download again</a>
          </template>
          <template v-else-if="exportJob.status === 'failed'">
            <span class="error-text">Export failed: {{ exportJob.error || 'unknown error' }}</span>
          </template>
          <template v-else>
            <span class="spinner" aria-hidden="true"></span>
            Export {{ exportJob.status }}…
          </template>
        </div>

        <div class="modal-actions">
          <button class="btn btn-ghost" type="button" @click="showExport = false">Close</button>
          <button
            class="btn btn-primary btn-inline"
            type="button"
            :disabled="!!exportJob && (exportJob.status === 'pending' || exportJob.status === 'processing')"
            @click="submitExport"
          >
            Start export
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.hevc-note {
  margin: 0 0 0.75rem;
  padding: 0.6rem 0.8rem;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  background: var(--bg-card);
  color: var(--text-muted);
  font-size: 0.85rem;
}

.page-title {
  margin: 0;
  font-size: 1.3rem;
  font-weight: 600;
}

.muted {
  color: var(--text-muted);
}

.small {
  font-size: 0.8rem;
}

.mono {
  font-family: 'SF Mono', 'Menlo', monospace;
}

.detail-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1.25rem;
  gap: 1rem;
  flex-wrap: wrap;
}

.detail-title {
  display: flex;
  align-items: center;
  gap: 0.9rem;
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

.header-actions {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}

.btn-inline {
  width: auto;
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
}

.text-link {
  color: var(--accent);
}

/* ---- UI3-style layout (rail + main stage) ---- */

.playback-layout {
  display: grid;
  grid-template-columns: 200px 1fr;
  gap: 1rem;
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
  gap: 0.35rem;
  overflow-y: auto;
  padding-right: 0.2rem;
}

.rail-item {
  display: flex;
  align-items: center;
  gap: 0.45rem;
  padding: 0.45rem 0.6rem;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 4px;
  font: inherit;
  color: inherit;
  cursor: pointer;
  text-align: left;
  transition: border-color 0.12s ease, background 0.12s ease;
  min-width: 0;
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

.rail-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.rail-name {
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
  flex: 1;
}

.playback-main {
  min-width: 0;
  display: flex;
  flex-direction: column;
}

.player-wrap {
  width: 100%;
  margin-bottom: 0.7rem;
}

.player-frame {
  position: relative;
  width: 100%;
  aspect-ratio: 16 / 9;
  max-height: calc(100vh - 18rem);
  background: #000;
  border-radius: var(--radius);
  overflow: hidden;
}

.player-frame :deep(.live-player) {
  position: absolute;
  inset: 0;
}

.player-frame :deep(.live-media) {
  width: 100%;
  height: 100%;
  object-fit: contain;
}

.player-placeholder {
  display: flex;
  align-items: center;
  justify-content: center;
  aspect-ratio: 16 / 9;
  max-height: calc(100vh - 18rem);
  background: #000;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text-muted);
}

.player-info {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 0.7rem;
  flex-wrap: wrap;
}

.player-info-left,
.player-info-right {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}

.player-cam-name {
  font-weight: 600;
}

.timeline-section {
  /* The timeline flows full width below the player. */
}

.range-row {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  margin-bottom: 0.6rem;
  flex-wrap: wrap;
}

.range-buttons {
  display: flex;
  gap: 0.4rem;
}

.range-btn {
  font-family: 'SF Mono', 'Menlo', monospace;
}

.range-active {
  color: var(--text);
  border-color: var(--accent);
  background: rgba(79, 140, 255, 0.12);
}

.playhead-time {
  font-size: 0.85rem;
  color: var(--text-muted);
}

.empty-note {
  margin-top: 0.75rem;
  font-size: 0.9rem;
}

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1.5rem;
  z-index: 100;
}

.modal {
  width: 100%;
  max-width: 460px;
}

.modal-title {
  margin: 0 0 1.25rem;
  font-size: 1.15rem;
  font-weight: 600;
}

.field-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.9rem;
}

.field input {
  font-family: inherit;
}

.export-status {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 1rem;
  font-size: 0.9rem;
  color: var(--text-muted);
}

.spinner {
  display: inline-block;
  width: 13px;
  height: 13px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.6rem;
}

@media (max-width: 920px) {
  .playback-layout {
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
    flex: 0 0 200px;
  }
  .player-frame,
  .player-placeholder {
    max-height: none;
  }
}
</style>
