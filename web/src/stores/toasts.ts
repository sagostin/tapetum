import { defineStore } from 'pinia'

export interface Toast {
  id: number
  kind: 'info' | 'success' | 'error'
  text: string
  link?: string
}

let nextId = 1

export const useToastStore = defineStore('toasts', {
  state: () => ({ toasts: [] as Toast[] }),
  actions: {
    push(kind: Toast['kind'], text: string, link?: string) {
      const toast: Toast = { id: nextId++, kind, text, link }
      this.toasts.push(toast)
      setTimeout(() => this.dismiss(toast.id), 6000)
    },
    dismiss(id: number) {
      this.toasts = this.toasts.filter((t) => t.id !== id)
    },
  },
})
