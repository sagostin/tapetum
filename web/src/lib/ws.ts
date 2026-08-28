// WebSocket client for /ws with topic subscriptions and exponential-backoff
// reconnect (docs/08 Realtime Wiring).

export interface WsMessage {
  topic: string
  data: unknown
}

type Handler = (data: unknown) => void

class WsClient {
  private ws: WebSocket | null = null
  private handlers = new Map<string, Set<Handler>>()
  private attempt = 0
  private timer: ReturnType<typeof setTimeout> | null = null
  private stopped = false

  connect() {
    if (this.ws || this.stopped) return
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    this.ws = new WebSocket(`${proto}://${window.location.host}/ws`)
    this.ws.onopen = () => {
      this.attempt = 0
    }
    this.ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data as string) as WsMessage
        const set = this.handlers.get(msg.topic)
        if (set) for (const h of set) h(msg.data)
      } catch {
        // malformed frame — ignore
      }
    }
    this.ws.onclose = () => {
      this.ws = null
      if (!this.stopped) this.scheduleReconnect()
    }
    this.ws.onerror = () => {
      this.ws?.close()
    }
  }

  private scheduleReconnect() {
    if (this.timer) return
    const delay = Math.min(30_000, 1000 * 2 ** this.attempt)
    this.attempt++
    this.timer = setTimeout(() => {
      this.timer = null
      this.connect()
    }, delay)
  }

  on(topic: string, handler: Handler): () => void {
    let set = this.handlers.get(topic)
    if (!set) {
      set = new Set()
      this.handlers.set(topic, set)
    }
    set.add(handler)
    return () => {
      set.delete(handler)
      if (set.size === 0) this.handlers.delete(topic)
    }
  }
}

export const wsClient = new WsClient()
