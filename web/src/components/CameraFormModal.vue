<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { post, patch, ApiError } from '../api/client'
import type {
  Camera,
  CameraPayload,
  OnvifProbeResponse,
  PlaybackTranscode,
  ProbeResponse,
  ProbeStream,
  RecordMode,
  Transport,
} from '../api/types'

export interface CameraPrefill {
  name?: string
  onvif_endpoint?: string
}

const props = defineProps<{
  camera: Camera | null
  prefill?: CameraPrefill | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved'): void
}>()

const isEdit = computed(() => props.camera !== null)

const form = reactive({
  name: props.camera?.name ?? props.prefill?.name ?? '',
  main_url: props.camera?.main_url ?? '',
  sub_url: props.camera?.sub_url ?? '',
  username: props.camera?.username ?? '',
  password: '',
  transport: (props.camera?.transport ?? 'auto') as Transport,
  record_mode: (props.camera?.record_mode ?? 'continuous') as RecordMode,
  retention_days: props.camera?.retention_days ?? 7,
  retention_gb: props.camera?.retention_gb ?? 0,
  onvif_endpoint: props.camera?.onvif_endpoint ?? props.prefill?.onvif_endpoint ?? '',
  tier_after_days: props.camera?.tier_after_days ?? null as number | null,
  playback_transcode: (props.camera?.playback_transcode ?? 'auto') as PlaybackTranscode,
})

const saving = ref(false)
const saveError = ref('')

const probing = ref(false)
const probeResult = ref<ProbeResponse | null>(null)
const onvifResult = ref<OnvifProbeResponse | null>(null)
const probeError = ref('')

function describeStream(s: ProbeStream): string {
  if (s.type === 'video') {
    const codec = s.codec.toUpperCase()
    return s.width && s.height ? `${codec} ${s.width}×${s.height}` : codec
  }
  const codec = s.codec.toUpperCase()
  if (s.channels && s.rate) return `${codec} ${s.channels}ch ${Math.round(s.rate / 1000)}kHz`
  return codec
}

const probeSummary = computed(() => {
  if (!probeResult.value) return ''
  return probeResult.value.streams.map(describeStream).join(' + ')
})

async function testConnection() {
  probing.value = true
  probeResult.value = null
  onvifResult.value = null
  probeError.value = ''
  try {
    if (form.onvif_endpoint) {
      onvifResult.value = await post<OnvifProbeResponse>('/cameras/probe', {
        onvif_endpoint: form.onvif_endpoint,
        username: form.username || undefined,
        password: form.password || undefined,
      })
    } else {
      probeResult.value = await post<ProbeResponse>('/cameras/probe', {
        url: form.main_url,
        username: form.username || undefined,
        password: form.password || undefined,
        transport: form.transport,
      })
      if (!probeResult.value.ok) {
        probeError.value = probeResult.value.error || 'Probe failed'
      }
    }
  } catch (err) {
    probeError.value = err instanceof ApiError ? err.message : 'Probe request failed'
  } finally {
    probing.value = false
  }
}

function useProfileStreams() {
  const profiles = onvifResult.value?.profiles ?? []
  const main = profiles.find((p) => p.stream_uri)
  if (!main) return
  form.main_url = main.stream_uri
  const sub = profiles.find((p) => p.stream_uri && p.stream_uri !== main.stream_uri)
  form.sub_url = sub ? sub.stream_uri : ''
}

