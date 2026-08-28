<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Hls from 'hls.js'
import { useZoomPan } from '../composables/useZoomPan'

/**
 * UI3-style video player.
 *
 * Wraps a bare <video> (no native controls) in a CSS-transform container
 * so we can digital-zoom and pan around the footage independently of the
 * underlying stream. Includes a built-in control bar (play/pause, seek,
 * scrubber, time, fullscreen, reset) that can be hidden when the player
 * is just streaming live.
 *
 * Source semantics:
 *   - `src` is the current URL.
 *   - `mode: 'live' | 'playback'` controls whether the playback bar is shown.
 *   - When switching to 'playback', set `initialSeekMs` to seek on load.
 *   - Pass `autoplay` to attempt play() on load (subject to browser policy).
 */

const props = withDefaults(
  defineProps<{
    src: string
    mode?: 'live' | 'playback'
    initialSeekMs?: number | null
    autoplay?: boolean
    muted?: boolean
    /** When true, also render the live badge overlay. */
    showLiveBadge?: boolean
    /** When true, allow programmatic fullscreen via the control bar. */
    allowFullscreen?: boolean
  }>(),
  {
    mode: 'playback',
    initialSeekMs: null,
    autoplay: false,
    muted: true,
    showLiveBadge: false,
    allowFullscreen: true,
  },
)

const emit = defineEmits<{
  (e: 'fatal-error', message: string): void
  (e: 'timeupdate', seconds: number): void
  (e: 'durationchange', seconds: number): void
  (e: 'playing'): void
  (e: 'paused'): void
  (e: 'ended'): void
  (e: 'loaded'): void
}>()

const videoEl = ref<HTMLVideoElement | null>(null)
const containerEl = ref<HTMLElement | null>(null)
const loading = ref(false)
const error = ref('')
const isPlaying = ref(false)
const currentTimeS = ref(0)
const durationS = ref(0)
const isFullscreen = ref(false)

let hls: Hls | null = null

const zoom = useZoomPan({ minZoom: 1, maxZoom: 8 })

const videoStyle = computed(() => ({
  transform: zoom.transform.value,
  cursor: cursorStyle.value,
}))

const cursorStyle = computed(() => {
  if (zoom.isPanning.value) return 'grabbing'
  if (zoom.isZoomed.value) return 'grab'
  return 'default'
})

function destroy() {
  if (hls) {
    hls.destroy()
    hls = null
  }
}

function load(src: string) {
  destroy()
  error.value = ''
  if (!src) return
  const video = videoEl.value
  if (!video) return

  loading.value = true

  if (Hls.isSupported()) {
    const instance = new Hls({
      liveSyncDurationCount: 3,
      backBufferLength: 120,
      manifestLoadingMaxRetry: 2,
      levelLoadingMaxRetry: 2,
      fragLoadingMaxRetry: 3,
    })
    hls = instance
    instance.on(Hls.Events.MANIFEST_PARSED, () => {
      loading.value = false
      // Some browsers / source-open orderings cause hls.js to skip its
      // autoStartLoad. Explicitly call startLoad to be safe.
      instance.startLoad(-1)
      // Seek to the requested point if provided.
      const seekTo = props.initialSeekMs
      if (seekTo != null && Number.isFinite(seekTo)) {
        // Convert ms → seconds and apply. We have to wait a tick so the
        // source buffer has loaded enough fragments to seek.
        video.currentTime = seekTo / 1000
      }
      if (props.autoplay) {
        video.play().catch(() => {
          // Autoplay blocked — user can press play.
        })
      }
    })
    instance.on(Hls.Events.ERROR, (_event, data) => {
      if (!data.fatal) return
      loading.value = false
      const message = data.details || data.type || 'Playback error'
      error.value = message
      destroy()
      emit('fatal-error', message)
    })
    instance.loadSource(src)
    instance.attachMedia(video)
  } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
    video.src = src
    video.addEventListener(
      'loadedmetadata',
      () => {
        loading.value = false
        const seekTo = props.initialSeekMs
        if (seekTo != null && Number.isFinite(seekTo)) {
          video.currentTime = seekTo / 1000
        }
        if (props.autoplay) video.play().catch(() => {})
      },
      { once: true },
    )
    video.addEventListener(
      'error',
      () => {
        loading.value = false
        error.value = 'Failed to load stream'
        emit('fatal-error', error.value)
      },
      { once: true },
    )
  } else {
    loading.value = false
    error.value = 'HLS is not supported in this browser'
    emit('fatal-error', error.value)
  }
}

