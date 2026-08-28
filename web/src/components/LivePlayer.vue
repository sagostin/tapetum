<script setup lang="ts">
/**
 * UI3 live player.
 *
 * Streams a camera's recent fMP4 recordings as HLS via hls.js
 * (`GET /api/v1/streams/{cam}/live.m3u8`) — same plain HTTP, no ICE, no UDP,
 * no peer connections. Falls back to MJPEG (`<img>` multipart) if HLS fails
 * to start. The audio track isn't streamed live; audio is recorded-only.
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Hls, { type ErrorData } from 'hls.js'

type Fit = 'contain' | 'cover'

const props = withDefaults(
  defineProps<{
    cameraId: string
    /** Ignored for live HLS — the server only records the main stream. Kept
     *  for API compatibility with the previous player. */
    stream?: 'sub' | 'main'
    muted?: boolean
    /** When true, the "Live" badge and MJPEG-fallback tag are hidden. */
    hideBadge?: boolean
    /** object-fit mode — 'contain' = UI3 Fit, 'cover' = UI3 Fill. */
    fit?: Fit
    /** Rotation in degrees — only multiples of 90 are honored. */
    rotate?: number
    /** Mirror the video horizontally. */
    hflip?: boolean
    /** Mirror the video vertically. */
    vflip?: boolean
  }>(),
  {
    stream: 'sub',
    muted: true,
    hideBadge: false,
    fit: 'contain',
    rotate: 0,
    hflip: false,
    vflip: false,
  },
)

const emit = defineEmits<{
  (e: 'mode', mode: 'hls' | 'mjpeg'): void
}>()

const mode = ref<'hls' | 'mjpeg'>('hls')
const videoEl = ref<HTMLVideoElement | null>(null)
const rootEl = ref<HTMLDivElement | null>(null)

let hls: Hls | null = null
let destroyed = false
let observer: IntersectionObserver | null = null
let visible = false
let pendingStart = false

function setMode(m: 'hls' | 'mjpeg') {
  if (mode.value === m) return
  mode.value = m
  emit('mode', m)
}

function mjpegUrl() {
  return `/api/v1/streams/${props.cameraId}/mjpeg`
}

function hlsUrl() {
  // ?transcode=h264 — server transcodes H.265 main segments on demand.
  return `/api/v1/streams/${props.cameraId}/live.m3u8?transcode=h264`
}

function destroy() {
  if (hls) {
    hls.destroy()
    hls = null
  }
}

function startHls() {
  const video = videoEl.value
  if (!video) return
  destroy()

  if (Hls.isSupported()) {
    // UI3-style low-latency live: ~1s segments on the server, sit on the
    // live edge with a 1-segment DVR window. hls.js's default of 3
    // segments of buffer adds ~3s of glass-to-glass latency on top of the
    // 1s segment target — drop both to 1.
    const instance = new Hls({
      liveSyncDurationCount: 1,
      maxBufferLength: 1,
      maxMaxBufferLength: 1,
      backBufferLength: 0,
      manifestLoadingMaxRetry: 3,
      levelLoadingMaxRetry: 3,
      fragLoadingMaxRetry: 3,
      // Don't retry a broken segment forever; give up so MJPEG fallback
      // can kick in instead of locking up the tile.
      fragLoadingMaxRetryTimeout: 8000,
    })
    hls = instance
    instance.on(Hls.Events.MANIFEST_PARSED, () => {
      video.play().catch(() => {
        // Autoplay blocked — user gesture can resume.
      })
    })
    instance.on(Hls.Events.ERROR, (_event: typeof Hls.Events.ERROR, data: ErrorData) => {
      if (!data.fatal) return
      destroy()
      if (!destroyed) setMode('mjpeg')
    })
    instance.loadSource(hlsUrl())
    instance.attachMedia(video)
  } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
    video.src = hlsUrl()
    video.addEventListener(
      'loadedmetadata',
      () => {
        video.play().catch(() => {})
      },
      { once: true },
    )
    video.addEventListener(
      'error',
      () => {
        if (!destroyed) setMode('mjpeg')
      },
      { once: true },
    )
  } else {
    setMode('mjpeg')
  }
}

