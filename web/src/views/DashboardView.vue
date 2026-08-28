<script setup lang="ts">
/**
 * UI3 dashboard wall.
 *
 * Every camera is rendered as a tile in a flexible grid. Tiles have no
 * borders (just a thin gap between them) and are sized to FIT (object-fit:
 * contain) by default. Per-tile rotate / H-flip / V-flip / Fit-Fill controls
 * are revealed on hover and persisted server-side via PATCH
 * /cameras/:id/display so they survive reloads and follow the user across
 * browsers. The wall scrolls vertically when there are more cameras than
 * fit on one screen.
 *
 * Data flow: the cameras Pinia store is the single source of truth. AppShell
 * subscribes to the `camera.status` WebSocket topic and pushes transitions
 * into the store. The wall loads the list once on mount and only re-fetches
 * when the page becomes visible again (catches camera CRUD that happened
 * while the tab was backgrounded) — no N+1 stats polling, no per-second
 * wholesale array replacement.
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { patchCameraDisplay } from '../api/cameras'
import { useCamerasStore } from '../stores/cameras'
import type { Camera, DisplayRotate } from '../api/types'
import StatusBadge from '../components/StatusBadge.vue'
import LivePlayer from '../components/LivePlayer.vue'

const router = useRouter()
const store = useCamerasStore()
const cameras = computed<Camera[]>(() => store.list)
const loading = computed(() => !store.loaded)
const loadError = ref('')
const streamSource = ref<'main' | 'sub'>('sub')
const globalFit = ref<'contain' | 'cover'>('contain')

let pollTimer: ReturnType<typeof setInterval> | null = null

async function refresh() {
  try {
    await store.refresh()
    loadError.value = ''
  } catch {
    if (!store.list.length) loadError.value = 'Failed to load cameras'
  }
}

// ---- navigation ----------------------------------------------------------

function openCamera(id: string) {
  router.push(`/cameras/${id}`)
}

// ---- display persistence -------------------------------------------------

const tileFit = ref<Record<string, 'contain' | 'cover'>>({})

function effectiveFit(cam: Camera): 'contain' | 'cover' {
  return tileFit.value[cam.id] ?? globalFit.value
}

function toggleTileFit(cam: Camera) {
  const cur = effectiveFit(cam)
  tileFit.value = {
    ...tileFit.value,
    [cam.id]: cur === 'contain' ? 'cover' : 'contain',
  }
}

const saveTimers: Record<string, ReturnType<typeof setTimeout>> = {}
function queueDisplaySave(
  cam: Camera,
  patch: { rotate?: DisplayRotate; hflip?: boolean; vflip?: boolean },
) {
  if (patch.rotate !== undefined) cam.display_rotate = patch.rotate
  if (patch.hflip !== undefined) cam.display_hflip = patch.hflip
  if (patch.vflip !== undefined) cam.display_vflip = patch.vflip
  store.upsert(cam)

  const id = cam.id
  if (saveTimers[id]) clearTimeout(saveTimers[id])
  saveTimers[id] = setTimeout(() => {
    patchCameraDisplay(id, patch).catch(() => {
      // On failure we leave the optimistic state; the next refresh will
      // reconcile. A toast would be nicer but keeps the wall uncluttered.
    })
  }, 200)
}

function rotateCw(cam: Camera) {
  const next: DisplayRotate =
    cam.display_rotate === 0
      ? 90
      : cam.display_rotate === 90
        ? 180
        : cam.display_rotate === 180
          ? 270
          : 0
  queueDisplaySave(cam, { rotate: next })
}

function rotateCcw(cam: Camera) {
  const next: DisplayRotate =
    cam.display_rotate === 0
      ? 270
      : cam.display_rotate === 270
        ? 180
        : cam.display_rotate === 180
          ? 90
          : 0
  queueDisplaySave(cam, { rotate: next })
}

function toggleHFlip(cam: Camera) {
  queueDisplaySave(cam, { hflip: !cam.display_hflip })
}

function toggleVFlip(cam: Camera) {
  queueDisplaySave(cam, { vflip: !cam.display_vflip })
}

function resetDisplay(cam: Camera) {
  queueDisplaySave(cam, {
    rotate: 0,
    hflip: false,
    vflip: false,
  })
}

function setStreamSource(s: 'main' | 'sub') {
  streamSource.value = s
}

onMounted(() => {
  refresh()
  // Slow reconciliation poll — catches cameras added/removed/renamed by
  // other admins while this tab was open. 30s is plenty; status changes
  // come over WS and don't need polling.
  pollTimer = setInterval(refresh, 30_000)
})

onBeforeUnmount(() => {
  if (pollTimer) clearInterval(pollTimer)
  for (const t of Object.values(saveTimers)) clearTimeout(t)
})

const onlineCount = computed(
  () => cameras.value.filter((c) => c.status === 'online').length,
)
</script>

<template>
  <div class="dashboard">
    <header class="wall-toolbar">
      <div class="toolbar-left">
        <h1 class="page-title">Dashboard</h1>
        <span class="muted small" v-if="cameras.length">
          {{ onlineCount }} / {{ cameras.length }} online
        </span>
      </div>
      <div class="toolbar-right">
        <div class="tool-group">
          <button
            class="tool-btn"
            :class="{ 'tool-btn-active': globalFit === 'contain' }"
            type="button"
            @click="globalFit = 'contain'"
            title="Fit (UI3) — fit every tile without cropping"
          >Fit</button>
          <button
            class="tool-btn"
            :class="{ 'tool-btn-active': globalFit === 'cover' }"
            type="button"
            @click="globalFit = 'cover'"
            title="Fill — crop tiles to fill the cell"
          >Fill</button>
        </div>
        <div class="tool-group">
          <button
            class="tool-btn"
            :class="{ 'tool-btn-active': streamSource === 'sub' }"
            type="button"
            @click="setStreamSource('sub')"
            title="Sub-stream (lower bandwidth)"
          >Sub</button>
          <button
            class="tool-btn"
            :class="{ 'tool-btn-active': streamSource === 'main' }"
            type="button"
            @click="setStreamSource('main')"
            title="Main stream (full resolution)"
          >Main</button>
        </div>
      </div>
    </header>

    <p v-if="loadError" class="error-text">{{ loadError }}</p>
    <p v-else-if="loading" class="muted">Loading cameras…</p>

    <div v-else-if="!cameras.length" class="empty-state empty-centered">
      <h2>No cameras yet</h2>
      <p>
        <router-link to="/cameras" class="text-link">Add your first camera</router-link>
        to see live streams here.
      </p>
    </div>

    <div v-else class="wall" :class="`wall-fit-${globalFit}`">
      <div
        v-for="cam in cameras"
        :key="cam.id"
        class="tile"
        :class="{
          'tile-offline': cam.status === 'offline' || !cam.enabled,
          'tile-rotated': cam.display_rotate === 90 || cam.display_rotate === 270,
        }"
        :title="`${cam.name} — ${cam.status}`"
        @click="openCamera(cam.id)"
      >
        <LivePlayer
          v-if="cam.enabled && cam.status !== 'offline'"
          :camera-id="cam.id"
          :stream="streamSource"
          hide-badge
          :fit="effectiveFit(cam)"
          :rotate="cam.display_rotate"
          :hflip="cam.display_hflip"
          :vflip="cam.display_vflip"
        />
        <div v-else class="tile-placeholder">
          <span>{{ cam.enabled ? cam.status : 'disabled' }}</span>
        </div>

        <div class="tile-meta">
          <span class="tile-name">{{ cam.name }}</span>
          <StatusBadge :status="cam.status" compact />
        </div>

        <div class="tile-controls" @click.stop>
          <button
            class="tile-btn"
            type="button"
            @click.stop="rotateCw(cam)"
            title="Rotate 90° clockwise"
            aria-label="Rotate clockwise"
          >↻</button>
          <button
            class="tile-btn"
            type="button"
            @click.stop="rotateCcw(cam)"
            title="Rotate 90° counter-clockwise"
            aria-label="Rotate counter-clockwise"
          >↺</button>
          <button
            class="tile-btn"
            type="button"
            :class="{ 'tool-btn-active': cam.display_hflip }"
            @click.stop="toggleHFlip(cam)"
            title="Horizontal flip"
            aria-label="Horizontal flip"
          >⇋</button>
          <button
            class="tile-btn"
            type="button"
            :class="{ 'tool-btn-active': cam.display_vflip }"
            @click.stop="toggleVFlip(cam)"
            title="Vertical flip"
            aria-label="Vertical flip"
          >⇅</button>
          <button
            class="tile-btn"
            type="button"
            :class="{ 'tool-btn-active': effectiveFit(cam) === 'cover' }"
            @click.stop="toggleTileFit(cam)"
            :title="effectiveFit(cam) === 'contain' ? 'Fill (crop to tile)' : 'Fit (letterbox)'"
            :aria-label="effectiveFit(cam) === 'contain' ? 'Fill' : 'Fit'"
          >⛶</button>
          <button
            v-if="cam.display_rotate || cam.display_hflip || cam.display_vflip"
            class="tile-btn tile-btn-reset"
            type="button"
            @click.stop="resetDisplay(cam)"
            title="Reset rotation and flips"
            aria-label="Reset"
          >×</button>
        </div>
      </div>
    </div>
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

.small {
  font-size: 0.8rem;
}

.text-link {
  color: var(--accent);
}

.error-text {
  color: var(--danger);
}

.empty-centered {
  margin: 4rem auto 0;
}

/* ---- Sticky toolbar (UI3 wall controls) ---- */

