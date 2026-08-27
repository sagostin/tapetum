<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '../stores/auth'

const router = useRouter()
const auth = useAuthStore()

const username = ref('')
const displayName = ref('')
const password = ref('')
const confirmPassword = ref('')
const instanceName = ref('')

const error = ref('')
const submitting = ref(false)

async function submit() {
  error.value = ''

  if (password.value !== confirmPassword.value) {
    error.value = 'Passwords do not match.'
    return
  }

  submitting.value = true
  try {
    await auth.setup({
      username: username.value,
      password: password.value,
      ...(displayName.value ? { display_name: displayName.value } : {}),
      ...(instanceName.value ? { instance_name: instanceName.value } : {}),
    })
    await router.push('/')
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Setup failed.'
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth-page">
    <div class="card auth-card">
      <h1 class="brand">Tapetum</h1>
      <p class="subtitle">Welcome. Create the initial admin account to finish setting up your NVR.</p>

      <form @submit.prevent="submit">
        <label class="field">
          <span>Username</span>
          <input v-model="username" type="text" required autocomplete="username" />
        </label>

        <label class="field">
          <span>Display name <em>(optional)</em></span>
          <input v-model="displayName" type="text" autocomplete="name" />
        </label>

        <label class="field">
          <span>Password</span>
          <input v-model="password" type="password" required minlength="10" autocomplete="new-password" />
        </label>

        <label class="field">
          <span>Confirm password</span>
          <input v-model="confirmPassword" type="password" required autocomplete="new-password" />
        </label>

        <label class="field">
          <span>Instance name <em>(optional)</em></span>
          <input v-model="instanceName" type="text" placeholder="Home" />
        </label>

        <p v-if="error" class="error-text">{{ error }}</p>

        <button class="btn btn-primary" type="submit" :disabled="submitting">
          {{ submitting ? 'Creating…' : 'Create admin account' }}
        </button>
      </form>
    </div>
  </div>
</template>
