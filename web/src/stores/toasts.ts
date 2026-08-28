import { defineStore } from 'pinia'

export interface Toast {
  id: number
  kind: 'info' | 'success' | 'error'
  text: string
  link?: string
}

const MAX_TOASTS = 8
const TOAST_TTL_MS = 6000

let nextId = 1

export const useToastStore = defineStore('toasts', {
  state: () => ({ toasts: [] as Toast[] }),
  actions: {
    push(kind: Toast['kind'], text: string, link?: string) {
      const toast: Toast = { id: nextId++, kind, text, link }
      this.toasts.push(toast)
      if (this.toasts.length > MAX_TOASTS) {
        this.toasts.splice(0, this.toasts.length - MAX_TOASTS)
      }
      setTimeout(() => this.dismiss(toast.id), TOAST_TTL_MS)
    },
    dismiss(id: number) {
      const i = this.toasts.findIndex((t) => t.id === id)
      if (i >= 0) this.toasts.splice(i, 1)
    },
  },
})
