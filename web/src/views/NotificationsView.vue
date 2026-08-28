<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { get, ApiError } from '../api/client'
import {
  createChannel,
  createRule,
  deleteChannel,
  deleteRule,
  listChannels,
  listRules,
  notifyLog,
  testChannel,
  updateChannel,
  updateRule,
  type ChannelPayload,
  type RulePayload,
} from '../api/notify'
import type { Camera, ChannelType, NotifyChannel, NotifyLogEntry, NotifyRule } from '../api/types'
import { formatDateTime } from '../utils/format'

type Tab = 'channels' | 'rules' | 'log'
const tab = ref<Tab>('channels')

const cameras = ref<Camera[]>([])
const channels = ref<NotifyChannel[]>([])
const rules = ref<NotifyRule[]>([])
const logEntries = ref<NotifyLogEntry[]>([])
const pageError = ref('')

// ---- channel form ----

interface FieldDef {
  key: string
  label: string
  secret?: boolean
  number?: boolean
  hint?: string
  options?: string[]
}

const CHANNEL_FIELDS: Record<ChannelType, FieldDef[]> = {
  smtp: [
    { key: 'host', label: 'SMTP host' },
    { key: 'port', label: 'Port', number: true, hint: '587' },
    { key: 'from', label: 'From address' },
    { key: 'to', label: 'Recipients', hint: 'comma separated' },
    { key: 'username', label: 'Username' },
    { key: 'password', label: 'Password', secret: true },
    { key: 'tls', label: 'TLS mode', options: ['starttls', 'tls', 'none'] },
  ],
  webhook: [
    { key: 'url', label: 'URL' },
    { key: 'hmac_secret', label: 'HMAC secret', secret: true, hint: 'optional — signs body' },
  ],
  ntfy: [
    { key: 'server', label: 'Server', hint: 'https://ntfy.sh' },
    { key: 'topic', label: 'Topic' },
    { key: 'priority', label: 'Priority', hint: 'optional 1–5' },
    { key: 'token', label: 'Token', secret: true },
  ],
  gotify: [
    { key: 'server', label: 'Server', hint: 'https://gotify.example.com' },
    { key: 'token', label: 'App token', secret: true },
    { key: 'priority', label: 'Priority', number: true },
  ],
  discord: [{ key: 'url', label: 'Webhook URL', secret: true }],
  slack: [{ key: 'url', label: 'Webhook URL', secret: true }],
  telegram: [
    { key: 'bot_token', label: 'Bot token', secret: true },
    { key: 'chat_id', label: 'Chat ID' },
  ],
}

const channelForm = reactive({
  id: '' as string | '',
  name: '',
  type: 'ntfy' as ChannelType,
  config: {} as Record<string, string>,
  enabled: true,
})
const showChannelForm = ref(false)
const channelError = ref('')
const testingId = ref('')
const testResult = ref<Record<string, string>>({})

const channelFields = computed(() => CHANNEL_FIELDS[channelForm.type])

function openChannelForm(ch?: NotifyChannel) {
  channelError.value = ''
  if (ch) {
    channelForm.id = ch.id
    channelForm.name = ch.name
    channelForm.type = ch.type
    channelForm.enabled = ch.enabled
    channelForm.config = {}
    for (const [k, v] of Object.entries(ch.config)) {
      if (k.endsWith('_enc')) continue // secrets write-only
      channelForm.config[k] = Array.isArray(v) ? v.join(', ') : String(v ?? '')
    }
  } else {
    channelForm.id = ''
    channelForm.name = ''
    channelForm.type = 'ntfy'
    channelForm.config = {}
    channelForm.enabled = true
  }
  showChannelForm.value = true
}

function channelConfigPayload(): Record<string, unknown> {
  const cfg: Record<string, unknown> = {}
  for (const f of CHANNEL_FIELDS[channelForm.type]) {
    const raw = (channelForm.config[f.key] ?? '').trim()
    if (raw === '') continue // empty secret keeps stored value; others omitted
    if (f.number) {
      cfg[f.key] = Number(raw)
    } else if (f.key === 'to') {
      cfg[f.key] = raw.split(',').map((s) => s.trim()).filter(Boolean)
    } else {
      cfg[f.key] = raw
    }
  }
  return cfg
}

