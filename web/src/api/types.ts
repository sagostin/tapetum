export type CameraStatus = 'online' | 'offline' | 'degraded'
export type Transport = 'tcp' | 'udp' | 'auto'
export type RecordMode = 'continuous' | 'motion' | 'off'

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
  record_mode: RecordMode
  retention_days: number
  retention_gb: number
  group_id: string
  status: CameraStatus
  status_detail: CameraStatusDetail | null
  last_seen_at: string
  created_at: string
  updated_at: string
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

export interface TimelineResponse {
  camera_id: string
  from: string
  to: string
  buckets: number
  density: number[]
  recorded: TimelineRange[]
  events: unknown[]
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
