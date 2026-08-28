import { defineStore } from 'pinia'
import { get as apiGet } from '../api/client'
import type { Camera } from '../api/types'

interface CameraState {
  cameras: Record<string, Camera>
  loaded: boolean
}

export const useCamerasStore = defineStore('cameras', {
  state: (): CameraState => ({
    cameras: {},
    loaded: false,
  }),
  getters: {
    list: (s): Camera[] =>
      Object.values(s.cameras).sort((a, b) => a.name.localeCompare(b.name)),
    byId:
      (s) =>
      (id: string): Camera | undefined =>
        s.cameras[id],
  },
  actions: {
    async refresh(): Promise<void> {
      const res = await apiGet<{ cameras: Camera[] }>('/cameras')
      const next: Record<string, Camera> = {}
      for (const c of res.cameras ?? []) next[c.id] = c
      this.cameras = next
      this.loaded = true
    },
    upsert(c: Camera): void {
      this.cameras = { ...this.cameras, [c.id]: c }
    },
    applyStatus(cameraId: string, status: Camera['status'], detail?: Record<string, unknown>): void {
      const cur = this.cameras[cameraId]
      if (!cur) return
      const mergedDetail = detail
        ? ({ ...(cur.status_detail ?? {}), ...detail } as Camera['status_detail'])
        : cur.status_detail
      const next: Camera = { ...cur, status, status_detail: mergedDetail }
      this.cameras = { ...this.cameras, [cameraId]: next }
    },
    remove(id: string): void {
      if (!(id in this.cameras)) return
      const { [id]: _drop, ...rest } = this.cameras
      this.cameras = rest
    },
  },
})