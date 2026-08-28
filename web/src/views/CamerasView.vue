<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { get, post, del, ApiError } from '../api/client'
import type {
  Camera,
  CameraListResponse,
  DiscoveredDevice,
  DiscoverMode,
  DiscoverRequest,
  DiscoverResponse,
} from '../api/types'
import { useAuthStore } from '../stores/auth'
import StatusBadge from '../components/StatusBadge.vue'
import CameraFormModal from '../components/CameraFormModal.vue'
import type { CameraPrefill } from '../components/CameraFormModal.vue'

const auth = useAuthStore()
const canWrite = computed(() => auth.user?.permissions.includes('cameras:write') ?? false)

const cameras = ref<Camera[]>([])
const loading = ref(true)
const loadError = ref('')

const showForm = ref(false)
const editingCamera = ref<Camera | null>(null)
const formPrefill = ref<CameraPrefill | null>(null)

const deletingCamera = ref<Camera | null>(null)
const deleteRecordings = ref(false)
const deleting = ref(false)
const deleteError = ref('')

const actionError = ref('')

// ---- ONVIF discovery ----
const showDiscover = ref(false)
const discovering = ref(false)
const discoverError = ref('')
const devices = ref<DiscoveredDevice[]>([])

// Discover form state. Mode = "multicast" (WS-Discovery, no creds),
// "network" (scan a CIDR with shared creds) or "host" (single IP/hostname).
const discoverMode = ref<DiscoverMode>('multicast')
const discoverTimeout = ref(5)
const discoverNetwork = ref('')
const discoverHost = ref('')
const discoverUsername = ref('')
const discoverPassword = ref('')

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
  formPrefill.value = null
  showForm.value = true
}

function openEdit(cam: Camera) {
  editingCamera.value = cam
  formPrefill.value = null
  showForm.value = true
}

async function onSaved() {
  showForm.value = false
  editingCamera.value = null
  formPrefill.value = null
  await refresh()
}

function openDiscover() {
  showDiscover.value = true
  discoverError.value = ''
  devices.value = []
  discover()
}

const discoverDisabled = computed(() => {
  if (discoverMode.value === 'network') return !discoverNetwork.value.trim()
  if (discoverMode.value === 'host') return !discoverHost.value.trim()
  return false
})

async function discover() {
  discovering.value = true
  discoverError.value = ''
  devices.value = []
  const body: DiscoverRequest = { mode: discoverMode.value }
  if (discoverMode.value === 'multicast') {
    body.timeout_s = discoverTimeout.value
  } else if (discoverMode.value === 'network') {
    body.network = discoverNetwork.value.trim()
  } else if (discoverMode.value === 'host') {
    body.host = discoverHost.value.trim()
  }
  if (discoverMode.value !== 'multicast') {
    if (discoverUsername.value) body.username = discoverUsername.value
    if (discoverPassword.value) body.password = discoverPassword.value
  }
  try {
    const res = await post<DiscoverResponse>('/cameras/discover', body)
    devices.value = res.devices ?? []
    if (!devices.value.length) {
      discoverError.value = discoverMode.value === 'multicast'
        ? 'No ONVIF devices found on the LAN.'
        : 'No ONVIF devices found at the given host(s).'
    }
  } catch (err) {
    discoverError.value = err instanceof ApiError ? err.message : 'Discovery failed'
  } finally {
    discovering.value = false
  }
}

// endpoints already adopted in the main camera list — derived from the last
// refresh so users can see which discovered devices are still pending.
const adoptedEndpoints = computed(() => {
  const s = new Set<string>()
  for (const c of cameras.value) {
    if (c.onvif_endpoint) s.add(c.onvif_endpoint)
  }
  return s
})

