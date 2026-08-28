// Camera-related API helpers. The base /api/v1/cameras routes are called
// directly from views, but anything more specific (display orientation, etc.)
// lives here so the view layer doesn't construct raw paths.
import { patch } from './client'
import type { Camera, CameraDisplayUpdate } from './types'

export function patchCameraDisplay(
  cameraId: string,
  body: CameraDisplayUpdate,
): Promise<Camera> {
  return patch<Camera>(`/cameras/${cameraId}/display`, body)
}
