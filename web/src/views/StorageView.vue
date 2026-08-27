<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { get, put, ApiError } from '../api/client'
import type { S3Settings, StorageOverview, SystemSettings } from '../api/types'
import { formatBytes } from '../utils/format'

const overview = ref<StorageOverview | null>(null)
const loadError = ref('')

const s3 = reactive<S3Settings & { secret_key: string }>({
  enabled: false,
  endpoint: '',
  secure: true,
  region: '',
  bucket: '',
  prefix: '',
  access_key: '',
  storage_class: '',
  secret_set: false,
  secret_key: '',
})
const saving = ref(false)
const saveError = ref('')
const savedOk = ref(false)

function diskUsedBytes(): number {
  if (!overview.value) return 0
  return Math.max(0, overview.value.local.total_bytes - overview.value.local.free_bytes)
}

function diskUsedPct(): number {
  if (!overview.value || overview.value.local.total_bytes <= 0) return 0
  return (diskUsedBytes() / overview.value.local.total_bytes) * 100
}

function maxCamBytes(): number {
  if (!overview.value) return 0
  return Math.max(0, ...overview.value.cameras.map((c) => c.local_bytes + c.s3_bytes))
}

function barPct(part: number, total: number): string {
  const max = maxCamBytes()
  if (!max || total <= 0) return '0%'
  return `${Math.max(part > 0 ? 1.5 : 0, (part / max) * 100)}%`
}

async function refresh() {
  try {
    overview.value = await get<StorageOverview>('/system/storage')
    loadError.value = ''
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : 'Failed to load storage overview'
  }
}

async function loadSettings() {
  try {
    const res = await get<SystemSettings>('/system/settings')
    Object.assign(s3, res.s3, { secret_key: '' })
  } catch {
    // Non-fatal — form keeps defaults.
  }
}

async function saveSettings() {
  saving.value = true
  saveError.value = ''
  savedOk.value = false
  try {
    await put('/system/settings', {
      s3: {
        enabled: s3.enabled,
        endpoint: s3.endpoint,
        secure: s3.secure,
        region: s3.region,
        bucket: s3.bucket,
        prefix: s3.prefix,
        access_key: s3.access_key,
        storage_class: s3.storage_class,
        secret_key: s3.secret_key,
      },
    })
    s3.secret_set = s3.secret_set || s3.secret_key !== ''
    s3.secret_key = ''
    savedOk.value = true
    await refresh()
  } catch (err) {
    saveError.value = err instanceof ApiError ? err.message : 'Failed to save settings'
  } finally {
    saving.value = false
  }
}

onMounted(() => {
  refresh()
  loadSettings()
})
</script>

