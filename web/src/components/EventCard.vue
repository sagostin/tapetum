<script setup lang="ts">
import { computed, ref } from 'vue'
import type { Camera, TapEvent } from '../api/types'
import { ackEvent, deleteEvent, eventClipUrl, eventSnapshotUrl } from '../api/events'
import { formatDateTime } from '../utils/format'
import VideoPlayer from './VideoPlayer.vue'

const props = defineProps<{
  event: TapEvent
  cameras: Camera[]
  canDelete: boolean
  highlight?: boolean
}>()

const emit = defineEmits<{
  (e: 'updated', ev: TapEvent): void
  (e: 'deleted', id: string): void
}>()

const playing = ref(false)
const busy = ref(false)
const actionError = ref('')

const cameraName = computed(
  () => props.cameras.find((c) => c.id === props.event.camera_id)?.name ?? 'Camera',
)

const label = computed(() => {
  if (props.event.type === 'ai' && props.event.label) return props.event.label
  return props.event.type
})

const confidencePct = computed(() =>
  props.event.confidence != null ? `${Math.round(props.event.confidence * 100)}%` : null,
)

const hasSnapshot = computed(() => !!props.event.metadata?.snapshot_url)
const snapshotUrl = computed(() => eventSnapshotUrl(props.event.id))
const clipUrl = computed(() => eventClipUrl(props.event.id))
const acked = computed(() => !!props.event.acked_at)

async function ack() {
  busy.value = true
  actionError.value = ''
  try {
    await ackEvent(props.event.id)
    emit('updated', { ...props.event, acked_at: new Date().toISOString() })
  } catch {
    actionError.value = 'Ack failed'
  } finally {
    busy.value = false
  }
}

async function remove() {
  busy.value = true
  actionError.value = ''
  try {
    await deleteEvent(props.event.id)
    emit('deleted', props.event.id)
  } catch {
    actionError.value = 'Delete failed'
  } finally {
    busy.value = false
  }
}

function fmt(ts: string): string {
  return formatDateTime(new Date(ts).getTime())
}
</script>

<template>
  <div class="event-card card" :class="{ highlighted: highlight }">
    <div class="thumb-wrap" @click="playing = !playing">
      <img
        v-if="hasSnapshot && !playing"
        :src="snapshotUrl"
        class="thumb"
        alt="event snapshot"
        loading="lazy"
      />
      <VideoPlayer v-if="playing" :src="clipUrl" mode="playback" :autoplay="true" :muted="true" :show-controls="true" />
      <div v-if="!hasSnapshot && !playing" class="thumb thumb-empty">no snapshot</div>
      <span v-if="!playing && (event.clip_start || hasSnapshot)" class="play-hint">▶</span>
    </div>

    <div class="event-body">
      <div class="event-head">
        <span class="chip" :class="`chip-${event.type}`">{{ label }}</span>
        <span v-if="confidencePct" class="chip chip-conf">{{ confidencePct }}</span>
        <span v-if="acked" class="chip chip-acked">acked</span>
      </div>
      <div class="event-meta">
        <strong>{{ cameraName }}</strong>
        <span class="muted">{{ fmt(event.ts) }}</span>
        <span v-if="event.end_ts" class="muted">
          → {{ fmt(event.end_ts).slice(-8) }}
        </span>
      </div>
      <p v-if="actionError" class="error-text">{{ actionError }}</p>
      <div class="event-actions">
        <button v-if="!playing" class="btn btn-ghost btn-sm" type="button" @click="playing = true">
          Play clip
        </button>
        <button v-else class="btn btn-ghost btn-sm" type="button" @click="playing = false">
          Hide clip
        </button>
        <button v-if="!acked" class="btn btn-ghost btn-sm" type="button" :disabled="busy" @click="ack">
          Ack
        </button>
        <router-link class="btn btn-ghost btn-sm" :to="`/playback/${event.camera_id}`">
          Timeline
        </router-link>
        <button
          v-if="canDelete"
          class="btn btn-ghost btn-sm btn-danger"
          type="button"
          :disabled="busy"
          @click="remove"
        >
          Delete
        </button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.event-card {
  display: grid;
  grid-template-columns: 220px 1fr;
  gap: 1rem;
  padding: 0.9rem;
}

.highlighted {
  border-color: var(--accent);
}

.thumb-wrap {
  position: relative;
  aspect-ratio: 16 / 9;
  background: #000;
  border-radius: 6px;
  overflow: hidden;
  cursor: pointer;
}

.thumb {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}

.thumb-empty {
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 0.8rem;
}

.play-hint {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 1.6rem;
  color: rgba(255, 255, 255, 0.85);
  text-shadow: 0 1px 6px rgba(0, 0, 0, 0.8);
  pointer-events: none;
}

.event-body {
  display: flex;
  flex-direction: column;
  gap: 0.45rem;
  min-width: 0;
}

.event-head {
  display: flex;
  gap: 0.4rem;
}

.chip {
  padding: 0.15rem 0.55rem;
  border-radius: 999px;
  font-size: 0.75rem;
  font-weight: 600;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  color: var(--text-muted);
}

.chip-motion {
  color: #f59e0b;
  border-color: #785a1e;
}

.chip-ai {
  color: #ef4444;
  border-color: #7f1d1d;
}

.chip-conf,
.chip-acked {
  color: var(--text-muted);
}

.event-meta {
  display: flex;
  gap: 0.6rem;
  align-items: baseline;
  font-size: 0.9rem;
  flex-wrap: wrap;
}

.muted {
  color: var(--text-muted);
}

.error-text {
  color: var(--danger);
  font-size: 0.82rem;
  margin: 0;
}

.event-actions {
  display: flex;
  gap: 0.4rem;
  margin-top: auto;
  flex-wrap: wrap;
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
  width: auto;
}

.btn-danger {
  color: var(--danger);
}
</style>