function onTimeUpdate() {
  const video = videoEl.value
  if (!video) return
  currentTimeS.value = video.currentTime
  emit('timeupdate', video.currentTime)
}
function onDurationChange() {
  const video = videoEl.value
  if (video && Number.isFinite(video.duration)) {
    durationS.value = video.duration
    emit('durationchange', video.duration)
  }
}
function onEnded() {
  isPlaying.value = false
  emit('ended')
}
function onPlaying() {
  isPlaying.value = true
  emit('playing')
}
function onPause() {
  isPlaying.value = false
  emit('paused')
}
function onLoadedData() {
  emit('loaded')
  // duration may already be set; otherwise pick it up on durationchange.
}

watch(
  () => props.src,
  (src) => load(src),
)

onMounted(() => load(props.src))
onBeforeUnmount(() => destroy())

function togglePlay() {
  if (isPlaying.value) pause()
  else play()
}

function play() {
  return videoEl.value?.play().catch(() => {})
}

function pause() {
  videoEl.value?.pause()
}

function seek(seconds: number) {
  const video = videoEl.value
  if (!video) return
  video.currentTime = seconds
  currentTimeS.value = seconds
}

function seekRelative(seconds: number) {
  const next = Math.min(
    Math.max(0, currentTimeS.value + seconds),
    Number.isFinite(durationS.value) ? durationS.value : currentTimeS.value + seconds,
  )
  const wasPlaying = isPlaying.value
  seek(next)
  if (wasPlaying) play()
}

function seekToFraction(fraction: number) {
  if (!Number.isFinite(durationS.value) || durationS.value <= 0) return
  const next = Math.min(1, Math.max(0, fraction)) * durationS.value
  const wasPlaying = isPlaying.value
  seek(next)
  if (wasPlaying) play()
}

function formatHMS(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) return '0:00'
  const total = Math.floor(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  return `${m}:${String(s).padStart(2, '0')}`
}

const progressPct = computed(() => {
  if (!Number.isFinite(durationS.value) || durationS.value <= 0) return 0
  return Math.min(100, Math.max(0, (currentTimeS.value / durationS.value) * 100))
})

// ---- Scrubber ----

const scrubberEl = ref<HTMLElement | null>(null)
const scrubbing = ref(false)
function scrubFraction(e: PointerEvent): number | null {
  const el = scrubberEl.value
  if (!el) return null
  const rect = el.getBoundingClientRect()
  if (rect.width <= 0) return null
  return Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
}
function onScrubDown(e: PointerEvent) {
  if (!Number.isFinite(durationS.value) || durationS.value <= 0) return
  scrubbing.value = true
  const f = scrubFraction(e)
  if (f != null) seekToFraction(f)
  scrubberEl.value?.setPointerCapture(e.pointerId)
  window.addEventListener('pointermove', onScrubMove)
  window.addEventListener('pointerup', onScrubUp, { once: true })
}
function onScrubMove(e: PointerEvent) {
  if (!scrubbing.value) return
  const f = scrubFraction(e)
  if (f != null) seekToFraction(f)
}
function onScrubUp() {
  scrubbing.value = false
  window.removeEventListener('pointermove', onScrubMove)
}

// ---- Zoom / pan ----

function onWheel(e: WheelEvent) {
  e.preventDefault()
  const rect = containerEl.value?.getBoundingClientRect()
  if (!rect) return
  const cx = e.clientX - rect.left
  const cy = e.clientY - rect.top
  // ctrlKey = trackpad pinch; deltaY normal = scroll wheel.
  const factor = (e.deltaY < 0 ? 1.1 : 1 / 1.1)
  zoom.zoomAt(cx, cy, rect.width, rect.height, factor)
}

function onPointerDown(e: PointerEvent) {
  if (!zoom.isZoomed.value) return
  zoom.startPan(e.clientX, e.clientY, containerEl.value)
}

