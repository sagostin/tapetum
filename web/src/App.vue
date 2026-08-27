<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import AppShell from './components/AppShell.vue'

const route = useRoute()
const bare = computed(() => route.path === '/login' || route.path === '/setup')
</script>

<template>
  <router-view v-if="bare" />
  <AppShell v-else>
    <router-view />
  </AppShell>
</template>