async function saveChannel() {
  channelError.value = ''
  const payload: ChannelPayload = {
    name: channelForm.name,
    type: channelForm.type,
    config: channelConfigPayload(),
    enabled: channelForm.enabled,
  }
  try {
    if (channelForm.id) {
      await updateChannel(channelForm.id, payload)
    } else {
      await createChannel(payload)
    }
    showChannelForm.value = false
    await refreshChannels()
  } catch (err) {
    channelError.value = err instanceof ApiError ? err.message : 'Save failed'
  }
}

async function removeChannel(ch: NotifyChannel) {
  try {
    await deleteChannel(ch.id)
    await refreshChannels()
  } catch (err) {
    pageError.value = err instanceof ApiError ? err.message : 'Delete failed'
  }
}

async function sendTest(ch: NotifyChannel) {
  testingId.value = ch.id
  testResult.value = { ...testResult.value, [ch.id]: '' }
  try {
    await testChannel(ch.id)
    testResult.value = { ...testResult.value, [ch.id]: 'sent' }
  } catch (err) {
    testResult.value = { ...testResult.value, [ch.id]: err instanceof ApiError ? err.message : 'failed' }
  } finally {
    testingId.value = ''
  }
}

// ---- rule form ----

const EVENT_TYPES = ['motion', 'ai']
const ruleForm = reactive({
  id: '',
  name: '',
  enabled: true,
  camera_ids: [] as string[],
  event_types: ['motion'] as string[],
  labels: '',
  cooldown_s: 300,
  schedFrom: '',
  schedTo: '',
  channel_ids: [] as string[],
})
const showRuleForm = ref(false)
const ruleError = ref('')

function openRuleForm(r?: NotifyRule) {
  ruleError.value = ''
  if (r) {
    ruleForm.id = r.id
    ruleForm.name = r.name
    ruleForm.enabled = r.enabled
    ruleForm.camera_ids = [...r.camera_ids]
    ruleForm.event_types = [...r.event_types]
    ruleForm.labels = r.labels.join(', ')
    ruleForm.cooldown_s = r.cooldown_s
    ruleForm.channel_ids = [...r.channel_ids]
    const everyday = (r.schedule?.everyday as [string, string][] | undefined) ?? []
    ruleForm.schedFrom = everyday[0]?.[0] ?? ''
    ruleForm.schedTo = everyday[0]?.[1] ?? ''
  } else {
    ruleForm.id = ''
    ruleForm.name = ''
    ruleForm.enabled = true
    ruleForm.camera_ids = []
    ruleForm.event_types = ['motion']
    ruleForm.labels = ''
    ruleForm.cooldown_s = 300
    ruleForm.channel_ids = []
    ruleForm.schedFrom = ''
    ruleForm.schedTo = ''
  }
  showRuleForm.value = true
}

function toggle(list: string[], id: string) {
  const i = list.indexOf(id)
  if (i >= 0) list.splice(i, 1)
  else list.push(id)
}

async function saveRule() {
  ruleError.value = ''
  const schedule: Record<string, unknown> = {}
  if (ruleForm.schedFrom && ruleForm.schedTo) {
    schedule.everyday = [[ruleForm.schedFrom, ruleForm.schedTo]]
  }
  const payload: RulePayload = {
    name: ruleForm.name,
    enabled: ruleForm.enabled,
    camera_ids: ruleForm.camera_ids,
    event_types: ruleForm.event_types,
    labels: ruleForm.labels.split(',').map((s) => s.trim()).filter(Boolean),
    cooldown_s: ruleForm.cooldown_s,
    channel_ids: ruleForm.channel_ids,
    schedule,
  }
  try {
    if (ruleForm.id) {
      await updateRule(ruleForm.id, payload)
    } else {
      await createRule(payload)
    }
    showRuleForm.value = false
    await refreshRules()
  } catch (err) {
    ruleError.value = err instanceof ApiError ? err.message : 'Save failed'
  }
}

async function removeRule(r: NotifyRule) {
  try {
    await deleteRule(r.id)
    await refreshRules()
  } catch (err) {
    pageError.value = err instanceof ApiError ? err.message : 'Delete failed'
  }
}