function zoomIn() {
  const rect = containerEl.value?.getBoundingClientRect()
  if (!rect) return
  zoom.zoomAt(rect.width / 2, rect.height / 2, rect.width, rect.height, 1.25)
}
function zoomOut() {
  const rect = containerEl.value?.getBoundingClientRect()
  if (!rect) return
  zoom.zoomAt(rect.width / 2, rect.height / 2, rect.width, rect.height, 1 / 1.25)
}
function zoomReset() {
  zoom.reset()
}

// ---- Fullscreen ----

function toggleFullscreen() {
  if (!props.allowFullscreen) return
  const el = containerEl.value
  if (!el) return
  if (!document.fullscreenElement) {
    el.requestFullscreen?.().then(() => {
      isFullscreen.value = true
    }).catch(() => {})
  } else {
    document.exitFullscreen?.().then(() => {
      isFullscreen.value = false
    }).catch(() => {})
  }
}

function onFullscreenChange() {
  isFullscreen.value = !!document.fullscreenElement
}

onMounted(() => {
  document.addEventListener('fullscreenchange', onFullscreenChange)
})
onBeforeUnmount(() => {
  document.removeEventListener('fullscreenchange', onFullscreenChange)
})

defineExpose({ play, pause, seek, seekRelative, seekToFraction, zoom })
</script>

<template>
  <div class="ui3-player">
    <div
      ref="containerEl"
      class="video-container"
      :class="{ 'is-zoomed': zoom.isZoomed.value, 'is-fullscreen': isFullscreen }"
      :style="videoStyle"
      @wheel="onWheel"
      @pointerdown="onPointerDown"
    >
      <video
        ref="videoEl"
        class="video"
        :muted="muted"
        playsinline
        preload="auto"
        @timeupdate="onTimeUpdate"
        @durationchange="onDurationChange"
        @ended="onEnded"
        @playing="onPlaying"
        @pause="onPause"
        @loadeddata="onLoadedData"
      ></video>

      <div v-if="loading" class="overlay overlay-loading">
        <span class="spinner" aria-hidden="true"></span>
        <span>Loading stream…</span>
      </div>
      <div v-else-if="error" class="overlay overlay-error">
        <span>{{ error }}</span>
      </div>

      <div v-if="showLiveBadge" class="live-badge">
        <span class="live-dot" aria-hidden="true"></span>
        LIVE
      </div>

      <div v-if="zoom.isZoomed.value" class="zoom-hint">{{ zoom.label.value }}</div>
    </div>

    <div v-if="mode === 'playback'" class="control-bar">
      <button
        class="ctrl"
        type="button"
        :title="isPlaying ? 'Pause (Space)' : 'Play (Space)'"
        :aria-label="isPlaying ? 'Pause' : 'Play'"
        @click="togglePlay"
      >
        <span v-if="isPlaying" aria-hidden="true">❚❚</span>
        <span v-else aria-hidden="true">▶</span>
      </button>
      <button
        class="ctrl ctrl-sm"
        type="button"
        title="Back 10s (←)"
        @click="seekRelative(-10)"
      >−10s</button>
      <button
        class="ctrl ctrl-sm"
        type="button"
        title="Forward 10s (→)"
        @click="seekRelative(10)"
      >+10s</button>
      <span class="time mono">{{ formatHMS(currentTimeS) }}</span>
      <div
        ref="scrubberEl"
        class="scrubber"
        :class="{ scrubbing }"
        role="slider"
        :aria-valuemin="0"
        :aria-valuemax="100"
        :aria-valuenow="Math.round(progressPct)"
        @pointerdown="onScrubDown"
      >
        <div class="scrubber-fill" :style="{ width: progressPct + '%' }"></div>
      </div>
      <span class="time mono">{{ formatHMS(durationS) }}</span>

      <div class="zoom-group">
        <button
          class="ctrl ctrl-sm"
          type="button"
          :disabled="!zoom.canZoomOut.value"
          title="Zoom out (−)"
          @click="zoomOut"
        >−</button>
        <span class="zoom-label mono" :title="zoom.label.value">{{ zoom.label.value }}</span>
        <button
          class="ctrl ctrl-sm"
          type="button"
          :disabled="!zoom.canZoomIn.value"
          title="Zoom in (+)"
          @click="zoomIn"
        >+</button>
        <button
          class="ctrl ctrl-sm"
          type="button"
          :disabled="!zoom.isZoomed.value"
          title="Reset zoom (0)"
          @click="zoomReset"
        >⤢</button>
        <button
          v-if="allowFullscreen"
          class="ctrl ctrl-sm"
          type="button"
          title="Fullscreen (F)"
          @click="toggleFullscreen"
        >⛶</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ui3-player {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
}

