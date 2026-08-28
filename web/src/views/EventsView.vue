<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { get } from '../api/client'
import { listEvents } from '../api/events'
import type { Camera, TapEvent } from '../api/types'
import { useAuthStore } from '../stores/auth'
import { wsClient } from '../lib/ws'
import EventCard from '../components/EventCard.vue'

const route = useRoute()
const auth = useAuthStore()

const cameras = ref<Camera[]>([])
const events = ref<TapEvent[]>([])
const cursor = ref('')
const loading = ref(false)
const loadError = ref('')

// filters
const fCamera = ref((route.query.camera as string) ?? '')
const fType = ref('')
const fUnacked = ref(false)
const highlightId = ref((route.query.id as string) ?? '')

const canDelete = computed(() => auth.user?.permissions.includes('cameras:write') ?? false)

async function fetchCameras() {
  try {
    const res = await get<{ cameras: Camera[] }>('/cameras')
    cameras.value = res.cameras ?? []
  } catch {
    // names fall back to camera id
  }
}

async function fetchFeed(reset: boolean) {
  loading.value = true
  loadError.value = ''
  try {
    const res = await listEvents({
      camera: fCamera.value || undefined,
      type: fType.value || undefined,
      unacked: fUnacked.value || undefined,
      limit: 50,
      cursor: reset ? undefined : cursor.value || undefined,
    })
    if (reset) {
      events.value = res.events ?? []
    } else {
      events.value = [...events.value, ...(res.events ?? [])]
    }
    cursor.value = res.cursor
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : 'Failed to load events'
  } finally {
    loading.value = false
  }
}

function applyFilters() {
  fetchFeed(true)
}

function onUpdated(ev: TapEvent) {
  const i = events.value.findIndex((e) => e.id === ev.id)
  if (i >= 0) events.value[i] = ev
}

function onDeleted(id: string) {
  events.value = events.value.filter((e) => e.id !== id)
}

let offEvent: (() => void) | null = null

onMounted(() => {
  fetchCameras()
  fetchFeed(true)
  offEvent = wsClient.on('event.created', (data) => {
    const d = data as { id?: string; camera_id?: string; type?: string; ts?: string }
    if (!d?.id || !d.camera_id) return
    if (fCamera.value && d.camera_id !== fCamera.value) return
    if (fType.value && d.type !== fType.value) return
    events.value = [
      { id: d.id, camera_id: d.camera_id, type: d.type ?? 'motion', ts: d.ts ?? new Date().toISOString() },
      ...events.value,
    ]
  })
})

onBeforeUnmount(() => offEvent?.())
</script>

<template>
  <div>
    <div class="feed-header">
      <h1 class="page-title">Events</h1>
      <div class="filters">
        <select v-model="fCamera" class="filter-select" @change="applyFilters">
          <option value="">All cameras</option>
          <option v-for="c in cameras" :key="c.id" :value="c.id">{{ c.name }}</option>
        </select>
        <select v-model="fType" class="filter-select" @change="applyFilters">
          <option value="">All types</option>
          <option value="motion">Motion</option>
          <option value="ai">AI</option>
        </select>
        <label class="filter-check">
          <input v-model="fUnacked" type="checkbox" @change="applyFilters" />
          Unacked only
        </label>
      </div>
    </div>

    <p v-if="loadError" class="error-text">{{ loadError }}</p>

    <div class="feed">
      <EventCard
        v-for="e in events"
        :key="e.id"
        :event="e"
        :cameras="cameras"
        :can-delete="canDelete"
        :highlight="e.id === highlightId"
        @updated="onUpdated"
        @deleted="onDeleted"
      />
      <p v-if="!loading && events.length === 0" class="muted empty">
        No events yet. Enable motion detection on a camera to get started.
      </p>
    </div>

    <div class="feed-footer">
      <span v-if="loading" class="muted">Loading…</span>
      <button v-else-if="cursor" class="btn btn-ghost btn-inline" type="button" @click="fetchFeed(false)">
        Load more
      </button>
    </div>
  </div>
</template>

<style scoped>
.page-title {
  margin: 0;
  font-size: 1.3rem;
  font-weight: 600;
}

.feed-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1.25rem;
  flex-wrap: wrap;
}

.filters {
  display: flex;
  align-items: center;
  gap: 0.75rem;
}

.filter-select {
  padding: 0.45rem 0.6rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 0.88rem;
  font-family: inherit;
}

.filter-check {
  display: flex;
  align-items: center;
  gap: 0.4rem;
  font-size: 0.88rem;
  color: var(--text-muted);
}

.feed {
  display: flex;
  flex-direction: column;
  gap: 0.75rem;
}

.empty {
  text-align: center;
  padding: 3rem 0;
}

.feed-footer {
  display: flex;
  justify-content: center;
  padding: 1.25rem 0;
}

.btn-inline {
  width: auto;
}

.error-text {
  color: var(--danger);
}

.muted {
  color: var(--text-muted);
}
</style>
