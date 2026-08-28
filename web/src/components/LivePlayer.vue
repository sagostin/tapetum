<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { post } from '../api/client'
import { startWebRTC } from '../lib/webrtc'
import { acquireWebRTCSlot, releaseWebRTCSlot } from '../lib/webrtc-slot'
import type { DisplayRotate } from '../api/types'

type Fit = 'contain' | 'cover'

const props = withDefaults(
  defineProps<{
    cameraId: string
    stream?: 'sub' | 'main'
    muted?: boolean
    /** When true, the "Live" badge and MJPEG-fallback tag are hidden. */
    hideBadge?: boolean
    /** object-fit mode — 'contain' = UI3 Fit, 'cover' = UI3 Fill. */
    fit?: Fit
    /** Rotation in degrees — only multiples of 90 are honored. */
    rotate?: DisplayRotate
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
  (e: 'mode', mode: 'webrtc' | 'mjpeg'): void
}>()

const mode = ref<'webrtc' | 'mjpeg'>('webrtc')
const videoEl = ref<HTMLVideoElement | null>(null)
const rootEl = ref<HTMLDivElement | null>(null)

let cleanup: (() => void) | null = null
let inFlight = false
let visible = false
let destroyed = false
let observer: IntersectionObserver | null = null
let pendingStart = false

const mjpegUrl = () => `/api/v1/streams/${props.cameraId}/mjpeg`

function setMode(m: 'webrtc' | 'mjpeg') {
  if (mode.value === m) return
  mode.value = m
  emit('mode', m)
}

async function start() {
  // Don't even attempt WebRTC for off-screen tiles — saves a full RTCPeerConnection
  // setup (~2s of ICE gathering on the main thread) per hidden tile and stops the
  // dashboard from locking up on first paint with many cameras.
  if (!visible) {
    pendingStart = true
    return
  }
  if (inFlight) return
  stop()
  const video = videoEl.value
  if (!video) return
  inFlight = true
  await acquireWebRTCSlot()
  try {
    // Navigated away / scrolled off while queued for a slot — don't set up
    // a connection nobody is looking at.
    if (destroyed || !visible) return
    const c = await startWebRTC(
      video,
      props.cameraId,
      props.stream,
      { post: (path, body) => post(path, body) },
      () => setMode('mjpeg'),
    )
    // Unmounted or hidden while setup was in flight — close the connection
    // immediately instead of leaking a stream that keeps downloading.
    if (destroyed || !visible) {
      c()
      return
    }
    cleanup = c
  } finally {
    inFlight = false
    releaseWebRTCSlot()
  }
}

function stop() {
  if (cleanup) {
    cleanup()
    cleanup = null
  }
}

onMounted(() => {
  // Observe visibility on the wrapper (the <video> itself is hidden until webrtc
  // mode wins, so its bounding box is 0×0 and the observer would never fire).
  if (rootEl.value && typeof IntersectionObserver !== 'undefined') {
    observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          visible = entry.isIntersecting
          if (visible) {
            if (pendingStart || !cleanup) {
              pendingStart = false
              start()
            }
          } else {
            // Free the slot + connection immediately when scrolled away.
            stop()
            // MJPEG mode: stop() can't close the <img>'s multipart stream —
            // flipping back to webrtc removes the img from the DOM, which
            // does. On scroll-in, WebRTC is retried (and re-falls-back if
            // still unavailable).
            if (mode.value === 'mjpeg') setMode('webrtc')
          }
        }
      },
      { threshold: 0.05 },
    )
    observer.observe(rootEl.value)
  } else {
    // Fallback: assume visible so non-IntersectionObserver browsers still work.
    visible = true
    start()
  }
})

watch(
  () => [props.cameraId, props.stream],
  () => {
    mode.value = 'webrtc'
    start()
  },
)

// Compute the CSS transform that orients the media element. Rotate first,
// then flip on the rotated frame (so a 90° + hflip mirrors vertically,
// matching UI3's expectations).
const mediaTransform = computed(() => {
  const r = props.rotate || 0
  const sx = props.hflip ? -1 : 1
  const sy = props.vflip ? -1 : 1
  if (r === 0) {
    return sx === 1 && sy === 1 ? 'none' : `scale(${sx}, ${sy})`
  }
  return `rotate(${r}deg) scale(${sx}, ${sy})`
})

// Rotate 90 / 270 swap the tile aspect ratio; the wrapper applies this.
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
        v-show="mode === 'webrtc'"
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

/* live-frame hosts the media so the rotation transform sits on a 100%
   sized box while the player still flexes to whatever the parent wants. */
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

/* When the media is rotated 90/270, the source is square-tile-shaped in its
   own coordinate system but rotated to portrait. Constrain its visible
   bounds so it stays centered and clips symmetrically. */
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
