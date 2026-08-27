<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { get, ApiError } from '../api/client'
import type { AuditEntry, AuditLogResponse, SystemInfo } from '../api/types'

const info = ref<SystemInfo | null>(null)
const entries = ref<AuditEntry[]>([])
const loadError = ref('')
const filter = ref('')

const filtered = computed(() => {
  const q = filter.value.trim().toLowerCase()
  if (!q) return entries.value
  return entries.value.filter(
    (e) =>
      e.action.toLowerCase().includes(q) ||
      (e.target ?? '').toLowerCase().includes(q) ||
      (e.user_id ?? '').toLowerCase().includes(q),
  )
})

function formatTs(ts: string): string {
  return new Date(ts).toLocaleString()
}

function formatUptime(s: number): string {
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

async function refresh() {
  try {
    const [i, log] = await Promise.all([
      get<SystemInfo>('/system/info'),
      get<AuditLogResponse>('/audit-log'),
    ])
    info.value = i
    entries.value = log.entries ?? []
    loadError.value = ''
  } catch (err) {
    loadError.value = err instanceof ApiError ? err.message : 'Failed to load system view'
  }
}

onMounted(refresh)
</script>

<template>
  <div>
    <div class="page-header">
      <h1 class="page-title">System</h1>
      <button class="btn btn-ghost btn-inline" type="button" @click="refresh">Refresh</button>
    </div>

    <p v-if="loadError" class="error-text">{{ loadError }}</p>

    <div v-if="info" class="info-cards">
      <div class="info-card">
        <span class="info-label">Version</span>
        <span class="info-value mono">{{ info.version }}</span>
      </div>
      <div class="info-card">
        <span class="info-label">Uptime</span>
        <span class="info-value">{{ formatUptime(info.uptime_s) }}</span>
      </div>
      <div class="info-card">
        <span class="info-label">WS clients</span>
        <span class="info-value">{{ info.ws_clients }}</span>
      </div>
      <div class="info-card">
        <span class="info-label">Public URL</span>
        <span class="info-value">{{ info.public_url || '—' }}</span>
      </div>
    </div>

    <section class="audit-section">
      <div class="audit-header">
        <h2 class="section-title">Audit log</h2>
        <input
          v-model="filter"
          class="input filter-input"
          type="search"
          placeholder="Filter by action, target, user…"
        />
      </div>
      <table class="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Action</th>
            <th>User</th>
            <th>Target</th>
            <th>IP</th>
            <th>Detail</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in filtered" :key="e.id">
            <td class="mono nowrap">{{ formatTs(e.ts) }}</td>
            <td class="mono">{{ e.action }}</td>
            <td class="mono">{{ e.user_id ?? '—' }}</td>
            <td class="mono">{{ e.target ?? '—' }}</td>
            <td class="mono">{{ e.ip ?? '—' }}</td>
            <td class="mono detail-cell">
              {{ Object.keys(e.detail ?? {}).length ? JSON.stringify(e.detail) : '—' }}
            </td>
          </tr>
          <tr v-if="!filtered.length">
            <td colspan="6" class="muted">No audit entries.</td>
          </tr>
        </tbody>
      </table>
    </section>
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

.info-cards {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 0.75rem;
  margin-bottom: 1.5rem;
}

.info-card {
  display: flex;
  flex-direction: column;
  gap: 0.3rem;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 0.8rem 1rem;
}

.info-label {
  font-size: 0.72rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
  color: var(--text-muted);
}

.info-value {
  font-size: 1.05rem;
  font-weight: 600;
}

.mono {
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 0.82rem;
}

.nowrap {
  white-space: nowrap;
}

.muted {
  color: var(--text-muted);
}

.audit-section {
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  padding: 1rem;
}

.audit-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 0.75rem;
}

.section-title {
  margin: 0;
  font-size: 1rem;
  font-weight: 600;
}

.filter-input {
  max-width: 320px;
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.85rem;
}

.table th,
.table td {
  text-align: left;
  padding: 0.45rem 0.6rem;
  border-bottom: 1px solid var(--border);
}

.table th {
  color: var(--text-muted);
  font-size: 0.72rem;
  text-transform: uppercase;
  letter-spacing: 0.06em;
}

.detail-cell {
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
