<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { get } from '../api/client'
import type {
  Camera,
  CameraListResponse,
  TimelineEvent,
  TimelineRange,
  TimelineResponse,
} from '../api/types'
import VideoPlayer from '../components/VideoPlayer.vue'
import CameraStage from '../components/CameraStage.vue'
import StatusBadge from '../components/StatusBadge.vue'
import ZoomableTimeline, {
  type TimelineEvent as ZTLEvent,
} from '../components/ZoomableTimeline.vue'
import { formatDateTime } from '../utils/format'

const router = useRouter()

const RANGE_OPTIONS = [
  { label: '1h', hours: 1 },
  { label: '6h', hours: 6 },
  { label: '24h', hours: 24 },
  { label: '3d', hours: 72 },
]

interface CameraTimeline {
  recorded: TimelineRange[]
  events: TimelineEvent[]
  loading: boolean
  error: string
}

const cameras = ref<Camera[]>([])
const loading = ref(true)
const loadError = ref('')

const windowHours = ref(24)
const windowFromMs = ref(Date.now() - 24 * 3_600_000)
const windowToMs = ref(Date.now())
const playheadMs = ref<number | null>(null)
const perCamera = ref<Record<string, CameraTimeline>>({})
const mode = ref<'overlay' | 'stacked'>('overlay')

const activeCameraId = ref<string | null>(null)
const playbackMode = ref<'live' | 'playback'>('live')

const activeCamera = computed(() => {
  if (!activeCameraId.value) return null
  return cameras.value.find((c) => c.id === activeCameraId.value) ?? null
})

const playbackSrc = computed(() => {
  const cam = activeCamera.value
  if (!cam) return ''
  const base = `/api/v1/playback/${cam.id}/playlist.m3u8`
  return playheadMs.value != null
    ? `${base}?start=${encodeURIComponent(new Date(playheadMs.value).toISOString())}`
    : base
})

let pollTimer: ReturnType<typeof setInterval> | null = null

async function refreshCameras() {
  try {
    const res = await get<CameraListResponse>('/cameras')
    cameras.value = res.cameras ?? []
    loadError.value = ''
    for (const cam of cameras.value) {
      if (!perCamera.value[cam.id]) {
        perCamera.value[cam.id] = { recorded: [], events: [], loading: true, error: '' }
      }
    }
    if (!activeCameraId.value && cameras.value.length > 0) {
      activeCameraId.value = cameras.value[0].id
      playbackMode.value = 'live'
    }
  } catch {
    if (!cameras.value.length) loadError.value = 'Failed to load cameras'
  } finally {
    loading.value = false
  }
}

async function refreshTimeline(camId: string, fromMs: number, toMs: number) {
  const entry = perCamera.value[camId]
  if (!entry) return
  entry.loading = true
  entry.error = ''
  try {
    const res = await get<TimelineResponse>(
      `/recordings/timeline?camera=${encodeURIComponent(camId)}&from=${encodeURIComponent(new Date(fromMs).toISOString())}&to=${encodeURIComponent(new Date(toMs).toISOString())}&buckets=200`,
    )
    entry.recorded = (res.recorded ?? []).map((r) => ({ start: r.start, end: r.end }))
    entry.events = (res.events ?? []).map((e) => ({
      id: e.id,
      ts: e.ts,
      type: e.type,
      label: e.label,
    }))
  } catch {
    entry.error = 'Timeline failed to load'
  } finally {
    entry.loading = false
  }
}

async function refreshAllTimelines() {
  const to = Date.now()
  const from = to - windowHours.value * 3_600_000
  windowFromMs.value = from
  windowToMs.value = to
  await Promise.all(
    cameras.value.map((cam) =>
      refreshTimeline(cam.id, from, to),
    ),
  )
}

function setWindowHours(hours: number) {
  windowHours.value = hours
  refreshAllTimelines()
}

