<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const router = useRouter()
const auth = useAuthStore()

const user = computed(() => auth.user)
const canPlayback = computed(() => auth.user?.permissions.includes('playback') ?? false)
const canSettings = computed(() => auth.user?.permissions.includes('settings:write') ?? false)
const canUsers = computed(() => auth.user?.permissions.includes('users:write') ?? false)

async function logout() {
  await auth.logout()
  await router.push('/login')
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
          <router-link v-if="canSettings" to="/admin/storage" class="nav-link" active-class="nav-active">Storage</router-link>
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
</style>
