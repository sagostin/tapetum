import { del, get, patch, post } from './client'
import type {
  ChannelListResponse,
  NotifyChannel,
  NotifyLogResponse,
  NotifyRule,
  RuleListResponse,
} from './types'

export interface ChannelPayload {
  name: string
  type: string
  config: Record<string, unknown>
  enabled?: boolean
}

export function listChannels(): Promise<ChannelListResponse> {
  return get<ChannelListResponse>('/notify/channels')
}

export function createChannel(p: ChannelPayload): Promise<NotifyChannel> {
  return post<NotifyChannel>('/notify/channels', p)
}

export function updateChannel(id: string, p: Partial<ChannelPayload>): Promise<NotifyChannel> {
  return patch<NotifyChannel>(`/notify/channels/${id}`, p)
}

export function deleteChannel(id: string): Promise<void> {
  return del<void>(`/notify/channels/${id}`)
}

export function testChannel(id: string): Promise<{ status: string }> {
  return post<{ status: string }>(`/notify/channels/${id}/test`)
}

export interface RulePayload {
  name: string
  enabled?: boolean
  camera_ids?: string[]
  event_types?: string[]
  labels?: string[]
  schedule?: Record<string, unknown>
  cooldown_s?: number
  channel_ids?: string[]
}

export function listRules(): Promise<RuleListResponse> {
  return get<RuleListResponse>('/notify/rules')
}

export function createRule(p: RulePayload): Promise<NotifyRule> {
  return post<NotifyRule>('/notify/rules', p)
}

export function updateRule(id: string, p: Partial<RulePayload>): Promise<NotifyRule> {
  return patch<NotifyRule>(`/notify/rules/${id}`, p)
}

export function deleteRule(id: string): Promise<void> {
  return del<void>(`/notify/rules/${id}`)
}

export function notifyLog(rule?: string, status?: string, limit = 100): Promise<NotifyLogResponse> {
  const params = new URLSearchParams()
  if (rule) params.set('rule', rule)
  if (status) params.set('status', status)
  params.set('limit', String(limit))
  return get<NotifyLogResponse>(`/notify/log?${params.toString()}`)
}
