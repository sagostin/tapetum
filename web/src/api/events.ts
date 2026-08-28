import { del, get, post } from './client'
import type { EventDetailResponse, EventListResponse } from './types'

export interface EventFilters {
  camera?: string
  type?: string
  label?: string
  from?: string
  to?: string
  unacked?: boolean
  limit?: number
  cursor?: string
}

export function listEvents(f: EventFilters): Promise<EventListResponse> {
  const params = new URLSearchParams()
  if (f.camera) params.set('camera', f.camera)
  if (f.type) params.set('type', f.type)
  if (f.label) params.set('label', f.label)
  if (f.from) params.set('from', f.from)
  if (f.to) params.set('to', f.to)
  if (f.unacked) params.set('unacked', 'true')
  if (f.limit) params.set('limit', String(f.limit))
  if (f.cursor) params.set('cursor', f.cursor)
  const qs = params.toString()
  return get<EventListResponse>(`/events${qs ? `?${qs}` : ''}`)
}

export function getEvent(id: string): Promise<EventDetailResponse> {
  return get<EventDetailResponse>(`/events/${id}`)
}

export function ackEvent(id: string): Promise<{ acked: boolean }> {
  return post<{ acked: boolean }>(`/events/${id}/ack`)
}

export function deleteEvent(id: string): Promise<void> {
  return del<void>(`/events/${id}`)
}

export function eventSnapshotUrl(id: string): string {
  return `/api/v1/events/${id}/snapshot.jpg`
}

export function eventClipUrl(id: string, transcode = false): string {
  return `/api/v1/events/${id}/clip.m3u8${transcode ? '?transcode=1' : ''}`
}
