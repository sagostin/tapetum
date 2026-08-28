<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import { useToastStore } from '../stores/toasts'
import { wsClient } from '../lib/ws'

const router = useRouter()
const auth = useAuthStore()
const toasts = useToastStore()

const user = computed(() => auth.user)
const canPlayback = computed(() => auth.user?.permissions.includes('playback') ?? false)
const canEvents = computed(() => auth.user?.permissions.includes('events') ?? false)
const canSettings = computed(() => auth.user?.permissions.includes('settings:write') ?? false)
const canUsers = computed(() => auth.user?.permissions.includes('users:write') ?? false)

let offFns: (() => void)[] = []

onMounted(() => {
  wsClient.connect()
  offFns = [
    wsClient.on('event.created', (data) => {
      const d = data as { id?: string; type?: string }
      const what = d?.type === 'ai' ? 'Detection' : 'Motion'
      toasts.push('info', `${what} event`, d?.id ? `/events?id=${d.id}` : '/events')
    }),
    wsClient.on('export.done', (data) => {
      const d = data as { id?: string }
      toasts.push('success', 'Export ready', d?.id ? `/api/v1/exports/${d.id}/download` : undefined)
    }),
    wsClient.on('storage.warning', () => {
      toasts.push('error', 'Storage warning — check Storage settings')
    }),
  ]
})

onBeforeUnmount(() => {
  for (const off of offFns) off()
})

async function logout() {
  await auth.logout()
  await router.push('/login')
}

function dismissToast(id: number) {
  toasts.dismiss(id)
}
</script>

<template>
  <div class="app-shell">
    <header class="topbar">
      <div class="topbar-left">
        <router-link to="/" class="topbar-brand brand-link">Tapetum</router-link>
        <nav class="topbar-nav">
          <router-link to="/" class="nav-link" exact-active-class="nav-active">Dashboard</router-link>
          <router-link to="/cameras" class="nav-link" active-class="nav-active">Cameras</router-link>
          <router-link v-if="canPlayback" to="/playback" class="nav-link" active-class="nav-active">Playback</router-link>
          <router-link v-if="canEvents" to="/events" class="nav-link" active-class="nav-active">Events</router-link>
          <router-link v-if="canSettings" to="/admin/storage" class="nav-link" active-class="nav-active">Storage</router-link>
          <router-link v-if="canSettings" to="/admin/notifications" class="nav-link" active-class="nav-active">Notifications</router-link>
          <router-link v-if="canUsers" to="/admin/system" class="nav-link" active-class="nav-active">System</router-link>
        </nav>
      </div>
      <div class="topbar-right">
        <router-link v-if="user" to="/profile" class="user-chip user-chip-link">
          <span class="user-name">{{ user.display_name || user.username }}</span>
          <span class="user-role">{{ user.role }}</span>
        </router-link>
        <button class="btn btn-ghost" type="button" @click="logout">Log out</button>
      </div>
    </header>
    <main class="content-area">
      <slot />
    </main>
    <div class="toast-stack" aria-live="polite">
      <div
        v-for="t in toasts.toasts"
        :key="t.id"
        class="toast"
        :class="`toast-${t.kind}`"
        @click="dismissToast(t.id)"
      >
        <span>{{ t.text }}</span>
        <a v-if="t.link" :href="t.link" class="toast-link" @click.stop>View</a>
      </div>
    </div>
  </div>
</template>

<style scoped>
.brand-link {
  color: var(--text);
  text-decoration: none;
}

.nav-active {
  color: var(--text);
  background: var(--bg-card);
}

.user-chip-link {
  text-decoration: none;
}

.content-area {
  flex: 1;
  padding: 1.5rem;
  max-width: 1400px;
  width: 100%;
  margin: 0 auto;
}

.toast-stack {
  position: fixed;
  bottom: 1.25rem;
  right: 1.25rem;
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  z-index: 200;
}

.toast {
  display: flex;
  align-items: center;
  gap: 0.75rem;
  padding: 0.7rem 1rem;
  background: var(--bg-card);
  border: 1px solid var(--border);
  border-radius: var(--radius);
  box-shadow: 0 4px 18px rgba(0, 0, 0, 0.4);
  font-size: 0.88rem;
  cursor: pointer;
  animation: toast-in 0.2s ease-out;
}

.toast-success {
  border-color: #166534;
}

.toast-error {
  border-color: var(--danger);
}

.toast-link {
  color: var(--accent);
  text-decoration: none;
  font-weight: 500;
}

@keyframes toast-in {
  from {
    opacity: 0;
    transform: translateY(6px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}
</style>
