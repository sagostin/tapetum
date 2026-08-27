<script setup lang="ts">
import type { CameraStatus } from '../api/types'

defineProps<{ status: CameraStatus }>()
</script>

<template>
  <span class="status-badge" :class="`status-${status}`">{{ status }}</span>
</template>

<style scoped>
.status-badge {
  display: inline-block;
  font-size: 0.72rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  padding: 0.15rem 0.5rem;
  border-radius: 4px;
}

.status-online {
  color: #4ade80;
  background: rgba(74, 222, 128, 0.12);
  border: 1px solid rgba(74, 222, 128, 0.3);
}

.status-degraded {
  color: #fbbf24;
  background: rgba(251, 191, 36, 0.12);
  border: 1px solid rgba(251, 191, 36, 0.3);
}

.status-offline {
  color: var(--danger);
  background: rgba(255, 92, 108, 0.12);
  border: 1px solid rgba(255, 92, 108, 0.3);
}
</style>