async function setWindow(fromMs: number, toMs: number, fromZoom = false) {
  windowFromMs.value = fromMs
  windowToMs.value = toMs
  if (!fromZoom) {
    const hours = Math.round((toMs - fromMs) / 3_600_000)
    if ([1, 6, 24, 72].includes(hours)) windowHours.value = hours
    else windowHours.value = -1
  }
  await Promise.all(
    cameras.value.map((cam) =>
      refreshTimeline(cam.id, fromMs, toMs),
    ),
  )
}

async function resetWindow() {
  await refreshAllTimelines()
}

async function jumpToNow() {
  const now = Date.now()
  const from = now - (windowToMs.value - windowFromMs.value)
  await setWindow(from, now, true)
}

function selectCamera(camId: string, atMs?: number) {
  if (activeCameraId.value !== camId) activeCameraId.value = camId
  if (atMs != null) {
    playheadMs.value = atMs
    playbackMode.value = 'playback'
  } else {
    playheadMs.value = null
    playbackMode.value = 'live'
  }
}

function clearPlayhead() {
  playheadMs.value = null
  playbackMode.value = 'live'
}

function openCameraFull(camId: string, atMs?: number) {
  const t = atMs ?? playheadMs.value
  const q = t != null ? `?t=${t}` : ''
  router.push(`/playback/${camId}${q}`)
}

// ---- Aggregate timeline data ----

interface OverlaySeg { start: number; end: number; camId: string }
interface OverlayEvent extends TimelineEvent { camId: string }

const overlaySegments = computed<OverlaySeg[]>(() => {
  const out: OverlaySeg[] = []
  for (const cam of cameras.value) {
    const entry = perCamera.value[cam.id]
    if (!entry) continue
    for (const r of entry.recorded) {
      out.push({
        start: new Date(r.start).getTime(),
        end: new Date(r.end).getTime(),
        camId: cam.id,
      })
    }
  }
  return out
})

const overlayEvents = computed<OverlayEvent[]>(() => {
  const out: OverlayEvent[] = []
  for (const cam of cameras.value) {
    const entry = perCamera.value[cam.id]
    if (!entry) continue
    for (const e of entry.events) out.push({ ...e, camId: cam.id })
  }
  return out.sort((a, b) => new Date(a.ts).getTime() - new Date(b.ts).getTime())
})

const aggregateRecorded = computed<{ start: number; end: number; camId: string }[]>(() =>
  overlaySegments.value.map((s) => ({
    start: s.start,
    end: s.end,
    camId: s.camId,
  })),
)

const aggregateEvents = computed<ZTLEvent[]>(() =>
  overlayEvents.value.map((e) => ({
    id: e.id,
    ts: e.ts,
    type: e.type,
    label: e.label,
    camId: e.camId,
  })),
)

const stackedRows = computed(() =>
  cameras.value.map((cam) => {
    const entry = perCamera.value[cam.id]
    return {
      cam,
      recorded: (entry?.recorded ?? []).map((r) => ({
        start: new Date(r.start).getTime(),
        end: new Date(r.end).getTime(),
      })),
      events: (entry?.events ?? []).map((e) => ({
        id: e.id,
        ts: e.ts,
        type: e.type,
        label: e.label,
      })),
    }
  }),
)

function hueFor(camId: string): number {
  let hash = 0
  for (let i = 0; i < camId.length; i++) {
    hash = (hash * 31 + camId.charCodeAt(i)) >>> 0
  }
  return hash % 360
}

// ---- Timeline interaction handlers ----

function onAggregateSelect(ms: number, camId?: string) {
  if (camId) selectCamera(camId, ms)
  else {
    playheadMs.value = ms
    playbackMode.value = 'live'
  }
}

function onAggregateSeek(ms: number) {
  playheadMs.value = ms
}

function onAggregateOpen(ms: number, camId?: string) {
  if (camId) openCameraFull(camId, ms)
}

function onAggregateWindowChange(from: number, to: number) {
  setWindow(from, to, true)
}