function channelName(id: string): string {
  return channels.value.find((c) => c.id === id)?.name ?? 'channel'
}

function ruleDesc(r: NotifyRule): string {
  const cams = r.camera_ids.length === 0 ? 'all cameras' : `${r.camera_ids.length} camera(s)`
  return `${cams} · ${r.event_types.join('/')}${r.labels.length ? ' · ' + r.labels.join(', ') : ''} · cooldown ${r.cooldown_s}s`
}

// ---- log ----

async function refreshLog() {
  try {
    const res = await notifyLog(undefined, undefined, 100)
    logEntries.value = res.log ?? []
  } catch {
    // non-fatal
  }
}

async function refreshChannels() {
  const res = await listChannels()
  channels.value = res.channels ?? []
}

async function refreshRules() {
  const res = await listRules()
  rules.value = res.rules ?? []
}

function fmt(ts?: string): string {
  return ts ? formatDateTime(new Date(ts).getTime()) : '—'
}

onMounted(async () => {
  try {
    const res = await get<{ cameras: Camera[] }>('/cameras')
    cameras.value = res.cameras ?? []
  } catch {
    // names fall back
  }
  try {
    await Promise.all([refreshChannels(), refreshRules(), refreshLog()])
  } catch (err) {
    pageError.value = err instanceof ApiError ? err.message : 'Failed to load'
  }
})
</script>

