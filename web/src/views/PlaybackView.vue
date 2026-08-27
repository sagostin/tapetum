<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { get, post, ApiError } from '../api/client'
import type { Camera, ExportJob, ExportListResponse, TimelineResponse } from '../api/types'
import {
  formatBytes,
  formatDateTime,
  fromLocalInputValue,
  toLocalInputValue,
} from '../utils/format'
import StatusBadge from '../components/StatusBadge.vue'
import HlsPlayer from '../components/HlsPlayer.vue'
import TimelineScrubber from '../components/TimelineScrubber.vue'

const route = useRoute()
const cameraId = route.params.cameraId as string

const RANGE_OPTIONS = [
  { label: '1h', hours: 1 },
  { label: '6h', hours: 6 },
  { label: '24h', hours: 24 },
  { label: '3d', hours: 72 },
]

const camera = ref<Camera | null>(null)

// ---- Timeline window ----
const windowHours = ref(24)
const windowFromMs = ref(0)
const windowToMs = ref(0)
const recorded = ref<{ startMs: number; endMs: number }[]>([])
const density = ref<number[]>([])
const timelineLoading = ref(true)
const noRecordings = ref(false)

// ---- Playback ----
const playlistUrl = ref('')
const baseTimeMs = ref<number | null>(null)
const videoTimeS = ref(0)
const seekOverrideMs = ref<number | null>(null)
const playerKey = ref(0)

// ---- Export ----
const showExport = ref(false)
const exportStartInput = ref('')
const exportEndInput = ref('')
const exportJob = ref<ExportJob | null>(null)
const exportError = ref('')

let timelineTimer: ReturnType<typeof setInterval> | null = null
let exportPollTimer: ReturnType<typeof setInterval> | null = null
let seekDebounce: ReturnType<typeof setTimeout> | null = null

const playheadMs = computed(() => {
  if (seekOverrideMs.value != null) return seekOverrideMs.value
  if (baseTimeMs.value != null) return baseTimeMs.value + videoTimeS.value * 1000
  return null
})

function playlistFor(startMs: number, endMs: number): string {
  const start = new Date(startMs).toISOString()
  const end = new Date(endMs).toISOString()
  return `/api/v1/playback/${cameraId}/playlist.m3u8?start=${encodeURIComponent(start)}&end=${encodeURIComponent(end)}`
}

function startPlayback(startMs: number) {
  const start = Math.max(windowFromMs.value, Math.min(startMs, windowToMs.value - 1000))
  baseTimeMs.value = start
  videoTimeS.value = 0
  seekOverrideMs.value = null
  playlistUrl.value = playlistFor(start, windowToMs.value)
  // Force HlsPlayer to reload even if the URL is unchanged.
  playerKey.value++
}

function onSeek(ms: number) {
  seekOverrideMs.value = ms
  if (seekDebounce) clearTimeout(seekDebounce)
  seekDebounce = setTimeout(() => startPlayback(ms), 400)
}

function onTimeUpdate(seconds: number) {
  videoTimeS.value = seconds
}

async function fetchCamera() {
  try {
    camera.value = await get<Camera>(`/cameras/${cameraId}`)
  } catch {
    // Non-fatal — header just shows less info.
  }
}

async function fetchTimeline() {
  const to = Date.now()
  const from = to - windowHours.value * 3_600_000
  windowFromMs.value = from
  windowToMs.value = to
  try {
    const res = await get<TimelineResponse>(
      `/recordings/timeline?camera=${encodeURIComponent(cameraId)}&from=${encodeURIComponent(new Date(from).toISOString())}&to=${encodeURIComponent(new Date(to).toISOString())}&buckets=200`,
    )
    recorded.value = (res.recorded ?? []).map((r) => ({
      startMs: new Date(r.start).getTime(),
      endMs: new Date(r.end).getTime(),
    }))
    density.value = res.density ?? []
    noRecordings.value = recorded.value.length === 0
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) {
      recorded.value = []
      density.value = []
      noRecordings.value = true
    }
  } finally {
    timelineLoading.value = false
  }

  // Auto-start playback once, at the tail of the most recent recording.
  if (!playlistUrl.value && recorded.value.length > 0) {
    const last = recorded.value[recorded.value.length - 1]
    const start = Math.max(last.startMs, Math.min(last.endMs, windowToMs.value) - 60_000)
    startPlayback(start)
  }
}

function setWindowHours(hours: number) {
  windowHours.value = hours
  timelineLoading.value = true
  fetchTimeline()
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
      camera_id: cameraId,
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
        if (job.status === 'done') {
          triggerDownload(id)
        }
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

onMounted(() => {
  fetchCamera()
  fetchTimeline()
  timelineTimer = setInterval(fetchTimeline, 30_000)
})

onBeforeUnmount(() => {
  if (timelineTimer) clearInterval(timelineTimer)
  if (exportPollTimer) clearInterval(exportPollTimer)
  if (seekDebounce) clearTimeout(seekDebounce)
})
</script>

<template>
  <div>
    <div class="detail-header">
      <div class="detail-title">
        <router-link :to="`/cameras/${cameraId}`" class="back-link">Camera</router-link>
        <h1 class="page-title">Playback{{ camera ? ` — ${camera.name}` : '' }}</h1>
        <StatusBadge v-if="camera" :status="camera.status" />
      </div>
      <button
        class="btn btn-primary btn-inline"
        type="button"
        :disabled="noRecordings && !timelineLoading"
        @click="openExport"
      >
        Export clip
      </button>
    </div>

    <div class="player-wrap">
      <HlsPlayer
        v-if="playlistUrl"
        :key="playerKey"
        :src="playlistUrl"
        @timeupdate="onTimeUpdate"
      />
      <div v-else-if="timelineLoading" class="player-placeholder">
        <span>Loading recordings…</span>
      </div>
      <div v-else class="player-placeholder">
        <span>No recordings in this time window.</span>
      </div>
    </div>

    <div class="timeline-section">
      <div class="range-row">
        <div class="range-buttons">
          <button
            v-for="opt in RANGE_OPTIONS"
            :key="opt.hours"
            class="btn btn-ghost btn-sm"
            :class="{ 'range-active': windowHours === opt.hours }"
            type="button"
            @click="setWindowHours(opt.hours)"
          >
            {{ opt.label }}
          </button>
        </div>
        <span v-if="playheadMs != null" class="playhead-time mono">{{ formatDateTime(playheadMs) }}</span>
      </div>

      <TimelineScrubber
        v-if="windowToMs > windowFromMs"
        :from-ms="windowFromMs"
        :to-ms="windowToMs"
        :recorded="recorded"
        :density="density"
        :playhead-ms="playheadMs"
        @seek="onSeek"
      />
      <p v-if="noRecordings && !timelineLoading" class="muted empty-note">
        Nothing recorded in this window yet. Recordings will appear here as they are captured.
      </p>
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
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
}

.player-wrap {
  max-width: 960px;
  margin-bottom: 1.25rem;
}

.player-placeholder {
  display: flex;
  align-items: center;
  justify-content: center;
  aspect-ratio: 16 / 9;
  background: #000;
  border: 1px solid var(--border);
  border-radius: var(--radius);
  color: var(--text-muted);
}

.timeline-section {
  max-width: 960px;
}

.range-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.6rem;
}

.range-buttons {
  display: flex;
  gap: 0.4rem;
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

.text-link {
  color: var(--accent);
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
  to {
    transform: rotate(360deg);
  }
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.6rem;
}
</style>