.wall-toolbar {
  position: sticky;
  top: 0;
  z-index: 10;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  padding: 0.5rem 0;
  margin-bottom: 0.75rem;
  background: rgba(15, 17, 21, 0.85);
  backdrop-filter: blur(8px);
  -webkit-backdrop-filter: blur(8px);
  flex-wrap: wrap;
}

.toolbar-left,
.toolbar-right {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}

.tool-group {
  display: inline-flex;
  border: 1px solid var(--border);
  border-radius: 6px;
  overflow: hidden;
}

.tool-btn {
  background: transparent;
  color: var(--text-muted);
  border: none;
  padding: 0.35rem 0.7rem;
  font: inherit;
  font-size: 0.82rem;
  cursor: pointer;
  display: inline-flex;
  align-items: center;
  gap: 0.35rem;
  transition: background 0.12s ease, color 0.12s ease;
}

.tool-btn:hover {
  color: var(--text);
  background: rgba(255, 255, 255, 0.04);
}

.tool-btn-active {
  color: var(--text);
  background: var(--bg-elevated);
}

/* ---- The wall itself: scrollable grid of borderless tiles ---- */

.wall {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(min(320px, 100%), 1fr));
  gap: 4px;
  align-content: start;
  padding-bottom: 2rem;
}

.tile {
  position: relative;
  aspect-ratio: 16 / 9;
  background: #000;
  overflow: hidden;
  cursor: pointer;
  transition: box-shadow 0.18s ease;
}