<template>
  <div>
    <div class="page-header">
      <h1 class="page-title">Storage</h1>
    </div>

    <p v-if="loadError" class="error-text">{{ loadError }}</p>

    <template v-if="overview">
      <div class="card disk-card">
        <div class="disk-head">
          <h2 class="section-title">Local disk</h2>
          <span class="mono muted">
            {{ formatBytes(diskUsedBytes()) }} used of {{ formatBytes(overview.local.total_bytes) }}
            · {{ formatBytes(overview.local.free_bytes) }} free
          </span>
        </div>
        <div class="disk-bar">
          <div class="disk-bar-used" :style="{ width: `${diskUsedPct()}%` }"></div>
        </div>
        <p class="muted s3-status">
          S3 tiering:
          <template v-if="overview.s3.enabled">enabled — bucket <span class="mono">{{ overview.s3.bucket }}</span></template>
          <template v-else>disabled</template>
        </p>
      </div>

      <div class="card table-card">
        <h2 class="section-title">Per-camera usage</h2>
        <table class="storage-table">
          <thead>
            <tr>
              <th>Camera</th>
              <th class="usage-col">Local / S3</th>
              <th>Segments</th>
              <th>Bitrate</th>
              <th>Retention</th>
              <th>Tier after</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="cam in overview.cameras" :key="cam.id">
              <td class="cam-cell">{{ cam.name }}</td>
              <td class="usage-col">
                <div class="usage-bar" :title="`${formatBytes(cam.local_bytes)} local · ${formatBytes(cam.s3_bytes)} S3`">
                  <div class="usage-local" :style="{ width: barPct(cam.local_bytes, cam.local_bytes + cam.s3_bytes) }"></div>
                  <div class="usage-s3" :style="{ width: barPct(cam.s3_bytes, cam.local_bytes + cam.s3_bytes) }"></div>
                </div>
                <span class="usage-text mono">
                  {{ formatBytes(cam.local_bytes) }}<template v-if="cam.s3_bytes"> + {{ formatBytes(cam.s3_bytes) }} S3</template>
                </span>
              </td>
              <td class="mono">{{ cam.segment_count }}</td>
              <td class="mono">{{ cam.bitrate_kbps ? `${cam.bitrate_kbps} kbps` : '—' }}</td>
              <td class="mono">
                {{ cam.retention_days }}d{{ cam.retention_gb ? ` / ${cam.retention_gb}GB` : '' }}
              </td>
              <td class="mono">{{ cam.tier_after_days != null ? `${cam.tier_after_days}d` : '—' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </template>

    <div class="card settings-card">
      <h2 class="section-title">S3 settings</h2>
      <form @submit.prevent="saveSettings">
        <label class="check-row">
          <input v-model="s3.enabled" type="checkbox" />
          <span>Enable S3 tiering</span>
        </label>

        <div class="field-row">
          <label class="field">
            <span>Endpoint</span>
            <input v-model="s3.endpoint" type="text" placeholder="s3.us-east-1.amazonaws.com" />
          </label>
          <label class="field">
            <span>Region</span>
            <input v-model="s3.region" type="text" placeholder="us-east-1" />
          </label>
        </div>

        <div class="field-row">
          <label class="field">
            <span>Bucket</span>
            <input v-model="s3.bucket" type="text" />
          </label>
          <label class="field">
            <span>Prefix <em>optional</em></span>
            <input v-model="s3.prefix" type="text" />
          </label>
        </div>

        <div class="field-row">
          <label class="field">
            <span>Access key</span>
            <input v-model="s3.access_key" type="text" autocomplete="off" />
          </label>
          <label class="field">
            <span>Secret key <em>{{ s3.secret_set ? 'leave blank to keep saved' : '' }}</em></span>
            <input
              v-model="s3.secret_key"
              type="password"
              autocomplete="new-password"
              :placeholder="s3.secret_set ? '••• saved' : ''"
            />
          </label>
        </div>

        <div class="field-row">
          <label class="field">
            <span>Storage class <em>optional</em></span>
            <input v-model="s3.storage_class" type="text" placeholder="STANDARD" />
          </label>
          <label class="check-row secure-row">
            <input v-model="s3.secure" type="checkbox" />
            <span>Use HTTPS</span>
          </label>
        </div>

        <p v-if="saveError" class="error-text">{{ saveError }}</p>
        <p v-if="savedOk" class="ok-text">Settings saved.</p>

        <div class="form-actions">
          <button class="btn btn-primary btn-inline" type="submit" :disabled="saving">
            {{ saving ? 'Saving…' : 'Save settings' }}
          </button>
        </div>
      </form>
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

.section-title {
  margin: 0 0 1rem;
  font-size: 0.85rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
}

.muted {
  color: var(--text-muted);
}

.mono {
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 0.85rem;
}

.disk-card {
  padding: 1.25rem 1.5rem;
  margin-bottom: 1rem;
}

.disk-head {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 0.75rem;
}

.disk-head .section-title {
  margin: 0;
}

.disk-bar {
  height: 10px;
  border-radius: 5px;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  overflow: hidden;
}

.disk-bar-used {
  height: 100%;
  background: var(--accent);
  transition: width 0.3s ease;
}

.s3-status {
  margin: 0.75rem 0 0;
  font-size: 0.9rem;
}

.table-card {
  padding: 1.25rem 1.5rem;
  overflow-x: auto;
  margin-bottom: 1rem;
}

.storage-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.92rem;
}

.storage-table th {
  text-align: left;
  font-size: 0.78rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
  padding: 0.5rem 0.75rem;
  border-bottom: 1px solid var(--border);
}

.storage-table td {
  padding: 0.7rem 0.75rem;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}

.storage-table tbody tr:last-child td {
  border-bottom: none;
}

.cam-cell {
  font-weight: 500;
  white-space: nowrap;
}

.usage-col {
  min-width: 220px;
}

.usage-bar {
  display: flex;
  height: 8px;
  border-radius: 4px;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  overflow: hidden;
  margin-bottom: 0.3rem;
}

.usage-local {
  background: var(--accent);
}

.usage-s3 {
  background: #a78bfa;
}

.usage-text {
  color: var(--text-muted);
}

.settings-card {
  padding: 1.25rem 1.5rem;
  max-width: 720px;
}

.field-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.9rem;
}

.check-row {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin-bottom: 1.1rem;
  font-size: 0.92rem;
  color: var(--text-muted);
  cursor: pointer;
}

.secure-row {
  align-self: end;
  margin-bottom: 1.35rem;
}

.ok-text {
  color: #4ade80;
  font-size: 0.9rem;
  margin: 0 0 1rem;
}

.btn-inline {
  width: auto;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
}
</style>