function adopt(dev: DiscoveredDevice) {
  // keep the discover modal open so the user can adopt more devices from the
  // same scan without re-running the probe
  editingCamera.value = null
  const usedCreds = discoverMode.value !== 'multicast'
  formPrefill.value = {
    name: dev.name || dev.hardware || '',
    onvif_endpoint: dev.endpoint,
    username: usedCreds ? discoverUsername.value : '',
    password: usedCreds ? discoverPassword.value : '',
  }
  showForm.value = true
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
      <div v-if="canWrite" class="header-actions">
        <button class="btn btn-ghost btn-inline" type="button" @click="openDiscover">Discover</button>
        <button class="btn btn-primary btn-inline" type="button" @click="openAdd">
          Add camera
        </button>
      </div>
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

    <div v-if="showDiscover" class="modal-backdrop" @click.self="showDiscover = false">
      <div class="modal card discover-modal">
        <h2 class="modal-title">Discover cameras</h2>

        <div class="mode-tabs" role="tablist">
          <button
            v-for="m in (['multicast', 'network', 'host'] as DiscoverMode[])"
            :key="m"
            type="button"
            role="tab"
            class="mode-tab"
            :class="{ active: discoverMode === m }"
            :aria-selected="discoverMode === m"
            @click="discoverMode = m"
          >
            {{
              m === 'multicast'
                ? 'Auto-discover'
                : m === 'network'
                ? 'Scan network'
                : 'Single host'
            }}
          </button>
        </div>

        <div v-if="discoverMode === 'multicast'" class="mode-body">
          <p class="muted hint">
            Sends a WS-Discovery multicast probe on the local network. No credentials
            are needed; cameras that don’t advertise ONVIF won’t be found.
          </p>
          <label class="field">
            <span>Timeout (s)</span>
            <input v-model.number="discoverTimeout" type="number" min="1" max="30" />
          </label>
        </div>

        <div v-else-if="discoverMode === 'network'" class="mode-body">
          <p class="muted hint">
            Probes every IPv4 address in the CIDR at common ONVIF ports (80, 8000,
            8080, 8899) using the credentials below. Use this when multicast is
            blocked or cameras are on another VLAN.
          </p>
          <label class="field">
            <span>Network (CIDR)</span>
            <input
              v-model="discoverNetwork"
              type="text"
              placeholder="192.168.1.0/24"
              spellcheck="false"
            />
          </label>
        </div>

        <div v-else class="mode-body">
          <p class="muted hint">
            Probes a single host (IP or hostname) at common ONVIF ports using the
            credentials below.
          </p>
          <label class="field">
            <span>Host</span>
            <input
              v-model="discoverHost"
              type="text"
              placeholder="192.168.1.50 or camera.lan"
              spellcheck="false"
            />
          </label>
        </div>

        <div v-if="discoverMode !== 'multicast'" class="field-row">
          <label class="field">
            <span>Username</span>
            <input v-model="discoverUsername" type="text" autocomplete="off" />
          </label>
          <label class="field">
            <span>Password</span>
            <input v-model="discoverPassword" type="password" autocomplete="new-password" />
          </label>
        </div>

        <div class="discover-status">
          <p v-if="discovering" class="muted">
            <span class="spinner" aria-hidden="true"></span>
            {{
              discoverMode === 'multicast'
                ? 'Scanning the LAN…'
                : discoverMode === 'network'
                ? 'Probing every host on the network…'
                : 'Probing host…'
            }}
          </p>
          <p v-else-if="discoverError" class="error-text">{{ discoverError }}</p>
          <p v-else-if="devices.length" class="muted">
            Found {{ devices.length }} device{{ devices.length === 1 ? '' : 's' }}.
          </p>
        </div>

        <div v-if="devices.length" class="device-list">
          <div
            v-for="dev in devices"
            :key="dev.endpoint"
            class="device-row"
            :class="{ adopted: adoptedEndpoints.has(dev.endpoint) }"
          >
            <div class="device-info">
              <span class="device-name">
                {{ dev.name || dev.hardware || 'ONVIF device' }}
                <span v-if="adoptedEndpoints.has(dev.endpoint)" class="adopted-tag">already added</span>
              </span>
              <span v-if="dev.hardware && dev.hardware !== dev.name" class="device-sub">
                {{ dev.hardware }}
              </span>
              <span class="device-sub mono">{{ dev.endpoint }}</span>
            </div>
            <button
              class="btn btn-ghost btn-sm"
              type="button"
              :disabled="adoptedEndpoints.has(dev.endpoint)"
              @click="adopt(dev)"
            >
              Adopt
            </button>
          </div>
        </div>

        <div class="modal-actions">
          <button class="btn btn-ghost" type="button" @click="showDiscover = false">Close</button>
          <button
            class="btn btn-primary btn-inline"
            type="button"
            :disabled="discovering || discoverDisabled"
            @click="discover"
          >
            {{ discovering ? 'Scanning…' : 'Scan' }}
          </button>
        </div>
      </div>
    </div>

    <CameraFormModal
      v-if="showForm"
      :camera="editingCamera"
      :prefill="formPrefill"
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

.header-actions {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}

.device-list {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  margin-top: 0.75rem;
  max-height: 320px;
  overflow-y: auto;
}

.device-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  padding: 0.6rem 0.75rem;
  border: 1px solid var(--border);
  border-radius: 8px;
  background: var(--bg-elevated);
}