async function save() {
  saving.value = true
  saveError.value = ''
  const payload: CameraPayload = {
    name: form.name,
    main_url: form.main_url,
    sub_url: form.sub_url || undefined,
    username: form.username || undefined,
    password: form.password || undefined,
    transport: form.transport,
    record_mode: form.record_mode,
    retention_days: form.retention_days,
    retention_gb: form.retention_gb || undefined,
  }
  if (form.onvif_endpoint) payload.onvif_endpoint = form.onvif_endpoint
  if (form.tier_after_days != null) payload.tier_after_days = form.tier_after_days
  if (form.playback_transcode !== 'auto') payload.playback_transcode = form.playback_transcode
  try {
    if (props.camera) {
      await patch(`/cameras/${props.camera.id}`, payload)
    } else {
      await post('/cameras', payload)
    }
    emit('saved')
  } catch (err) {
    saveError.value = err instanceof ApiError ? err.message : 'Failed to save camera'
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div class="modal-backdrop" @click.self="emit('close')">
    <div class="modal card">
      <h2 class="modal-title">{{ isEdit ? 'Edit camera' : 'Add camera' }}</h2>

      <form @submit.prevent="save">
        <label class="field">
          <span>Name</span>
          <input v-model="form.name" type="text" required placeholder="Front door" />
        </label>

        <label class="field">
          <span>Main stream URL <em>rtsp://…</em></span>
          <input v-model="form.main_url" type="text" :required="!form.onvif_endpoint" placeholder="rtsp://192.168.1.50:554/stream1" />
        </label>

        <label class="field">
          <span>Sub stream URL <em>optional</em></span>
          <input v-model="form.sub_url" type="text" placeholder="rtsp://192.168.1.50:554/stream2" />
        </label>

        <label class="field">
          <span>ONVIF endpoint <em>optional — enables discovery, PTZ &amp; imaging</em></span>
          <input v-model="form.onvif_endpoint" type="text" placeholder="http://192.168.1.50:80/onvif/device_service" />
        </label>

        <div class="field-row">
          <label class="field">
            <span>Username <em>optional</em></span>
            <input v-model="form.username" type="text" autocomplete="off" />
          </label>
          <label class="field">
            <span>Password <em>{{ isEdit ? 'leave blank to keep current' : 'optional' }}</em></span>
            <input v-model="form.password" type="password" autocomplete="new-password" />
          </label>
        </div>

        <div class="field-row">
          <label class="field">
            <span>Transport</span>
            <select v-model="form.transport">
              <option value="auto">Auto</option>
              <option value="tcp">TCP</option>
              <option value="udp">UDP</option>
            </select>
          </label>
          <label class="field">
            <span>Record mode</span>
            <select v-model="form.record_mode">
              <option value="continuous">Continuous</option>
              <option value="motion">Motion</option>
              <option value="off">Off</option>
            </select>
          </label>
        </div>

        <div class="field-row">
          <label class="field">
            <span>Retention (days)</span>
            <input v-model.number="form.retention_days" type="number" min="0" />
          </label>
          <label class="field">
            <span>Retention (GB) <em>0 = unlimited</em></span>
            <input v-model.number="form.retention_gb" type="number" min="0" />
          </label>
        </div>

        <div class="field-row">
          <label class="field">
            <span>Tier to S3 after (days) <em>blank = never</em></span>
            <input v-model.number="form.tier_after_days" type="number" min="0" placeholder="—" />
          </label>
          <label class="field">
            <span>Playback transcode</span>
            <select v-model="form.playback_transcode">
              <option value="auto">Auto</option>
              <option value="never">Never</option>
              <option value="always">Always</option>
            </select>
          </label>
        </div>

        <div class="probe-row">
          <button
            class="btn btn-ghost"
            type="button"
            :disabled="probing || (!form.main_url && !form.onvif_endpoint)"
            @click="testConnection"
          >
            <span v-if="probing" class="spinner" aria-hidden="true"></span>
            {{ probing ? 'Testing…' : 'Test connection' }}
          </button>
          <span v-if="probeResult?.ok" class="probe-ok">{{ probeSummary || 'Connected' }}</span>
          <span v-else-if="probeError" class="probe-error">{{ probeError }}</span>
        </div>

        <div v-if="onvifResult" class="onvif-result">
          <p class="probe-ok onvif-device">
            {{ [onvifResult.manufacturer, onvifResult.model].filter(Boolean).join(' ') || 'ONVIF device' }}
            <span v-if="onvifResult.firmware_version" class="muted"> · fw {{ onvifResult.firmware_version }}</span>
            <span class="muted"> · PTZ: {{ onvifResult.has_ptz ? 'yes' : 'no' }}</span>
          </p>
          <ul v-if="onvifResult.profiles?.length" class="profile-list">
            <li v-for="p in onvifResult.profiles" :key="p.token">
              {{ p.name || p.token }} — {{ p.codec.toUpperCase() }} {{ p.width }}×{{ p.height }}
            </li>
          </ul>
          <button
            v-if="onvifResult.profiles?.some((p) => p.stream_uri)"
            class="btn btn-ghost btn-sm"
            type="button"
            @click="useProfileStreams"
          >
            Use profile streams
          </button>
        </div>

        <p v-if="saveError" class="error-text">{{ saveError }}</p>

        <div class="modal-actions">
          <button class="btn btn-ghost" type="button" @click="emit('close')">Cancel</button>
          <button class="btn btn-primary btn-inline" type="submit" :disabled="saving">
            {{ saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add camera' }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
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
  margin: 0 0 1.5rem;
  font-size: 1.2rem;
  font-weight: 600;
}

.field-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.9rem;
}

.field select {
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

.field select:focus {
  border-color: var(--accent);
}

.probe-row {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  margin-bottom: 1.1rem;
  min-height: 2.4rem;
}

.probe-ok {
  color: #4ade80;
  font-size: 0.88rem;
}

.probe-error {
  color: var(--danger);
  font-size: 0.88rem;
}

.onvif-result {
  margin: -0.4rem 0 1.1rem;
}

.onvif-device {
  margin: 0 0 0.4rem;
}

.profile-list {
  margin: 0 0 0.6rem;
  padding-left: 1.2rem;
  font-size: 0.88rem;
  color: var(--text-muted);
}

.muted {
  color: var(--text-muted);
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
  width: auto;
}

.spinner {
  display: inline-block;
  width: 13px;
  height: 13px;
  border: 2px solid var(--border);
  border-top-color: var(--text);
  border-radius: 50%;
  animation: spin 0.8s linear infinite;
  vertical-align: -2px;
  margin-right: 0.35rem;
}

@keyframes spin {
  to {
    transform: rotate(360deg);
  }
}

.modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 0.6rem;
}

.btn-inline {
  width: auto;
}
</style>
