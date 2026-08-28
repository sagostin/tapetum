export type CameraStatus = 'online' | 'offline' | 'degraded'
export type Transport = 'tcp' | 'udp' | 'auto'
export type RecordMode = 'continuous' | 'motion' | 'off'
export type PlaybackTranscode = 'auto' | 'never' | 'always'

export interface CameraStatusDetail {
  bitrate_kbps: number
  fps: number
  uptime: number
  codec: string
  last_error: string
}

export interface Camera {
  id: string
  name: string
  enabled: boolean
  main_url: string
  sub_url: string
  username: string
  transport: Transport
  onvif_endpoint: string | null
  onvif_profile: string | null
  has_ptz: boolean
  record_mode: RecordMode
  retention_days: number
  retention_gb: number
  tier_after_days: number | null
  playback_transcode: PlaybackTranscode
  motion_config?: MotionConfig
  group_id: string
  status: CameraStatus
  status_detail: CameraStatusDetail | null
  last_seen_at: string
  created_at: string
  updated_at: string
  display_rotate: 0 | 90 | 180 | 270
  display_hflip: boolean
  display_vflip: boolean
}

export type DisplayRotate = 0 | 90 | 180 | 270

export interface CameraDisplayUpdate {
  rotate?: DisplayRotate
  hflip?: boolean
  vflip?: boolean
}

export interface CameraListResponse {
  cameras: Camera[]
}

export interface CameraPayload {
  name: string
  main_url: string
  sub_url?: string
  username?: string
  password?: string
  transport: Transport
  record_mode: RecordMode
  retention_days: number
  retention_gb?: number
  onvif_endpoint?: string
  tier_after_days?: number
  playback_transcode?: PlaybackTranscode
  motion_config?: MotionConfig
}

export interface ProbeStream {
  type: 'video' | 'audio'
  codec: string
  width?: number
  height?: number
  channels?: number
  rate?: number
}

export interface ProbeResponse {
  ok: boolean
  streams: ProbeStream[]
  error?: string
}

export interface CameraStats {
  running: boolean
  status: CameraStatus
  bitrate_kbps: number
  fps: number
  last_frame_age_s: number
  uptime: number
  recorded_bytes: number
  codec?: string
}

export interface TimelineRange {
  start: string
  end: string
}

export interface TimelineEvent {
  id: string
  ts: string
  type: string
  label?: string
}

export interface TimelineResponse {
  camera_id: string
  from: string
  to: string
  buckets: number
  density: number[]
  recorded: TimelineRange[]
  events: TimelineEvent[]
}

// ---- Events (phase 3) ----

export interface TapEvent {
  id: string
  camera_id: string
  ts: string
  end_ts?: string
  type: string
  label?: string
  confidence?: number
  bbox?: { x: number; y: number; w: number; h: number }
  clip_start?: string
  clip_end?: string
  notified_at?: string
  acked_by?: string
  acked_at?: string
  metadata?: Record<string, unknown> & { snapshot_url?: string }
}

export interface EventListResponse {
  events: TapEvent[]
  cursor: string
}

export interface EventDetailResponse {
  event: TapEvent
  clip: { start?: string; end?: string; playlist?: string }
}

// ---- Motion config (cameras.motion_config) ----

export interface MotionZone {
  name: string
  polygon: [number, number][]
  mode: 'include' | 'exclude'
}

export interface MotionConfig {
  enabled?: boolean
  sensitivity?: number
  min_area_pct?: number
  zones?: MotionZone[]
  schedule?: Record<string, [string, string][]>
  pre_roll_s?: number
  post_roll_s?: number
  cooldown_s?: number
}

// ---- Notifications ----

export type ChannelType = 'smtp' | 'webhook' | 'ntfy' | 'gotify' | 'discord' | 'slack' | 'telegram'

export interface NotifyChannel {
  id: string
  name: string
  type: ChannelType
  config: Record<string, unknown>
  enabled: boolean
  created_at: string
}

export interface ChannelListResponse {
  channels: NotifyChannel[]
}

export interface NotifyRule {
  id: string
  name: string
  enabled: boolean
  camera_ids: string[]
  event_types: string[]
  labels: string[]
  schedule: Record<string, unknown>
  cooldown_s: number
  channel_ids: string[]
  created_at: string
}

export interface RuleListResponse {
  rules: NotifyRule[]
}

export interface NotifyLogEntry {
  id: string
  rule_id?: string
  channel_id?: string
  event_ts?: string
  event_id?: string
  status: 'sent' | 'failed' | 'cooldown_skip'
  error?: string
  sent_at: string
}

export interface NotifyLogResponse {
  log: NotifyLogEntry[]
}

export type ExportStatus = 'pending' | 'processing' | 'done' | 'failed'

export interface ExportJob {
  id: string
  camera_id: string
  start: string
  end: string
  status: ExportStatus
  size_bytes: number
  error: string
  created_at: string
}

export interface ExportListResponse {
  exports: ExportJob[]
}

// ---- WebRTC ----

export interface WebRTCAnswer {
  sdp: string
}

// ---- ONVIF ----

export interface OnvifProfile {
  token: string
  name: string
  codec: string
  width: number
  height: number
  stream_uri: string
  has_ptz: boolean
}

export interface OnvifProbeResponse {
  manufacturer: string
  model: string
  firmware_version: string
  has_ptz: boolean
  has_imaging: boolean
  profiles: OnvifProfile[]
}

export interface OnvifSyncResponse {
  camera: Camera
  probe: OnvifProbeResponse
}

export interface DiscoveredDevice {
  endpoint: string
  name: string
  hardware: string
  location: string
  xaddrs: string[]
}

export interface DiscoverResponse {
  devices: DiscoveredDevice[]
}

// ---- PTZ & imaging ----

export interface PtzMovePayload {
  pan: number
  tilt: number
  zoom: number
  timeout_ms: number
}

export interface PtzPreset {
  token: string
  name: string
}

export interface PtzPresetsResponse {
  presets: PtzPreset[]
}

export interface ImagingSettings {
  brightness?: number
  contrast?: number
  color_saturation?: number
  sharpness?: number
  ir_cut_filter?: string
  wdr_enabled?: boolean
  wdr_level?: number
}

// ---- Storage & settings ----

export interface CameraStorage {
  id: string
  name: string
  local_bytes: number
  s3_bytes: number
  segment_count: number
  bitrate_kbps: number
  retention_days: number
  retention_gb: number | null
  tier_after_days: number | null
}

export interface StorageOverview {
  local: {
    total_bytes: number
    free_bytes: number
  }
  s3: {
    enabled: boolean
    bucket: string
  }
  cameras: CameraStorage[]
}

export interface S3Settings {
  enabled: boolean
  endpoint: string
  secure: boolean
  region: string
  bucket: string
  prefix: string
  access_key: string
  storage_class: string
  secret_set: boolean
}

export interface SystemSettings {
  s3: S3Settings
}

export interface SystemInfo {
  version: string
  uptime_s: number
  ws_clients: number
  public_url: string
}

// ---- Audit log ----

export interface AuditEntry {
  id: number
  user_id: string | null
  action: string
  target: string | null
  detail: Record<string, unknown>
  ip: string | null
  ts: string
}

export interface AuditLogResponse {
  entries: AuditEntry[]
}

// ---- API tokens ----

export interface ApiToken {
  id: string
  name: string
  scopes: string[]
  expires_at: string | null
  last_used_at: string | null
  created_at: string
}

export interface TokenListResponse {
  tokens: ApiToken[]
}

export interface CreateTokenResponse {
  id: string
  token: string
}