<template>
  <div>
    <div class="page-head">
      <h1 class="page-title">Notifications</h1>
      <div class="tab-row">
        <button
          v-for="t in (['channels', 'rules', 'log'] as Tab[])"
          :key="t"
          class="btn btn-ghost btn-sm"
          :class="{ 'tab-active': tab === t }"
          type="button"
          @click="tab = t"
        >
          {{ t === 'log' ? 'Delivery log' : t[0].toUpperCase() + t.slice(1) }}
        </button>
      </div>
    </div>

    <p v-if="pageError" class="error-text">{{ pageError }}</p>

    <!-- Channels -->
    <section v-if="tab === 'channels'">
      <div class="section-head">
        <p class="muted">Where notifications go: email, push, chat, webhooks.</p>
        <button class="btn btn-primary btn-inline" type="button" @click="openChannelForm()">
          Add channel
        </button>
      </div>
      <table v-if="channels.length" class="table">
        <thead>
          <tr><th>Name</th><th>Type</th><th>Enabled</th><th></th></tr>
        </thead>
        <tbody>
          <tr v-for="ch in channels" :key="ch.id">
            <td>{{ ch.name }}</td>
            <td class="mono">{{ ch.type }}</td>
            <td>{{ ch.enabled ? 'yes' : 'no' }}</td>
            <td class="row-actions">
              <span v-if="testResult[ch.id]" class="test-result" :class="{ 'test-fail': testResult[ch.id] !== 'sent' }">
                {{ testResult[ch.id] }}
              </span>
              <button class="btn btn-ghost btn-sm" type="button" :disabled="testingId === ch.id" @click="sendTest(ch)">
                {{ testingId === ch.id ? 'Sending…' : 'Test' }}
              </button>
              <button class="btn btn-ghost btn-sm" type="button" @click="openChannelForm(ch)">Edit</button>
              <button class="btn btn-ghost btn-sm btn-danger" type="button" @click="removeChannel(ch)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="muted empty">No channels yet — add one to receive notifications.</p>
    </section>

    <!-- Rules -->
    <section v-if="tab === 'rules'">
      <div class="section-head">
        <p class="muted">Which events notify which channels, with schedules and cooldowns.</p>
        <button
          class="btn btn-primary btn-inline"
          type="button"
          :disabled="channels.length === 0"
          @click="openRuleForm()"
        >
          Add rule
        </button>
      </div>
      <table v-if="rules.length" class="table">
        <thead>
          <tr><th>Name</th><th>Matches</th><th>Channels</th><th>Enabled</th><th></th></tr>
        </thead>
        <tbody>
          <tr v-for="r in rules" :key="r.id">
            <td>{{ r.name }}</td>
            <td class="muted">{{ ruleDesc(r) }}</td>
            <td>{{ r.channel_ids.map(channelName).join(', ') }}</td>
            <td>{{ r.enabled ? 'yes' : 'no' }}</td>
            <td class="row-actions">
              <button class="btn btn-ghost btn-sm" type="button" @click="openRuleForm(r)">Edit</button>
              <button class="btn btn-ghost btn-sm btn-danger" type="button" @click="removeRule(r)">Delete</button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="muted empty">
        {{ channels.length === 0 ? 'Add a channel first.' : 'No rules yet.' }}
      </p>
    </section>

    <!-- Log -->
    <section v-if="tab === 'log'">
      <table v-if="logEntries.length" class="table">
        <thead>
          <tr><th>Time</th><th>Status</th><th>Error</th><th>Event</th></tr>
        </thead>
        <tbody>
          <tr v-for="e in logEntries" :key="e.id">
            <td class="mono">{{ fmt(e.sent_at) }}</td>
            <td>
              <span class="chip" :class="`status-${e.status}`">{{ e.status }}</span>
            </td>
            <td class="muted">{{ e.error ?? '' }}</td>
            <td>
              <router-link v-if="e.event_id" class="text-link" :to="`/events?id=${e.event_id}`">
                event
              </router-link>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="muted empty">Nothing delivered yet.</p>
    </section>

    <!-- Channel modal -->
    <div v-if="showChannelForm" class="modal-backdrop" @click.self="showChannelForm = false">
      <div class="modal card">
        <h2 class="modal-title">{{ channelForm.id ? 'Edit channel' : 'Add channel' }}</h2>
        <label class="field">
          <span>Name</span>
          <input v-model="channelForm.name" type="text" required placeholder="Phone push" />
        </label>
        <label class="field">
          <span>Type</span>
          <select v-model="channelForm.type" :disabled="!!channelForm.id">
            <option v-for="t in Object.keys(CHANNEL_FIELDS)" :key="t" :value="t">{{ t }}</option>
          </select>
        </label>
        <label v-for="f in channelFields" :key="f.key" class="field">
          <span>{{ f.label }} <em v-if="f.secret">secret — blank keeps current</em> <em v-else-if="f.hint">{{ f.hint }}</em></span>
          <select v-if="f.options" v-model="channelForm.config[f.key]">
            <option v-for="o in f.options" :key="o" :value="o">{{ o }}</option>
          </select>
          <input
            v-else
            v-model="channelForm.config[f.key]"
            :type="f.secret ? 'password' : 'text'"
            :inputmode="f.number ? 'numeric' : undefined"
            autocomplete="off"
          />
        </label>
        <label class="field field-check">
          <input v-model="channelForm.enabled" type="checkbox" />
          <span>Enabled</span>
        </label>
        <p v-if="channelError" class="error-text">{{ channelError }}</p>
        <div class="modal-actions">
          <button class="btn btn-ghost" type="button" @click="showChannelForm = false">Cancel</button>
          <button class="btn btn-primary btn-inline" type="button" :disabled="!channelForm.name" @click="saveChannel">
            Save
          </button>
        </div>
      </div>
    </div>

    <!-- Rule modal -->
    <div v-if="showRuleForm" class="modal-backdrop" @click.self="showRuleForm = false">
      <div class="modal card">
        <h2 class="modal-title">{{ ruleForm.id ? 'Edit rule' : 'Add rule' }}</h2>
        <label class="field">
          <span>Name</span>
          <input v-model="ruleForm.name" type="text" required placeholder="Front door at night" />
        </label>
        <div class="field">
          <span>Cameras <em>none checked = all</em></span>
          <div class="check-grid">
            <label v-for="c in cameras" :key="c.id" class="check-item">
              <input
                type="checkbox"
                :checked="ruleForm.camera_ids.includes(c.id)"
                @change="toggle(ruleForm.camera_ids, c.id)"
              />
              {{ c.name }}
            </label>
          </div>
        </div>
        <div class="field">
          <span>Event types</span>
          <div class="check-grid">
            <label v-for="t in EVENT_TYPES" :key="t" class="check-item">
              <input
                type="checkbox"
                :checked="ruleForm.event_types.includes(t)"
                @change="toggle(ruleForm.event_types, t)"
              />
              {{ t }}
            </label>
          </div>
        </div>
        <div class="field-row">
          <label class="field">
            <span>Labels <em>comma separated; AI only</em></span>
            <input v-model="ruleForm.labels" type="text" placeholder="person, car" />
          </label>
          <label class="field">
            <span>Cooldown (s)</span>
            <input v-model.number="ruleForm.cooldown_s" type="number" min="0" />
          </label>
        </div>
        <div class="field-row">
          <label class="field">
            <span>Active from <em>optional, daily</em></span>
            <input v-model="ruleForm.schedFrom" type="time" />
          </label>
          <label class="field">
            <span>Active until <em>may wrap midnight</em></span>
            <input v-model="ruleForm.schedTo" type="time" />
          </label>
        </div>
        <div class="field">
          <span>Channels</span>
          <div class="check-grid">
            <label v-for="c in channels" :key="c.id" class="check-item">
              <input
                type="checkbox"
                :checked="ruleForm.channel_ids.includes(c.id)"
                @change="toggle(ruleForm.channel_ids, c.id)"
              />
              {{ c.name }}
            </label>
          </div>
        </div>
        <label class="field field-check">
          <input v-model="ruleForm.enabled" type="checkbox" />
          <span>Enabled</span>
        </label>
        <p v-if="ruleError" class="error-text">{{ ruleError }}</p>
        <div class="modal-actions">
          <button class="btn btn-ghost" type="button" @click="showRuleForm = false">Cancel</button>
          <button
            class="btn btn-primary btn-inline"
            type="button"
            :disabled="!ruleForm.name || ruleForm.channel_ids.length === 0"
            @click="saveRule"
          >
            Save
          </button>
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

