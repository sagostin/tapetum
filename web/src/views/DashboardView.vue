<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { get } from '../api/client'
import type { Camera, CameraListResponse } from '../api/types'
import StatusBadge from '../components/StatusBadge.vue'
import LivePlayer from '../components/LivePlayer.vue'

const cameras = ref<Camera[]>([])
const loading = ref(true)
const loadError = ref('')

let pollTimer: ReturnType<typeof setInterval> | null = null

async function refresh() {
  try {
    const res = await get<CameraListResponse>('/cameras')
    cameras.value = res.cameras ?? []
    loadError.value = ''
  } catch {
    if (!cameras.value.length) loadError.value = 'Failed to load cameras'
  } finally {
    loading.value = false
  }
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
    <div class="page-header">
      <h1 class="page-title">Dashboard</h1>
    </div>

    <p v-if="loadError" class="error-text">{{ loadError }}</p>
    <p v-else-if="loading" class="muted">Loading cameras…</p>

    <div v-else-if="!cameras.length" class="empty-state empty-centered">
      <h2>No cameras yet</h2>
      <p>
        <router-link to="/cameras" class="text-link">Add your first camera</router-link>
        to see live streams here.
      </p>
    </div>

    <div v-else class="camera-grid">
      <router-link
        v-for="cam in cameras"
        :key="cam.id"
        :to="`/cameras/${cam.id}`"
        class="camera-tile"
      >
        <div class="tile-preview">
          <LivePlayer
            v-if="cam.enabled && cam.status !== 'offline'"
            :camera-id="cam.id"
            stream="sub"
          />
          <div v-else class="tile-placeholder">
            <span>{{ cam.enabled ? cam.status : 'disabled' }}</span>
          </div>
          <div class="tile-overlay">
            <span class="tile-name">{{ cam.name }}</span>
            <StatusBadge :status="cam.status" />
          </div>
        </div>
      </router-link>
    </div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1.25rem;
}

.page-title {
  margin: 0;
  font-size: 1.3rem;
  font-weight: 600;
}

.muted {
  color: var(--text-muted);
}

.empty-centered {
  margin: 4rem auto 0;
}

.text-link {
  color: var(--accent);
}

.camera-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 1rem;
}

.camera-tile {
  display: block;
  text-decoration: none;
  color: inherit;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  overflow: hidden;
  transition: border-color 0.15s ease;
}

.camera-tile:hover {
  border-color: var(--accent);
}

.tile-preview {
  position: relative;
  aspect-ratio: 16 / 9;
  background: #000;
}

.tile-preview :deep(.live-player) {
  position: absolute;
  inset: 0;
}

.tile-placeholder {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 0.85rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
}

.tile-overlay {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.5rem 0.7rem;
  background: linear-gradient(transparent, rgba(0, 0, 0, 0.75));
}

.tile-name {
  font-size: 0.9rem;
  font-weight: 600;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.8);
}
</style>
