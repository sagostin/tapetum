<script setup lang="ts">
import { computed } from 'vue'
import LivePlayer from './LivePlayer.vue'
import type { Camera, DisplayRotate } from '../api/types'

/**
 * CameraStage — the shared UI3 player primitive used by the dashboard wall,
 * the camera detail page, and the playback aggregate view.
 *
 * `mode='tile'` strips all chrome (no overlay, no nav buttons, no status
 * badge) so N tiles can sit side-by-side with no visual noise. The parent
 * provides its own hover overlay if it wants per-tile controls.
 *
 * `mode='stage'` shows the full UI3 overlay: camera name, status badge,
 * optional prev/next nav buttons, and a small live-stats line. The parent
 * decides whether to render prev/next by passing the computed refs.
 */

const props = withDefaults(
  defineProps<{
    camera: Camera
    stream?: 'sub' | 'main'
    mode?: 'tile' | 'stage'
    /** When true, click anywhere on the player navigates to /cameras/:id. */
    navigateOnClick?: boolean
    /** Defaults: contain (UI3 Fit). Cover (UI3 Fill) crops to fill the tile. */
    fit?: 'contain' | 'cover'
  }>(),
  {
    stream: 'sub',
    mode: 'stage',
    navigateOnClick: false,
    fit: 'contain',
  },
)

const emit = defineEmits<{
  (e: 'click'): void
  (e: 'navigate'): void
}>()

const rotate = computed<DisplayRotate>(() => props.camera.display_rotate ?? 0)

function handleClick() {
  if (props.mode === 'tile') {
    if (props.navigateOnClick) {
      emit('navigate')
    }
    emit('click')
  }
}
</script>

<template>
  <div
    class="stage"
    :class="[
      `stage-${mode}`,
      { 'stage-rotated': rotate === 90 || rotate === 270 },
    ]"
    @click="handleClick"
  >
    <LivePlayer
      :camera-id="camera.id"
      :stream="stream"
      :hide-badge="mode === 'tile'"
      :fit="fit"
      :rotate="rotate"
      :hflip="camera.display_hflip"
      :vflip="camera.display_vflip"
    />
    <template v-if="mode === 'stage'">
      <div class="stage-overlay">
        <slot name="overlay-left" />
        <slot name="overlay-right" />
      </div>
    </template>
  </div>
</template>

<style scoped>
.stage {
  position: relative;
  width: 100%;
  height: 100%;
  background: #000;
  overflow: hidden;
}

.stage-tile {
  cursor: pointer;
}

.stage-overlay {
  position: absolute;
  left: 0;
  right: 0;
  top: 0;
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.6rem 0.8rem;
  background: linear-gradient(rgba(0, 0, 0, 0.55), transparent);
  gap: 0.5rem;
  pointer-events: none;
}
</style>
