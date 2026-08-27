import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../stores/auth'
import SetupView from '../views/SetupView.vue'
import LoginView from '../views/LoginView.vue'
import DashboardView from '../views/DashboardView.vue'

const CamerasView = () => import('../views/CamerasView.vue')
const CameraDetailView = () => import('../views/CameraDetailView.vue')
const PlaybackView = () => import('../views/PlaybackView.vue')

const router = createRouter({
  history: createWebHistory(),
  routes: [
    {
      path: '/setup',
      name: 'setup',
      component: SetupView,
    },
    {
      path: '/login',
      name: 'login',
      component: LoginView,
    },
    {
      path: '/',
      name: 'dashboard',
      component: DashboardView,
      meta: { requiresAuth: true },
    },
    {
      path: '/cameras',
      name: 'cameras',
      component: CamerasView,
      meta: { requiresAuth: true },
    },
    {
      path: '/cameras/:id',
      name: 'camera-detail',
      component: CameraDetailView,
      meta: { requiresAuth: true },
    },
    {
      path: '/playback',
      redirect: '/',
    },
    {
      path: '/playback/:cameraId',
      name: 'playback',
      component: PlaybackView,
      meta: { requiresAuth: true, requiresPerm: 'playback' },
    },
  ],
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()
  await auth.init()

  // First-run wizard takes priority over everything.
  if (auth.needsSetup) {
    return to.path === '/setup' ? true : { path: '/setup' }
  }
  if (to.path === '/setup') {
    return { path: '/' }
  }

  if (to.meta.requiresAuth && !auth.user) {
    return { path: '/login' }
  }
  if (to.path === '/login' && auth.user) {
    return { path: '/' }
  }

  const requiredPerm = to.meta.requiresPerm as string | undefined
  if (requiredPerm && !auth.user?.permissions.includes(requiredPerm)) {
    return { path: '/' }
  }

  return true
})

export default router
