<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { post } from '../api/client'
import { startWebRTC } from '../lib/webrtc'

const props = withDefaults(
  defineProps<{
    cameraId: string
    stream?: 'sub' | 'main'
    muted?: boolean
  }>(),
  {
    stream: 'sub',
    muted: true,
  },
)

const emit = defineEmits<{
  (e: 'mode', mode: 'webrtc' | 'mjpeg'): void
}>()

const mode = ref<'webrtc' | 'mjpeg'>('webrtc')
const videoEl = ref<HTMLVideoElement | null>(null)

let cleanup: (() => void) | null = null

const mjpegUrl = () => `/api/v1/streams/${props.cameraId}/mjpeg`

function setMode(m: 'webrtc' | 'mjpeg') {
  if (mode.value === m) return
  mode.value = m
  emit('mode', m)
}

async function start() {
  stop()
  const video = videoEl.value
  if (!video) return
  cleanup = await startWebRTC(
    video,
    props.cameraId,
    props.stream,
    { post: (path, body) => post(path, body) },
    () => setMode('mjpeg'),
  )
}

function stop() {
  if (cleanup) {
    cleanup()
    cleanup = null
  }
}

onMounted(start)

watch(
  () => [props.cameraId, props.stream],
  () => {
    mode.value = 'webrtc'
    start()
  },
)

onBeforeUnmount(stop)

defineExpose({ mode })
</script>

<template>
  <div class="live-player">
    <video
      v-show="mode === 'webrtc'"
      ref="videoEl"
      class="live-media"
      autoplay
      playsinline
      :muted="muted"
    ></video>
    <img v-if="mode === 'mjpeg'" :src="mjpegUrl()" class="live-media" alt="live stream" />
    <span class="live-badge" :class="{ 'live-badge-mjpeg': mode === 'mjpeg' }">
      <span class="live-dot" aria-hidden="true"></span>
      Live
    </span>
    <span v-if="mode === 'mjpeg'" class="live-note">MJPEG fallback</span>
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

.live-media {
  display: block;
  width: 100%;
  height: 100%;
  object-fit: cover;
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
}
</style>