function onRowSelect(camId: string, ms: number) {
  selectCamera(camId, ms)
}
function onRowSeek(_camId: string, ms: number) {
  playheadMs.value = ms
}
function onRowOpen(camId: string, ms: number) {
  openCameraFull(camId, ms)
}
function onRowWindowChange(from: number, to: number) {
  setWindow(from, to, true)
}

// ---- Camera switcher helpers ----

function nextCamera(direction: 1 | -1) {
  if (!cameras.value.length || !activeCameraId.value) return
  const idx = cameras.value.findIndex((c) => c.id === activeCameraId.value)
  if (idx < 0) return
  const next = (idx + direction + cameras.value.length) % cameras.value.length
  selectCamera(cameras.value[next].id)
}

// ---- Keyboard ----

function onKeyDown(e: KeyboardEvent) {
  const tag = (e.target as HTMLElement | null)?.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA') return
  if (e.key === 'ArrowLeft') nextCamera(-1)
  else if (e.key === 'ArrowRight') nextCamera(1)
}

onMounted(() => {
  refreshCameras().then(() => refreshAllTimelines())
  pollTimer = setInterval(refreshAllTimelines, 60_000)
  window.addEventListener('keydown', onKeyDown)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
  window.removeEventListener('keydown', onKeyDown)
})

watch(windowHours, () => refreshAllTimelines())
</script>

