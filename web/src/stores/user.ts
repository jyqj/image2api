import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import axios from 'axios'

export interface LocalUser {
  email: string
  nickname: string
}

export interface MenuItem {
  key: string
  title: string
  icon?: string
  path?: string
  children?: MenuItem[]
}

const LOCAL_MENU: MenuItem[] = [
  {
    key: 'local',
    title: '本地中转',
    icon: 'Monitor',
    children: [
      { key: 'dashboard', title: '本地总览', icon: 'DataBoard', path: '/personal/dashboard' },
      { key: 'play', title: '在线体验', icon: 'MagicStick', path: '/personal/play' },
      { key: 'usage', title: '用量记录', icon: 'TrendCharts', path: '/personal/usage' },
      { key: 'docs', title: '接口文档', icon: 'Document', path: '/personal/docs' },
    ],
  },
  {
    key: 'ops',
    title: '运维管理',
    icon: 'Operation',
    children: [
      { key: 'accounts', title: '上游账号池', icon: 'UserFilled', path: '/admin/accounts' },
      { key: 'proxies', title: '代理池', icon: 'Connection', path: '/admin/proxies' },
      { key: 'models', title: '模型映射', icon: 'Grid', path: '/admin/models' },
      { key: 'admin_usage', title: '全局用量', icon: 'Histogram', path: '/admin/usage' },
      { key: 'audit', title: '审计日志', icon: 'Memo', path: '/admin/audit' },
      { key: 'backup', title: '备份恢复', icon: 'FolderChecked', path: '/admin/backup' },
      { key: 'settings', title: '系统设置', icon: 'Setting', path: '/admin/settings' },
    ],
  },
]

const TOKEN_KEY = 'admin_token'

export const useUserStore = defineStore('user', () => {
  const token = ref<string>(localStorage.getItem(TOKEN_KEY) || '')
  const user = ref<LocalUser>({ email: '', nickname: '' })
  const permissions = ref<string[]>(['local:*'])
  const role = ref<string>('local')
  const menu = ref<MenuItem[]>(LOCAL_MENU)

  const isLoggedIn = computed(() => !!token.value)
  const isAdmin = computed(() => !!token.value)

  async function login(username: string, password: string) {
    const baseURL = import.meta.env.VITE_API_BASE || ''
    const resp = await axios.post(`${baseURL}/api/admin/login`, { username, password })
    const data = resp.data?.data || resp.data
    if (!data?.token) {
      throw new Error(resp.data?.message || '登录失败')
    }
    token.value = data.token
    user.value = { email: data.username, nickname: data.username }
    localStorage.setItem(TOKEN_KEY, data.token)
  }

  async function fetchMe() {
    return { user: user.value, role: role.value, permissions: permissions.value }
  }

  async function fetchMenu() {
    return { menu: menu.value, role: role.value, permissions: permissions.value }
  }

  function hasPerm(): boolean { return true }

  function clear() {
    token.value = ''
    user.value = { email: '', nickname: '' }
    localStorage.removeItem(TOKEN_KEY)
  }

  async function logout() {
    clear()
  }

  return { token, user, permissions, role, menu, isLoggedIn, isAdmin, login, fetchMe, fetchMenu, hasPerm, clear, logout }
})
