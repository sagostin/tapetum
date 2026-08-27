// startWebRTC negotiates a recvonly WebRTC session against the Tapetum
// signaling endpoint. The server gathers ICE candidates itself and returns a
// complete answer — no trickle. Returns a cleanup function; onerror is called
// exactly once on any failure (after which the session is torn down).
export async function startWebRTC(
  videoEl: HTMLVideoElement,
  cameraId: string,
  stream: 'sub' | 'main',
  api: { post(path: string, body: unknown): Promise<unknown> },
  onerror: (err: Error) => void,
): Promise<() => void> {
  const pc = new RTCPeerConnection()

  let settled = false
  let gotTrack = false
  let trackTimer: ReturnType<typeof setTimeout> | null = null
  let iceTimer: ReturnType<typeof setTimeout> | null = null

  function clearTimers() {
    if (trackTimer) {
      clearTimeout(trackTimer)
      trackTimer = null
    }
    if (iceTimer) {
      clearTimeout(iceTimer)
      iceTimer = null
    }
  }

  function teardown() {
    clearTimers()
    try {
      pc.close()
    } catch {
      // Already closed.
    }
    if (videoEl.srcObject) {
      videoEl.srcObject = null
    }
  }

  function fail(err: Error) {
    if (settled) return
    settled = true
    teardown()
    onerror(err)
  }

  pc.ontrack = (event) => {
    gotTrack = true
    if (trackTimer) {
      clearTimeout(trackTimer)
      trackTimer = null
    }
    videoEl.srcObject = event.streams[0]
    videoEl.play().catch(() => {
      // Autoplay blocked — controls/user gesture can start it.
    })
  }

  pc.oniceconnectionstatechange = () => {
    const state = pc.iceConnectionState
    if (state === 'failed' || state === 'disconnected') {
      if (!iceTimer) {
        iceTimer = setTimeout(() => {
          fail(new Error(`WebRTC connection ${state}`))
        }, 3000)
      }
    } else if (iceTimer) {
      clearTimeout(iceTimer)
      iceTimer = null
    }
    if (state === 'closed') {
      clearTimers()
    }
  }

  // No track within 10s → treat as failure.
  trackTimer = setTimeout(() => {
    if (!gotTrack) {
      fail(new Error('WebRTC: no track received'))
    }
  }, 10_000)

  try {
    pc.addTransceiver('video', { direction: 'recvonly' })
    const offer = await pc.createOffer()
    await pc.setLocalDescription(offer)

    // No trickle: gather our candidates into the offer before POSTing.
    await new Promise<void>((resolve) => {
      if (pc.iceGatheringState === 'complete') return resolve()
      const timer = setTimeout(resolve, 2000)
      pc.onicegatheringstatechange = () => {
        if (pc.iceGatheringState === 'complete') {
          clearTimeout(timer)
          resolve()
        }
      }
    })

    const res = (await api.post(`/streams/${cameraId}/webrtc`, {
      sdp: pc.localDescription?.sdp ?? '',
      stream,
    })) as { sdp: string }

    if (settled) return teardown
    await pc.setRemoteDescription({ type: 'answer', sdp: res.sdp })
  } catch (err) {
    fail(err instanceof Error ? err : new Error(String(err)))
  }

  function teardownOnce() {
    if (settled) return
    settled = true
    teardown()
  }

  return teardownOnce
}