.tile-rotated {
  aspect-ratio: 9 / 16;
}

.tile:hover {
  box-shadow: 0 0 0 1px var(--accent) inset;
}

.tile-offline {
  cursor: default;
}

/* LivePlayer fills the tile; pointer-events none so clicks bubble to .tile. */
.tile :deep(.live-player) {
  position: absolute;
  inset: 0;
  pointer-events: none;
}

.tile :deep(.live-frame) {
  pointer-events: none;
}

/* Offline / disabled cameras. */
.tile-placeholder {
  position: absolute;
  inset: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  color: var(--text-muted);
  font-size: 0.78rem;
  text-transform: uppercase;
  letter-spacing: 0.08em;
}

/* Bottom-left camera name + status. */
.tile-meta {
  position: absolute;
  left: 0.5rem;
  bottom: 0.5rem;
  right: 0.5rem;
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.5rem;
  background: linear-gradient(transparent, rgba(0, 0, 0, 0.6));
  padding: 0.4rem 0.3rem 0.1rem;
  margin: -0.4rem -0.3rem -0.1rem;
  pointer-events: none;
  opacity: 0;
  transition: opacity 0.18s ease;
}

.tile:hover .tile-meta,
.tile:focus-within .tile-meta {
  opacity: 1;
}

.tile-name {
  font-size: 0.82rem;
  font-weight: 500;
  color: #fff;
  text-shadow: 0 1px 2px rgba(0, 0, 0, 0.7);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  flex: 1;
  min-width: 0;
}

/* Hover-revealed icon row, top-right corner. */
.tile-controls {
  position: absolute;
  top: 0.4rem;
  right: 0.4rem;
  display: flex;
  align-items: center;
  gap: 2px;
  padding: 2px;
  background: rgba(0, 0, 0, 0.55);
  border-radius: 6px;
  opacity: 0;
  transition: opacity 0.18s ease;
  pointer-events: none;
}

.tile:hover .tile-controls,
.tile:focus-within .tile-controls {
  opacity: 1;
  pointer-events: auto;
}

.tile-btn {
  width: 26px;
  height: 26px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  color: #fff;
  border: none;
  border-radius: 4px;
  font: inherit;
  font-size: 0.95rem;
  line-height: 1;
  cursor: pointer;
  transition: background 0.12s ease, color 0.12s ease;
}

.tile-btn:hover {
  background: rgba(255, 255, 255, 0.12);
}

.tile-btn-active {
  background: var(--accent);
  color: #fff;
}

.tile-btn-reset {
  color: var(--text-muted);
}

.tile-btn-reset:hover {
  color: var(--danger);
  background: rgba(255, 92, 108, 0.18);
}

/* Mobile / narrow screens: stack tiles in a single column. */
@media (max-width: 660px) {
  .wall {
    grid-template-columns: 1fr;
  }
  .tile-meta {
    opacity: 1;
  }
  .tile-controls {
    opacity: 1;
    pointer-events: auto;
  }
}
</style>