.device-row.adopted {
  opacity: 0.65;
}

.adopted-tag {
  margin-left: 0.5rem;
  padding: 0.05rem 0.4rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 4px;
  font-size: 0.7rem;
  font-weight: 500;
  color: var(--text-muted);
  text-transform: uppercase;
  letter-spacing: 0.04em;
  vertical-align: 1px;
}

.device-info {
  display: flex;
  flex-direction: column;
  gap: 0.1rem;
  min-width: 0;
}

.device-name {
  font-weight: 500;
}

.device-sub {
  font-size: 0.8rem;
  color: var(--text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
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

.discover-modal {
  max-width: 520px;
}

.mode-tabs {
  display: flex;
  gap: 0.25rem;
  margin-bottom: 0.9rem;
  border-bottom: 1px solid var(--border);
}

.mode-tab {
  flex: 1;
  padding: 0.5rem 0.6rem;
  background: transparent;
  border: none;
  border-bottom: 2px solid transparent;
  color: var(--text-muted);
  font-size: 0.88rem;
  font-family: inherit;
  cursor: pointer;
  transition: color 0.15s ease, border-color 0.15s ease;
}

.mode-tab:hover {
  color: var(--text);
}

.mode-tab.active {
  color: var(--accent);
  border-bottom-color: var(--accent);
}

.mode-body {
  margin-bottom: 0.9rem;
}

.mode-body .hint {
  font-size: 0.82rem;
  margin: 0 0 0.7rem;
  line-height: 1.4;
}

.discover-modal .field-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.7rem;
  margin-bottom: 0.9rem;
}

.discover-modal .field {
  display: flex;
  flex-direction: column;
  gap: 0.25rem;
  margin-bottom: 0.7rem;
}

.discover-modal .field span {
  font-size: 0.82rem;
  color: var(--text-muted);
}

.discover-modal input[type='text'],
.discover-modal input[type='number'],
.discover-modal input[type='password'] {
  width: 100%;
  padding: 0.5rem 0.7rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 0.92rem;
  font-family: inherit;
  outline: none;
}

.discover-modal input:focus {
  border-color: var(--accent);
}

.discover-status {
  min-height: 1.6rem;
  margin: 0.3rem 0 0.6rem;
  display: flex;
  align-items: center;
  gap: 0.5rem;
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

.spinner {
  display: inline-block;
  width: 12px;
  height: 12px;
  border: 2px solid var(--border);
  border-top-color: var(--text);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
  vertical-align: -2px;
  margin-right: 0.3rem;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}
</style>
