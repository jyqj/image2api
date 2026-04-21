import { createRouter, createWebHistory, type RouteRecordRaw } from 'vue-router'
import BasicLayout from '@/layouts/BasicLayout.vue'

const routes: RouteRecordRaw[] = [
  {
    path: '/login',
    component: () => import('@/views/Login.vue'),
    meta: { title: '登录', public: true },
  },
  {
    path: '/',
    component: BasicLayout,
    redirect: '/personal/dashboard',
    children: [
      { path: 'personal/dashboard', component: () => import('@/views/personal/Dashboard.vue'), meta: { title: '本地总览' } },
      { path: 'personal/usage', component: () => import('@/views/personal/Usage.vue'), meta: { title: '用量记录' } },
      { path: 'personal/play', component: () => import('@/views/personal/OnlinePlay.vue'), meta: { title: '在线体验' } },
      { path: 'personal/docs', component: () => import('@/views/personal/ApiDocs.vue'), meta: { title: '接口文档' } },
      { path: 'admin/accounts', component: () => import('@/views/admin/Accounts.vue'), meta: { title: '上游账号池' } },
      { path: 'admin/proxies', component: () => import('@/views/admin/Proxies.vue'), meta: { title: '代理池' } },
      { path: 'admin/models', component: () => import('@/views/admin/Models.vue'), meta: { title: '模型映射' } },
      { path: 'admin/usage', component: () => import('@/views/admin/UsageStats.vue'), meta: { title: '全局用量' } },
      { path: 'admin/audit', component: () => import('@/views/admin/Audit.vue'), meta: { title: '审计日志' } },
      { path: 'admin/backup', component: () => import('@/views/admin/Backup.vue'), meta: { title: '备份恢复' } },
      { path: 'admin/settings', component: () => import('@/views/admin/Settings.vue'), meta: { title: '系统设置' } },
      { path: 'personal/playground', redirect: '/personal/play' },
      { path: 'personal/images', redirect: '/personal/play' },
      { path: 'personal/keys', redirect: '/personal/docs' },
      { path: 'admin/users', redirect: '/admin/accounts' },
      { path: 'admin/groups', redirect: '/admin/accounts' },
      { path: 'admin/keys', redirect: '/admin/models' },
    ],
  },
  { path: '/403', component: () => import('@/views/Error403.vue'), meta: { title: '403', public: true } },
  { path: '/:pathMatch(.*)*', component: () => import('@/views/Error404.vue'), meta: { title: '404', public: true } },
]

const router = createRouter({ history: createWebHistory(), routes })

router.beforeEach((to) => {
  document.title = (to.meta.title as string) || 'Image2API 控制台'
  // 公开页面不需要登录
  if (to.meta.public) return true
  // 检查 token
  const token = localStorage.getItem('admin_token')
  if (!token) {
    return { path: '/login', query: { redirect: to.fullPath } }
  }
  return true
})

export default router