.video-container {
  position: relative;
  width: 100%;
  background: #000;
  border-radius: var(--radius);
  overflow: hidden;
  /* The video is the transform-child. `transform-origin: center center` keeps
     zooming anchored to the container's middle when no cursor event is
     available. */
  transform-origin: center center;
  touch-action: none;
  user-select: none;
}

.video-container.is-zoomed {
  cursor: grab;
}

.video-container.is-fullscreen {
  border-radius: 0;
}

.video {
  display: block;
  width: 100%;
  aspect-ratio: 16 / 9;
  background: #000;
  object-fit: contain;
  outline: none;
  /* Disable native right-click menu — our wrapper handles input. */
  -webkit-touch-callout: none;
}

.overlay {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 0.6rem;
  background: rgba(0, 0, 0, 0.55);
  color: var(--text-muted);
  font-size: 0.9rem;
  pointer-events: none;
}

.overlay-error {
  color: var(--danger);
}

.live-badge {
  position: absolute;
  top: 0.5rem;
  left: 0.5rem;
  display: flex;
  align-items: center;
  gap: 0.35rem;
  font-size: 0.7rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.08em;
  color: #fff;
  background: rgba(0, 0, 0, 0.55);
  padding: 0.18rem 0.5rem;
  border-radius: 4px;
  pointer-events: none;
}

.live-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #4ade80;
}

.zoom-hint {
  position: absolute;
  bottom: 0.6rem;
  right: 0.7rem;
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 0.78rem;
  color: #fff;
  background: rgba(0, 0, 0, 0.6);
  padding: 0.18rem 0.5rem;
  border-radius: 4px;
  pointer-events: none;
}

.spinner {
  width: 18px;
  height: 18px;
  border: 2px solid var(--border);
  border-top-color: var(--accent);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
}

@keyframes spin {
  to { transform: rotate(360deg); }
}

.control-bar {
  display: flex;
  align-items: center;
  gap: 0.55rem;
  padding: 0.35rem 0.6rem;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: 6px;
}

.ctrl {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  min-width: 2.2rem;
  height: 2rem;
  padding: 0 0.6rem;
  background: var(--bg-elevated);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: 4px;
  font: inherit;
  font-size: 0.85rem;
  cursor: pointer;
  transition: border-color 0.12s ease, background 0.12s ease;
}

.ctrl:hover:not([disabled]) {
  border-color: var(--accent);
}

.ctrl[disabled] {
  opacity: 0.4;
  cursor: not-allowed;
}

.ctrl-sm {
  min-width: 2.6rem;
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 0.78rem;
}

.time {
  min-width: 4.2rem;
  text-align: center;
  font-size: 0.82rem;
  color: var(--text-muted);
}

.scrubber {
  flex: 1;
  height: 8px;
  background: var(--bg-elevated);
  border-radius: 4px;
  cursor: pointer;
  position: relative;
  touch-action: none;
}

.scrubber.scrubbing {
  cursor: grabbing;
}

.scrubber-fill {
  position: absolute;
  top: 0;
  left: 0;
  bottom: 0;
  background: var(--accent);
  border-radius: 4px;
  pointer-events: none;
}

.zoom-group {
  display: inline-flex;
  align-items: center;
  gap: 0.3rem;
  padding-left: 0.5rem;
  margin-left: 0.4rem;
  border-left: 1px solid var(--border);
}

.zoom-label {
  min-width: 3.4rem;
  text-align: center;
  font-size: 0.82rem;
  color: var(--text-muted);
}

.mono {
  font-family: 'SF Mono', 'Menlo', monospace;
}
</style>
