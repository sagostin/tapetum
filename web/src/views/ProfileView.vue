<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { get, post, del, ApiError } from '../api/client'
import type { ApiToken, CreateTokenResponse, TokenListResponse } from '../api/types'
import { useAuthStore } from '../stores/auth'
import { formatDateTime, fromLocalInputValue } from '../utils/format'

const auth = useAuthStore()

// ---- Tokens ----
const tokens = ref<ApiToken[]>([])
const tokensError = ref('')

const newName = ref('')
const newExpires = ref('')
const newScopes = ref<string[]>([])
const creating = ref(false)
const createError = ref('')
const createdToken = ref('')

const availableScopes = computed(() => auth.user?.permissions ?? [])

async function fetchTokens() {
  try {
    const res = await get<TokenListResponse>('/auth/tokens')
    tokens.value = res.tokens ?? []
    tokensError.value = ''
  } catch (err) {
    tokensError.value = err instanceof ApiError ? err.message : 'Failed to load tokens'
  }
}

async function createToken() {
  creating.value = true
  createError.value = ''
  createdToken.value = ''
  try {
    const expiresMs = newExpires.value ? fromLocalInputValue(newExpires.value) : 0
    const res = await post<CreateTokenResponse>('/auth/tokens', {
      name: newName.value.trim(),
      scopes: newScopes.value,
      expires_at: expiresMs ? new Date(expiresMs).toISOString() : undefined,
    })
    createdToken.value = res.token
    newName.value = ''
    newExpires.value = ''
    newScopes.value = []
    await fetchTokens()
  } catch (err) {
    createError.value = err instanceof ApiError ? err.message : 'Failed to create token'
  } finally {
    creating.value = false
  }
}

async function revokeToken(t: ApiToken) {
  createError.value = ''
  try {
    await del(`/auth/tokens/${t.id}`)
    await fetchTokens()
  } catch (err) {
    createError.value = err instanceof ApiError ? err.message : 'Failed to revoke token'
  }
}

async function copyToken() {
  try {
    await navigator.clipboard.writeText(createdToken.value)
  } catch {
    // Clipboard unavailable — user can select the text manually.
  }
}

// ---- Password ----
const pwCurrent = ref('')
const pwNew = ref('')
const pwConfirm = ref('')
const pwSaving = ref(false)
const pwError = ref('')
const pwOk = ref(false)

async function changePassword() {
  pwError.value = ''
  pwOk.value = false
  if (pwNew.value !== pwConfirm.value) {
    pwError.value = 'New passwords do not match.'
    return
  }
  pwSaving.value = true
  try {
    await post('/auth/password', { current: pwCurrent.value, new: pwNew.value })
    pwOk.value = true
    pwCurrent.value = ''
    pwNew.value = ''
    pwConfirm.value = ''
  } catch (err) {
    pwError.value = err instanceof ApiError ? err.message : 'Failed to change password'
  } finally {
    pwSaving.value = false
  }
}

onMounted(fetchTokens)
</script>

