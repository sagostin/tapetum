<script setup lang="ts">
/**
 * Live player.
 *
 * Streams a camera's main-stream access units as a continuous fragmented
 * MP4 byte stream (UniFi Protect-style) — the browser consumes it via
 * MediaSource Extensions, appendBuffer'ing each moof+mdat chunk as it
 * arrives. No ICE, no peer connections, no UDP, no STUN/TURN, no segment
 * boundaries mid-stream. Glass-to-glass latency matches the camera
 * pipeline (~50-200 ms beyond the network).
 *
 * Falls back to HLS (1s segments via hls.js) when fMP4 is unsupported or
 * the server returns an error. HLS, in turn, falls back to MJPEG.
 *
 * Audio isn't streamed live — it's recorded-only (recorder writes audio
 * into the segments, but the live path is video-only).
 */
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import Hls, { type ErrorData } from 'hls.js'

type Fit = 'contain' | 'cover'
type Mode = 'fmp4' | 'hls' | 'mjpeg'

const props = withDefaults(
  defineProps<{
    cameraId: string
    /** Ignored for live — the server only records/streams the main stream. Kept
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
  (e: 'mode', mode: Mode): void
}>()

const mode = ref<Mode>('fmp4')
const videoEl = ref<HTMLVideoElement | null>(null)
const rootEl = ref<HTMLDivElement | null>(null)

let hls: Hls | null = null
let mse: MediaSource | null = null
let mseSourceBuffer: SourceBuffer | null = null
let mseReader: ReadableStreamDefaultReader<Uint8Array> | null = null
let mseAbort: AbortController | null = null
let mseQueue: Uint8Array[] = []
let mseAppending = false
let destroyed = false
let observer: IntersectionObserver | null = null
let visible = false

function setMode(m: Mode) {
  if (mode.value === m) return
  mode.value = m
  emit('mode', m)
}

function mjpegUrl() {
  return `/api/v1/streams/${props.cameraId}/mjpeg`
}

function fmp4Url() {
  return `/api/v1/streams/${props.cameraId}/live.mp4`
}

function hlsUrl() {
  return `/api/v1/streams/${props.cameraId}/live.m3u8?transcode=h264`
}

function canPlayCodec(): boolean {
  // h264 High 4.2 is what most IP cameras emit; this is the broadest match.
  return MediaSource.isTypeSupported('video/mp4; codecs="avc1.640028"')
}

// --- fMP4 mode (Protect-style) ------------------------------------------

function startFmp4() {
  const video = videoEl.value
  if (!video || typeof MediaSource === 'undefined' || !canPlayCodec()) {
    return false
  }

  stopFmp4()
  mse = new MediaSource()
  mseAbort = new AbortController()
  video.src = URL.createObjectURL(mse)

  // SourceBuffer.addSourceBuffer requires the codec string up front, but
  // the camera's actual H.264 profile/level (e.g. High@5.0 = avc1.640032)
  // varies per stream and only lives inside the init segment. The server
  // echoes it in the Content-Type header, so we issue the fetch early and
  // pass the codec into addSourceBuffer once MediaSource opens.
  const initFetch = fetch(fmp4Url(), { signal: mseAbort.signal })

  mse.addEventListener('sourceopen', () => {
    void onSourceOpen(video, initFetch)
  })
  mse.addEventListener('error', () => {
    if (!destroyed) startHlsFallback()
  })
  return true
}

async function onSourceOpen(video: HTMLVideoElement, initFetch: Promise<Response>) {
  if (!mse || mseSourceBuffer) return

  // Default to a common H.264 profile; the server's Content-Type overrides
  // this when present.
  let mime = 'video/mp4; codecs="avc1.640028"'
  try {
    const res = await initFetch
    if (!res.ok || !res.body) {
      throw new Error(`HTTP ${res.status}`)
    }
    const ct = res.headers.get('Content-Type')
    if (ct && ct.includes('codecs=')) {
      mime = ct
    }
    // Some browsers lie about supporting certain levels; if the codec
    // string the server gave us isn't supported, fall back to High@4.0
    // (most IP cameras emit <= Level 5.1, which Chrome still accepts under
    // the High@4.0 string in practice).
    if (!MediaSource.isTypeSupported(mime)) {
      mime = 'video/mp4; codecs="avc1.640028"'
    }
    mseReader = res.body.getReader()
  } catch (err) {
    if (destroyed) return
    if (!(err instanceof DOMException) || err.name !== 'AbortError') {
      startHlsFallback()
    }
    return
  }

  if (destroyed || !mse) return

  try {
    mseSourceBuffer = mse.addSourceBuffer(mime)
  } catch {
    if (!destroyed) startHlsFallback()
    return
  }
  mseSourceBuffer.mode = 'segments' // appendBuffer is queued automatically

  pumpFmp4Chunks(video)
}

function pumpFmp4Chunks(video: HTMLVideoElement) {
  if (!mseReader || !mseSourceBuffer) return
  mseReader
    .read()
    .then(({ done, value }) => {
      if (done || destroyed) return
      if (value && value.byteLength > 0) {
        mseQueue.push(value)
        drainFmp4Queue(video)
      }
      // Schedule next read; loop back even on empty reads so we keep
      // pulling chunks as the server flushes them.
      pumpFmp4Chunks(video)
    })
    .catch(() => {
      // Network closed — fall back so the tile keeps showing something.
      if (!destroyed) startHlsFallback()
    })
}

function drainFmp4Queue(_video: HTMLVideoElement) {
  if (!mseSourceBuffer || mseAppending || mseQueue.length === 0) return
  // Concatenate queued chunks into one append — SourceBuffer can't have two
  // pending appendBuffer calls at once, so merging amortizes the overhead
  // and avoids callback juggling.
  let total = 0
  for (const c of mseQueue) total += c.byteLength
  const merged = new Uint8Array(total)
  let off = 0
  while (mseQueue.length > 0) {
    const c = mseQueue.shift()!
    merged.set(c, off)
    off += c.byteLength
  }
  mseAppending = true
  try {
    mseSourceBuffer.appendBuffer(merged.buffer)
  } catch {
    mseAppending = false
    if (!destroyed) startHlsFallback()
    return
  }
  // The 'updateend' event fires when SourceBuffer has fully ingested the
  // buffer; we then drain the next chunk or, when the buffer is empty and
  // the stream is ended, kick playback.
  const onUpdateEnd = () => {
    if (!mseSourceBuffer) return
    mseSourceBuffer.removeEventListener('updateend', onUpdateEnd)
    mseAppending = false
    if (videoEl.value && videoEl.value.paused) {
      videoEl.value.play().catch(() => {})
    }
    drainFmp4Queue(videoEl.value!)
  }
  mseSourceBuffer.addEventListener('updateend', onUpdateEnd)
}

function stopFmp4() {
  if (mseAbort) {
    mseAbort.abort()
    mseAbort = null
  }
  if (mseReader) {
    mseReader.cancel().catch(() => {})
    mseReader = null
  }
  if (mseSourceBuffer) {
    try {
      mseSourceBuffer.abort()
    } catch {
      // ignore
    }
    mseSourceBuffer = null
  }
  if (mse) {
    try {
      if (mse.readyState === 'open') mse.endOfStream()
    } catch {
      // ignore
    }
    mse = null
  }
  mseQueue = []
  mseAppending = false
  if (videoEl.value) {
    if (videoEl.value.src.startsWith('blob:')) {
      URL.revokeObjectURL(videoEl.value.src)
    }
    videoEl.value.removeAttribute('src')
    videoEl.value.load()
  }
}

// --- HLS fallback (hls.js over the existing endpoint) -------------------

function startHlsFallback() {
  stopFmp4()
  const video = videoEl.value
  if (!video) return
  setMode('hls')

  if (Hls.isSupported()) {
    const instance = new Hls({
      liveSyncDurationCount: 1,
      maxBufferLength: 1,
      maxMaxBufferLength: 1,
      backBufferLength: 0,
      manifestLoadingMaxRetry: 2,
      levelLoadingMaxRetry: 2,
      fragLoadingMaxRetry: 2,
      fragLoadingMaxRetryTimeout: 8000,
    })
    hls = instance
    instance.on(Hls.Events.MANIFEST_PARSED, () => {
      video.play().catch(() => {})
    })
    instance.on(Hls.Events.ERROR, (_e: typeof Hls.Events.ERROR, data: ErrorData) => {
      if (!data.fatal) return
      destroyHls()
      if (!destroyed) setMode('mjpeg')
    })
    instance.loadSource(hlsUrl())
    instance.attachMedia(video)
  } else if (video.canPlayType('application/vnd.apple.mpegurl')) {
    video.src = hlsUrl()
    video.addEventListener('loadedmetadata', () => video.play().catch(() => {}), { once: true })
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

function destroyHls() {
  if (hls) {
    hls.destroy()
    hls = null
  }
}

function stopHls() {
  destroyHls()
  if (videoEl.value) {
    videoEl.value.removeAttribute('src')
    videoEl.value.load()
  }
}

// --- orchestration ------------------------------------------------------

function start() {
  if (!visible) {
    return
  }
  // Idempotent: if start() is called while a previous run is still in
  // flight, just let it finish — tearing down and re-creating the MSE
  // source on every observer tick causes flicker.
  if (mse || hls) {
    return
  }
  // Prefer the Protect-style fMP4 path; fall through to HLS on error.
  setMode('fmp4')
  requestAnimationFrame(() => {
    if (destroyed || !visible) return
    if (!startFmp4()) {
      startHlsFallback()
    }
  })
}

function stop() {
  stopFmp4()
  stopHls()
}

onMounted(() => {
  if (rootEl.value && typeof IntersectionObserver !== 'undefined') {
    observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          visible = entry.isIntersecting
          if (visible) {
            start()
          } else {
            stop()
            if (mode.value === 'mjpeg') setMode('fmp4')
          }
        }
      },
      { threshold: 0.05 },
    )
    observer.observe(rootEl.value)
  } else {
    visible = true
  }
  // Always start on mount regardless of observer state — the observer only
  // gates scroll-away. Without this, tiles that mount while visible would
  // never bootstrap because mode is already 'fmp4' and the elses branch
  // only restarts from 'mjpeg'.
  start()
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
        v-show="mode === 'fmp4' || mode === 'hls'"
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