<template>
  <div>
    <div class="detail-header">
      <div class="detail-title">
        <h1 class="page-title">Playback</h1>
        <span class="muted small">All cameras · live + recordings</span>
      </div>
      <div class="header-actions">
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
        <span v-if="windowHours === -1" class="muted small custom-range">
          custom
        </span>
        <div class="mode-buttons" role="group" aria-label="Timeline mode">
          <button
            class="btn btn-ghost btn-sm"
            :class="{ 'range-active': mode === 'overlay' }"
            type="button"
            @click="mode = 'overlay'"
            title="All cameras overlaid on a single row"
          >
            Overlay
          </button>
          <button
            class="btn btn-ghost btn-sm"
            :class="{ 'range-active': mode === 'stacked' }"
            type="button"
            @click="mode = 'stacked'"
            title="One row per camera"
          >
            Stacked
          </button>
        </div>
      </div>
    </div>

    <p v-if="loadError" class="error-text">{{ loadError }}</p>
    <p v-else-if="loading" class="muted">Loading cameras…</p>

    <div v-else-if="!cameras.length" class="empty-state empty-centered">
      <h2>No cameras yet</h2>
      <p>
        <router-link to="/cameras" class="text-link">Add your first camera</router-link>
        to start recording.
      </p>
    </div>

    <template v-else>
      <!-- Camera tabs above the player -->
      <div class="camera-switcher" role="tablist" aria-label="Cameras">
        <button
          v-for="cam in cameras"
          :key="cam.id"
          type="button"
          role="tab"
          class="cam-chip"
          :class="{
            'cam-chip-active': cam.id === activeCameraId,
            'cam-chip-offline': cam.status === 'offline',
          }"
          :aria-selected="cam.id === activeCameraId"
          :title="`${cam.name} — ${cam.status}`"
          @click="selectCamera(cam.id)"
        >
          <StatusBadge :status="cam.status" compact />
          <span class="cam-chip-name">{{ cam.name }}</span>
          <span
            class="cam-chip-dot"
            :style="{ background: `hsl(${hueFor(cam.id)} 70% 60%)` }"
            aria-hidden="true"
          ></span>
        </button>
      </div>

      <!-- Single main player area (UI3 model). -->
      <div class="player-area-wrap">
        <VideoPlayer
          v-if="playbackMode === 'playback' && playbackSrc"
          :key="`${activeCameraId}:${playheadMs}`"
          :src="playbackSrc"
          mode="playback"
          :initial-seek-ms="playheadMs"
          :autoplay="false"
          :muted="true"
        />
        <div v-else class="live-wrap">
          <CameraStage
            v-if="activeCamera && activeCamera.enabled && activeCamera.status !== 'offline'"
            mode="stage"
            :camera="activeCamera"
            stream="sub"
          />
          <div v-else class="player-placeholder">
            <span>{{ activeCamera?.enabled ? activeCamera.status : 'disabled' }}</span>
          </div>
        </div>

        <div class="player-info">
          <span v-if="activeCamera" class="player-cam-name">
            {{ activeCamera.name }}
            <span class="muted small">
              · {{ playbackMode === 'playback' && playheadMs ? formatDateTime(playheadMs) : 'live' }}
            </span>
          </span>
          <span class="muted small">
            <span v-if="playbackMode === 'playback'" class="badge-rec">REC</span>
          </span>
          <span class="spacer"></span>
          <button
            v-if="playheadMs != null"
            class="btn btn-ghost btn-sm"
            type="button"
            @click="clearPlayhead"
            title="Stop playback, return to live"
          >
            Live
          </button>
          <button
            v-if="activeCameraId && (perCamera[activeCameraId]?.recorded?.length ?? 0) > 0"
            class="btn btn-ghost btn-sm"
            type="button"
            @click="openCameraFull(activeCameraId)"
            title="Open the full single-camera playback page"
          >
            Open {{ activeCamera?.name }} →
          </button>
        </div>
      </div>

      <!-- Aggregate timeline at the bottom -->
      <section class="timeline-section">
        <div class="timeline-toolbar">
          <span class="muted small">
            {{ formatDateTime(windowFromMs) }} → {{ formatDateTime(windowToMs) }}
          </span>
          <div class="toolbar-actions">
            <button
              class="btn btn-ghost btn-sm"
              type="button"
              @click="resetWindow"
              title="Reset to the default range"
            >
              Reset
            </button>
            <button
              v-if="windowHours === -1"
              class="btn btn-ghost btn-sm"
              type="button"
              @click="jumpToNow"
              title="Center the timeline on the current time"
            >
              Now
            </button>
          </div>
        </div>

        <div v-if="mode === 'overlay'" class="overlay-section">
          <ZoomableTimeline
            :from-ms="windowFromMs"
            :to-ms="windowToMs"
            :recorded="aggregateRecorded"
            :events="aggregateEvents"
            :playhead-ms="playheadMs"
            aggregate
            @seek="onAggregateSeek"
            @select="onAggregateSelect"
            @open="onAggregateOpen"
            @window-change="onAggregateWindowChange"
          />
        </div>

        <div v-else class="stacked-section">
          <div
            v-for="row in stackedRows"
            :key="row.cam.id"
            class="stacked-row"
            :class="{ 'row-offline': row.cam.status === 'offline' }"
          >
            <button
              class="row-label"
              type="button"
              :class="{ 'row-label-active': row.cam.id === activeCameraId }"
              @click="selectCamera(row.cam.id)"
              :title="`Switch to ${row.cam.name}`"
            >
              <span
                class="row-dot"
                :style="{ background: `hsl(${hueFor(row.cam.id)} 70% 60%)` }"
                aria-hidden="true"
              ></span>
              <span class="row-name">{{ row.cam.name }}</span>
              <StatusBadge :status="row.cam.status" />
            </button>
            <ZoomableTimeline
              class="row-track"
              :from-ms="windowFromMs"
              :to-ms="windowToMs"
              :recorded="row.recorded"
              :events="row.events"
              :playhead-ms="playheadMs"
              @seek="(ms) => onRowSeek(row.cam.id, ms)"
              @select="(ms, camId) => onRowSelect(camId ?? row.cam.id, ms)"
              @open="(ms, camId) => onRowOpen(camId ?? row.cam.id, ms)"
              @window-change="onRowWindowChange"
            />
          </div>
        </div>

        <p class="muted small hint">
          Click a camera tab above to load it into the main player. Click a
          recording on the timeline (or click a pip) to load that camera at
          that time. Scroll on the timeline to zoom around the cursor; drag
          side to side to pan; double-click to open the full per-camera page.
          <span v-if="cameras.length > 1">Use ←/→ to cycle cameras.</span>
          In playback, the player's own control bar handles play/pause/seek
          and supports pinch (or Ctrl+scroll) zoom and drag-pan of the video.
        </p>
      </section>
    </template>
  </div>