<template>
  <div>
    <div class="page-header">
      <h1 class="page-title">Profile</h1>
    </div>

    <div class="card section-card">
      <h2 class="section-title">API tokens</h2>

      <p v-if="tokensError" class="error-text">{{ tokensError }}</p>

      <table v-if="tokens.length" class="token-table">
        <thead>
          <tr>
            <th>Name</th>
            <th>Scopes</th>
            <th>Expires</th>
            <th>Last used</th>
            <th>Created</th>
            <th class="actions-col"></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="t in tokens" :key="t.id">
            <td>{{ t.name }}</td>
            <td class="mono">{{ t.scopes.join(', ') || '—' }}</td>
            <td class="mono">{{ t.expires_at ? formatDateTime(new Date(t.expires_at).getTime()) : 'never' }}</td>
            <td class="mono">{{ t.last_used_at ? formatDateTime(new Date(t.last_used_at).getTime()) : '—' }}</td>
            <td class="mono">{{ formatDateTime(new Date(t.created_at).getTime()) }}</td>
            <td class="actions-col">
              <button class="btn btn-ghost btn-sm btn-danger" type="button" @click="revokeToken(t)">
                Revoke
              </button>
            </td>
          </tr>
        </tbody>
      </table>
      <p v-else class="muted">No tokens yet.</p>

      <form class="token-form" @submit.prevent="createToken">
        <div class="field-row">
          <label class="field">
            <span>Name</span>
            <input v-model="newName" type="text" required maxlength="64" placeholder="home-automation" />
          </label>
          <label class="field">
            <span>Expires <em>optional</em></span>
            <input v-model="newExpires" type="datetime-local" />
          </label>
        </div>

        <div v-if="availableScopes.length" class="scopes-row">
          <label v-for="scope in availableScopes" :key="scope" class="check-row">
            <input v-model="newScopes" type="checkbox" :value="scope" />
            <span class="mono">{{ scope }}</span>
          </label>
        </div>

        <p v-if="createError" class="error-text">{{ createError }}</p>

        <div class="form-actions">
          <button class="btn btn-primary btn-inline" type="submit" :disabled="creating || !newName.trim()">
            {{ creating ? 'Creating…' : 'Create token' }}
          </button>
        </div>
      </form>

      <div v-if="createdToken" class="token-reveal">
        <p class="token-warning">
          This is the only time the token will be shown. Copy it now and store it somewhere safe.
        </p>
        <div class="token-block">
          <code class="token-value">{{ createdToken }}</code>
          <button class="btn btn-ghost btn-sm" type="button" @click="copyToken">Copy</button>
        </div>
      </div>
    </div>

    <div class="card section-card">
      <h2 class="section-title">Change password</h2>
      <form @submit.prevent="changePassword">
        <label class="field">
          <span>Current password</span>
          <input v-model="pwCurrent" type="password" required autocomplete="current-password" />
        </label>
        <div class="field-row">
          <label class="field">
            <span>New password</span>
            <input v-model="pwNew" type="password" required autocomplete="new-password" />
          </label>
          <label class="field">
            <span>Confirm new password</span>
            <input v-model="pwConfirm" type="password" required autocomplete="new-password" />
          </label>
        </div>

        <p v-if="pwError" class="error-text">{{ pwError }}</p>
        <p v-if="pwOk" class="ok-text">Password changed.</p>

        <div class="form-actions">
          <button class="btn btn-primary btn-inline" type="submit" :disabled="pwSaving">
            {{ pwSaving ? 'Saving…' : 'Change password' }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>

<style scoped>
.page-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 1.25rem;
}

.page-title {
  margin: 0;
  font-size: 1.3rem;
  font-weight: 600;
}

.section-card {
  padding: 1.25rem 1.5rem;
  max-width: 860px;
  margin-bottom: 1rem;
}

.section-title {
  margin: 0 0 1rem;
  font-size: 0.85rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
}

.muted {
  color: var(--text-muted);
}

.mono {
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 0.85rem;
  color: var(--text-muted);
}

.token-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 0.92rem;
  margin-bottom: 1.25rem;
}

.token-table th {
  text-align: left;
  font-size: 0.78rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.05em;
  color: var(--text-muted);
  padding: 0.5rem 0.75rem;
  border-bottom: 1px solid var(--border);
}

.token-table td {
  padding: 0.6rem 0.75rem;
  border-bottom: 1px solid var(--border);
  vertical-align: middle;
}

.token-table tbody tr:last-child td {
  border-bottom: none;
}

.actions-col {
  text-align: right;
  white-space: nowrap;
}

.btn-sm {
  padding: 0.35rem 0.7rem;
  font-size: 0.85rem;
}

.btn-danger {
  color: var(--danger);
}

.btn-danger:hover {
  border-color: var(--danger);
  color: var(--danger);
}

.token-form {
  border-top: 1px solid var(--border);
  padding-top: 1.25rem;
}

.field-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 0.9rem;
}

.scopes-row {
  display: flex;
  flex-wrap: wrap;
  gap: 0.4rem 1.25rem;
  margin-bottom: 1.1rem;
}

.check-row {
  display: flex;
  align-items: center;
  gap: 0.45rem;
  font-size: 0.88rem;
  cursor: pointer;
}

.form-actions {
  display: flex;
  justify-content: flex-end;
}

.btn-inline {
  width: auto;
}

.ok-text {
  color: #4ade80;
  font-size: 0.9rem;
  margin: 0 0 1rem;
}

.token-reveal {
  margin-top: 1.25rem;
  padding: 1rem;
  border: 1px solid rgba(251, 191, 36, 0.4);
  border-radius: 8px;
  background: rgba(251, 191, 36, 0.06);
}

.token-warning {
  margin: 0 0 0.6rem;
  font-size: 0.88rem;
  color: #fbbf24;
}

.token-block {
  display: flex;
  align-items: center;
  gap: 0.6rem;
}

.token-value {
  flex: 1;
  font-family: 'SF Mono', 'Menlo', monospace;
  font-size: 0.85rem;
  word-break: break-all;
  user-select: all;
}
</style>