.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1.25rem;
  flex-wrap: wrap;
  gap: 1rem;
}

.tab-row {
  display: flex;
  gap: 0.4rem;
}

.tab-active {
  color: var(--text);
  border-color: var(--accent);
  background: rgba(79, 140, 255, 0.12);
}

.section-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1rem;
}

.section-head p {
  margin: 0;
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.9rem;
}

.table th {
  text-align: left;
  color: var(--text-muted);
  font-weight: 500;
  padding: 0.5rem 0.75rem;
  border-bottom: 1px solid var(--border);
}

.table td {
  padding: 0.6rem 0.75rem;
  border-bottom: 1px solid var(--border);
}

.row-actions {
  display: flex;
  gap: 0.35rem;
  justify-content: flex-end;
  align-items: center;
}

.mono {
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 0.82rem;
}

.muted {
  color: var(--text-muted);
}

.empty {
  padding: 2.5rem 0;
  text-align: center;
}

.btn-inline {
  width: auto;
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
  width: auto;
}

.btn-danger {
  color: var(--danger);
}

.error-text {
  color: var(--danger);
}

.text-link {
  color: var(--accent);
}

.test-result {
  font-size: 0.82rem;
  color: #4ade80;
}

.test-fail {
  color: var(--danger);
}

.chip {
  padding: 0.15rem 0.55rem;
  border-radius: 999px;
  font-size: 0.75rem;
  font-weight: 600;
  border: 1px solid var(--border);
}

.status-sent {
  color: #4ade80;
  border-color: #166534;
}

.status-failed {
  color: var(--danger);
  border-color: #7f1d1d;
}

.status-cooldown_skip {
  color: var(--text-muted);
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
  max-width: 520px;
  max-height: 90vh;
  overflow-y: auto;
}

.modal-title {
  margin: 0 0 1.25rem;
  font-size: 1.15rem;
  font-weight: 600;
}

.field-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.9rem;
}

.field select,
.field input[type='time'] {
  width: 100%;
  padding: 0.6rem 0.75rem;
  background: var(--bg-elevated);
  border: 1px solid var(--border);
  border-radius: 6px;
  color: var(--text);
  font-size: 0.95rem;
  font-family: inherit;
  outline: none;
}

.field em {
  color: var(--text-muted);
  font-style: normal;
  font-size: 0.78rem;
}

.field-check {
  display: flex;
  flex-direction: row;
  align-items: center;
  gap: 0.5rem;
}

.check-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem 1rem;
  margin-top: 0.35rem;
}

.check-item {
  display: flex;
  align-items: center;
  gap: 0.35rem;
  font-size: 0.88rem;
  color: var(--text-muted);
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.6rem;
  margin-top: 1rem;
}
</style>