</template>

<style scoped>
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
  align-items: baseline;
  gap: 0.6rem;
}

.page-title {
  margin: 0;
  font-size: 1.3rem;
  font-weight: 600;
}

.small {
  font-size: 0.8rem;
}

.muted {
  color: var(--text-muted);
}

.text-link {
  color: var(--accent);
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
}

.header-actions {
  display: flex;
  gap: 0.7rem;
  align-items: center;
}

.range-buttons,
.mode-buttons {
  display: flex;
  gap: 0.4rem;
}

.range-active {
  color: var(--text);
  border-color: var(--accent);
  background: rgba(79, 140, 255, 0.12);
}

.custom-range {
  font-style: italic;
}

.empty-centered {
  margin: 4rem auto 0;
}

.error-text {
  color: var(--danger);
}

.camera-switcher {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem;
  margin-bottom: 0.6rem;
  padding-bottom: 0.5rem;
  border-bottom: 1px solid var(--border);
}

.cam-chip {
  display: inline-flex;
  align-items: center;
  gap: 0.45rem;
  padding: 0.35rem 0.7rem;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 999px;
  font: inherit;
  font-size: 0.85rem;
  color: inherit;
  cursor: pointer;
  transition: border-color 0.12s ease, background 0.12s ease;
}

.cam-chip:hover {
  border-color: var(--accent);
}

.cam-chip-active {
  border-color: var(--accent);
  background: rgba(79, 140, 255, 0.12);
}

.cam-chip-offline {
  opacity: 0.55;
}

.cam-chip-name {
  font-weight: 500;
}

.cam-chip-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  margin-left: 0.15rem;
}

.player-area-wrap {
  margin-bottom: 1.5rem;
}

.live-wrap {
  position: relative;
  width: 100%;
  background: #000;
  border-radius: var(--radius);
  overflow: hidden;
  aspect-ratio: 16 / 9;
}

/* CameraStage fills .live-wrap (100% / 100%). */

.player-placeholder {
  width: 100%;
  height: 100%;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 1rem;
  text-transform: uppercase;
  letter-spacing: 0.1em;
}

.player-info {
  display: flex;
  align-items: center;
  gap: 0.7rem;
  margin-top: 0.5rem;
}

.player-cam-name {
  font-weight: 600;
}

.badge-rec {
  display: inline-block;
  padding: 0.05rem 0.4rem;
  font-size: 0.7rem;
  font-weight: 700;
  letter-spacing: 0.08em;
  color: #fff;
  background: var(--danger);
  border-radius: 3px;
}

.spacer {
  flex: 1;
}

.timeline-section {
  border-top: 1px solid var(--border);
  padding-top: 1rem;
}

.timeline-toolbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 0.5rem;
  gap: 1rem;
}

.toolbar-actions {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  flex-wrap: wrap;
  justify-content: flex-end;
}

.overlay-section {
  /* Single combined track. */
}

.stacked-section {
  display: flex;
  flex-direction: column;
  gap: 0.4rem;
}

.stacked-row {
  display: grid;
  grid-template-columns: 160px 1fr;
  gap: 0.75rem;
  align-items: center;
}

.stacked-row.row-offline {
  opacity: 0.55;
}

.row-label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.4rem 0.6rem;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 4px;
  font: inherit;
  color: inherit;
  cursor: pointer;
  text-align: left;
  min-width: 0;
}

.row-label:hover {
  border-color: var(--accent);
}

.row-label-active {
  border-color: var(--accent);
  background: rgba(79, 140, 255, 0.12);
}

.row-dot {
  display: inline-block;
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.row-name {
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
  flex: 1;
}

.hint {
  margin-top: 0.7rem;
  line-height: 1.45;
}

@media (max-width: 720px) {
  .stacked-row {
    grid-template-columns: 100px 1fr;
  }
}
</style>