function stopHls() {
  destroy()
  if (videoEl.value) {
    videoEl.value.removeAttribute('src')
    videoEl.value.load()
  }
}

function start() {
  if (!visible) {
    pendingStart = true
    return
  }
  setMode('hls')
  // Wait a tick so the video element is actually in the DOM (v-show toggles).
  requestAnimationFrame(() => {
    if (destroyed || !visible) return
    startHls()
  })
}

function stop() {
  stopHls()
}

onMounted(() => {
  if (rootEl.value && typeof IntersectionObserver !== 'undefined') {
    observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          visible = entry.isIntersecting
          if (visible) {
            if (pendingStart || mode.value === 'mjpeg') {
              pendingStart = false
              start()
            }
          } else {
            stop()
            if (mode.value === 'mjpeg') setMode('hls')
          }
        }
      },
      { threshold: 0.05 },
    )
    observer.observe(rootEl.value)
  } else {
    visible = true
    start()
  }
})

watch(
  () => [props.cameraId, props.stream],
  () => {
    start()
  },
)

const mediaTransform = computed(() => {
  const sx = props.hflip ? -1 : 1
  const sy = props.vflip ? -1 : 1
  if (props.rotate === 0) return sx === 1 && sy === 1 ? 'none' : `scale(${sx}, ${sy})`
  return `rotate(${props.rotate}deg) scale(${sx}, ${sy})`
})
const isRotated = computed(() => props.rotate === 90 || props.rotate === 270)

onBeforeUnmount(() => {
  destroyed = true
  stop()
  if (observer) {
    observer.disconnect()
    observer = null
  }
})

defineExpose({ mode })
</script>

<template>
  <div ref="rootEl" class="live-player" :class="{ 'live-rotated': isRotated }">
    <div class="live-frame">
      <video
        v-show="mode === 'hls'"
        ref="videoEl"
        class="live-media"
        :class="`live-fit-${fit}`"
        :style="{ transform: mediaTransform }"
        autoplay
        playsinline
        :muted="muted"
      ></video>
      <img
        v-if="mode === 'mjpeg'"
        :src="mjpegUrl()"
        class="live-media"
        :class="`live-fit-${fit}`"
        :style="{ transform: mediaTransform }"
        alt="live stream"
      />
    </div>
    <span v-if="!hideBadge" class="live-badge" :class="{ 'live-badge-mjpeg': mode === 'mjpeg' }">
      <span class="live-dot" aria-hidden="true"></span>
      Live
    </span>
    <span v-if="!hideBadge && mode === 'mjpeg'" class="live-note">MJPEG fallback</span>
  </div>
</template>

<style scoped>
.live-player {
  position: relative;
  width: 100%;
  height: 100%;
  background: #000;
  overflow: hidden;
}

.live-frame {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
}

.live-media {
  display: block;
  width: 100%;
  height: 100%;
  transition: transform 0.18s ease;
  transform-origin: center center;
}

.live-fit-contain {
  object-fit: contain;
  max-width: 100%;
  max-height: 100%;
}

.live-fit-cover {
  object-fit: cover;
}

.live-player.live-rotated .live-media {
  width: 100%;
  height: 100%;
}

.live-badge {
  position: absolute;
  top: 0.5rem;
  left: 0.5rem;
  display: flex;
  align-items: center;
  gap: 0.3rem;
  font-size: 0.68rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: #fff;
  background: rgba(0, 0, 0, 0.55);
  padding: 0.15rem 0.45rem;
  border-radius: 4px;
  pointer-events: none;
}

.live-dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: #4ade80;
}

.live-badge-mjpeg .live-dot {
  background: #fbbf24;
}

.live-note {
  position: absolute;
  top: 0.5rem;
  right: 0.5rem;
  font-size: 0.68rem;
  color: var(--text-muted);
  background: rgba(0, 0, 0, 0.55);
  padding: 0.15rem 0.45rem;
  border-radius: 4px;
  pointer-events: none;
}
</style>