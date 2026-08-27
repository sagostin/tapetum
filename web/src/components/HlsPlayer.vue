<script setup lang="ts">
import { onBeforeUnmount, ref, watch } from 'vue'
import Hls from 'hls.js'

const props = defineProps<{
  src: string
  autoplay?: boolean
  muted?: boolean
}>()

const emit = defineEmits<{
  (e: 'fatal-error', message: string): void
  (e: 'timeupdate', seconds: number): void
  (e: 'ended'): void
}>()

const videoEl = ref<HTMLVideoElement | null>(null)
const loading = ref(false)
const error = ref('')

let hls: Hls | null = null

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
      // Live playlists: stay close to the edge.
      liveSyncDurationCount: 3,
      manifestLoadingMaxRetry: 2,
      levelLoadingMaxRetry: 2,
      fragLoadingMaxRetry: 3,
    })
    hls = instance
    instance.on(Hls.Events.MANIFEST_PARSED, () => {
      loading.value = false
      if (props.autoplay !== false) {
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
        if (props.autoplay !== false) video.play().catch(() => {})
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
  if (video) emit('timeupdate', video.currentTime)
}

function onEnded() {
  emit('ended')
}

watch(
  () => props.src,
  (src) => load(src),
  { immediate: true },
)

onBeforeUnmount(() => destroy())

function play() {
  videoEl.value?.play().catch(() => {})
}

function pause() {
  videoEl.value?.pause()
}

defineExpose({ play, pause })
</script>

<template>
  <div class="hls-player">
    <video
      ref="videoEl"
      class="hls-video"
      :muted="muted !== false"
      controls
      playsinline
      @timeupdate="onTimeUpdate"
      @ended="onEnded"
    ></video>
    <div v-if="loading" class="hls-overlay">
      <span class="spinner" aria-hidden="true"></span>
      <span>Loading stream…</span>
    </div>
    <div v-else-if="error" class="hls-overlay hls-overlay-error">
      <span>{{ error }}</span>
    </div>
  </div>
</template>

<style scoped>
.hls-player {
  position: relative;
  width: 100%;
  background: #000;
  border-radius: var(--radius);
  overflow: hidden;
}

.hls-video {
  display: block;
  width: 100%;
  aspect-ratio: 16 / 9;
  background: #000;
}

.hls-overlay {
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

.hls-overlay-error {
  color: var(--danger);
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
  to {
    transform: rotate(360deg);
  }
}
</style>
