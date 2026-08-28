// Limits concurrent RTCPeerConnection setups across the app so the browser
// doesn't lock up when many LivePlayer tiles mount at once. Each WebRTC
// setup involves ICE gathering (~2s on most browsers), so doing N of them
// in parallel freezes the main thread.
const MAX_CONCURRENT = 2
let active = 0
const queue: Array<() => void> = []

export function acquireWebRTCSlot(): Promise<void> {
  return new Promise((resolve) => {
    if (active < MAX_CONCURRENT) {
      active++
      resolve()
      return
    }
    queue.push(() => {
      active++
      resolve()
    })
  })
}

export function releaseWebRTCSlot(): void {
  active = Math.max(0, active - 1)
  const next = queue.shift()
  if (next) next()
}
