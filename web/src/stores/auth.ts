import { defineStore } from 'pinia'
import { get, post, ApiError } from '../api/client'

export interface User {
  id: string
  username: string
  display_name: string
  email: string
  role: string
  permissions: string[]
}

export interface SetupPayload {
  username: string
  password: string
  display_name?: string
  instance_name?: string
}

interface SetupStatusResponse {
  needs_setup: boolean
}

interface AuthState {
  user: User | null
  needsSetup: boolean
  loaded: boolean
}

export const useAuthStore = defineStore('auth', {
  state: (): AuthState => ({
    user: null,
    needsSetup: false,
    loaded: false,
  }),
  actions: {
    async fetchSetupStatus(): Promise<void> {
      const res = await get<SetupStatusResponse>('/setup/status')
      this.needsSetup = res.needs_setup
    },
    async fetchMe(): Promise<void> {
      try {
        this.user = await get<User>('/auth/me', { skipAuthRedirect: true })
      } catch (err) {
        if (err instanceof ApiError && err.status === 401) {
          this.user = null
        } else {
          throw err
        }
      }
    },
    /** One-time initialization used by the router guard. */
    async init(): Promise<void> {
      if (this.loaded) return
      await this.fetchSetupStatus()
      await this.fetchMe()
      this.loaded = true
    },
    async login(username: string, password: string): Promise<void> {
      await post('/auth/login', { username, password })
      await this.fetchMe()
    },
    async logout(): Promise<void> {
      try {
        await post('/auth/logout')
      } finally {
        this.user = null
      }
    },
    async setup(payload: SetupPayload): Promise<void> {
      await post('/setup', payload)
      this.needsSetup = false
      await this.fetchMe()
    },
  },
})
