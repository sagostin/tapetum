<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { get } from '../api/client'
import type { Camera, CameraStats } from '../api/types'
import { useAuthStore } from '../stores/auth'
import { formatBytes, formatDuration } from '../utils/format'
import StatusBadge from '../components/StatusBadge.vue'
import HlsPlayer from '../components/HlsPlayer.vue'

const route = useRoute()
const auth = useAuthStore()
const cameraId = route.params.id as string

const canPlayback = computed(() => auth.user?.permissions.includes('playback') ?? false)

const camera = ref<Camera | null>(null)
const stats = ref<CameraStats | null>(null)
const loadError = ref('')
const hlsFailed = ref(false)

let pollTimer: ReturnType<typeof setInterval> | null = null

const liveUrl = computed(() => `/api/v1/streams/${cameraId}/live.m3u8`)
const mjpegUrl = computed(() => `/api/v1/streams/${cameraId}/mjpeg`)

const status = computed(() => stats.value?.status ?? camera.value?.status ?? 'offline')

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

function onHlsFatal() {
  hlsFailed.value = true
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
        <router-link
          v-if="canPlayback"
          :to="`/playback/${cameraId}`"
          class="btn btn-primary btn-inline"
        >
          Playback
        </router-link>
      </div>

      <div class="player-wrap">
        <HlsPlayer v-if="!hlsFailed" :src="liveUrl" @fatal-error="onHlsFatal" />
        <div v-else class="mjpeg-wrap">
          <img :src="mjpegUrl" :alt="camera.name" class="mjpeg-img" />
          <span class="mjpeg-note">HLS unavailable — showing MJPEG fallback</span>
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
  text-decoration: none;
}

.player-wrap {
  max-width: 960px;
  margin-bottom: 1.5rem;
}

.mjpeg-wrap {
  position: relative;
  background: #000;
  border-radius: var(--radius);
  overflow: hidden;
}

.mjpeg-img {
  display: block;
  width: 100%;
  aspect-ratio: 16 / 9;
  object-fit: contain;
}

.mjpeg-note {
  position: absolute;
  top: 0.6rem;
  left: 0.6rem;
  font-size: 0.75rem;
  color: var(--text-muted);
  background: rgba(0, 0, 0, 0.6);
  padding: 0.2rem 0.5rem;
  border-radius: 4px;
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
