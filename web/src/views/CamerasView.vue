<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { get, post, del, ApiError } from '../api/client'
import type { Camera, CameraListResponse } from '../api/types'
import { useAuthStore } from '../stores/auth'
import StatusBadge from '../components/StatusBadge.vue'
import CameraFormModal from '../components/CameraFormModal.vue'

const auth = useAuthStore()
const canWrite = computed(() => auth.user?.permissions.includes('cameras:write') ?? false)

const cameras = ref<Camera[]>([])
const loading = ref(true)
const loadError = ref('')

const showForm = ref(false)
const editingCamera = ref<Camera | null>(null)

const deletingCamera = ref<Camera | null>(null)
const deleteRecordings = ref(false)
const deleting = ref(false)
const deleteError = ref('')

const actionError = ref('')

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

function openAdd() {
  editingCamera.value = null
  showForm.value = true
}

function openEdit(cam: Camera) {
  editingCamera.value = cam
  showForm.value = true
}

async function onSaved() {
  showForm.value = false
  editingCamera.value = null
  await refresh()
}

async function toggleEnabled(cam: Camera) {
  actionError.value = ''
  try {
    await post(`/cameras/${cam.id}/${cam.enabled ? 'disable' : 'enable'}`)
    await refresh()
  } catch (err) {
    actionError.value = err instanceof ApiError ? err.message : 'Failed to update camera'
  }
}

function confirmDelete(cam: Camera) {
  deletingCamera.value = cam
  deleteRecordings.value = false
  deleteError.value = ''
}

async function doDelete() {
  const cam = deletingCamera.value
  if (!cam) return
  deleting.value = true
  deleteError.value = ''
  try {
    const query = deleteRecordings.value ? '?delete_recordings=true' : ''
    await del(`/cameras/${cam.id}${query}`)
    deletingCamera.value = null
    await refresh()
  } catch (err) {
    deleteError.value = err instanceof ApiError ? err.message : 'Failed to delete camera'
  } finally {
    deleting.value = false
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
      <h1 class="page-title">Cameras</h1>
      <button v-if="canWrite" class="btn btn-primary btn-inline" type="button" @click="openAdd">
        Add camera
      </button>
    </div>

    <p v-if="actionError" class="error-text">{{ actionError }}</p>
    <p v-if="loadError" class="error-text">{{ loadError }}</p>
    <p v-else-if="loading" class="muted">Loading cameras…</p>

    <div v-else-if="!cameras.length" class="empty-state empty-centered">
      <h2>No cameras yet</h2>
      <p v-if="canWrite">Click "Add camera" to connect your first RTSP camera.</p>
    </div>

    <div v-else class="card table-card">
      <table class="cam-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Status</th>
            <th>Codec</th>
            <th>Record mode</th>
            <th>Retention</th>
            <th>Enabled</th>
            <th v-if="canWrite" class="actions-col">Actions</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="cam in cameras" :key="cam.id">
            <td>
              <router-link :to="`/cameras/${cam.id}`" class="cam-name">{{ cam.name }}</router-link>
            </td>
            <td><StatusBadge :status="cam.status" /></td>
            <td class="mono">{{ cam.status_detail?.codec?.toUpperCase() || '—' }}</td>
            <td class="mono">{{ cam.record_mode }}</td>
            <td class="mono">
              {{ cam.retention_days }}d{{ cam.retention_gb ? ` / ${cam.retention_gb}GB` : '' }}
            </td>
            <td>
              <button
                class="toggle"
                :class="{ on: cam.enabled }"
                type="button"
                role="switch"
                :aria-checked="cam.enabled"
                :disabled="!canWrite"
                @click="toggleEnabled(cam)"
              >
                <span class="toggle-knob"></span>
              </button>
            </td>
            <td v-if="canWrite" class="actions-col">
              <button class="btn btn-ghost btn-sm" type="button" @click="openEdit(cam)">Edit</button>
              <button class="btn btn-ghost btn-sm btn-danger" type="button" @click="confirmDelete(cam)">
                Delete
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <CameraFormModal
      v-if="showForm"
      :camera="editingCamera"
      @close="showForm = false"
      @saved="onSaved"
    />

    <div v-if="deletingCamera" class="modal-backdrop" @click.self="deletingCamera = null">
      <div class="modal card">
        <h2 class="modal-title">Delete {{ deletingCamera.name }}?</h2>
        <p class="muted">This removes the camera configuration. This action cannot be undone.</p>
        <label class="check-row">
          <input v-model="deleteRecordings" type="checkbox" />
          <span>Also delete recordings</span>
        </label>
        <p v-if="deleteError" class="error-text">{{ deleteError }}</p>
        <div class="modal-actions">
          <button class="btn btn-ghost" type="button" @click="deletingCamera = null">Cancel</button>
          <button class="btn btn-danger-solid" type="button" :disabled="deleting" @click="doDelete">
            {{ deleting ? 'Deleting…' : 'Delete' }}
          </button>
        </div>
      </div>
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

.btn-inline {
  width: auto;
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
}

.muted {
  color: var(--text-muted);
}

.empty-centered {
  margin: 4rem auto 0;
}

.table-card {
  padding: 0.5rem 1rem;
  overflow-x: auto;
}

.cam-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.92rem;
}

.cam-table th {
  text-align: left;
  font-size: 0.78rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
  padding: 0.8rem 0.75rem;
  border-bottom: 1px solid var(--border);
}

.cam-table td {
  padding: 0.7rem 0.75rem;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}

.cam-table tbody tr:last-child td {
  border-bottom: none;
}

.cam-name {
  color: var(--text);
  font-weight: 500;
  text-decoration: none;
}

.cam-name:hover {
  color: var(--accent);
}

.mono {
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 0.85rem;
  color: var(--text-muted);
}

.actions-col {
  text-align: right;
  white-space: nowrap;
}

.btn-danger {
  color: var(--danger);
}

.btn-danger:hover {
  border-color: var(--danger);
  color: var(--danger);
}

.toggle {
  position: relative;
  width: 38px;
  height: 22px;
  border-radius: 11px;
  border: 1px solid var(--border);
  background: var(--bg-elevated);
  cursor: pointer;
  padding: 0;
  transition: background 0.15s ease;
}

.toggle:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.toggle.on {
  background: var(--accent);
  border-color: var(--accent);
}

.toggle-knob {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--text-muted);
  transition: transform 0.15s ease, background 0.15s ease;
}

.toggle.on .toggle-knob {
  transform: translateX(16px);
  background: #fff;
}

.modal-backdrop {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.6);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 1.5rem;
  z-index: 100;
}

.modal {
  width: 100%;
  max-width: 440px;
}

.modal-title {
  margin: 0 0 0.75rem;
  font-size: 1.15rem;
  font-weight: 600;
}

.check-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin: 1rem 0;
  font-size: 0.92rem;
  color: var(--text-muted);
  cursor: pointer;
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.6rem;
  margin-top: 1.25rem;
}

.btn-danger-solid {
  background: var(--danger);
  color: #fff;
}

.btn-danger-solid:hover:not(:disabled) {
  filter: brightness(1.1);
}
</style>
