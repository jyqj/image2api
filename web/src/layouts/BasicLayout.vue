<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { storeToRefs } from 'pinia'
import { useUserStore } from '@/stores/user'
import { useUIStore } from '@/stores/ui'
import { useSiteStore } from '@/stores/site'
import type { MenuItem } from '@/stores/user'

const store = useUserStore()
const ui = useUIStore()
const site = useSiteStore()
const router = useRouter()
const route = useRoute()

const siteName = computed(() => site.get('site.name', 'image2api'))
const siteLogo = computed(() => site.get('site.logo_url', ''))
const siteFooter = computed(() => site.get('site.footer', ''))

const { menu, user, role, permissions } = storeToRefs(store)
const collapsed = ref(false)
const loadingMenu = ref(false)

const activePath = computed(() => route.path)

const titleMap = computed(() => {
  const m = new Map<string, string>()
  function walk(items: MenuItem[]) {
    for (const it of items) {
      if (it.path) m.set(it.path, it.title)
      if (it.children) walk(it.children)
    }
  }
  walk(menu.value)
  return m
})

const currentTitle = computed(() => titleMap.value.get(activePath.value) || (route.meta.title as string) || '')

async function loadMenu() {
  if (menu.value.length > 0) return
  loadingMenu.value = true
  try {
    await store.fetchMenu()
  } finally {
    loadingMenu.value = false
  }
}

async function logout() {
  await store.logout()
  router.replace('/login')
}

function goto(path?: string) {
  if (path) router.push(path)
}

onMounted(loadMenu)
watch(() => store.isLoggedIn, (v) => { if (v) loadMenu() })
</script>

<template>
  <el-container class="layout-root">
    <el-aside :width="collapsed ? '64px' : '220px'" class="sidebar">
      <div class="logo">
        <img v-if="siteLogo" :src="siteLogo" class="logo-img" alt="logo" />
        <span v-else class="mark">{{ (siteName[0] || 'G').toUpperCase() }}</span>
        <span v-if="!collapsed" class="title">{{ siteName }}</span>
      </div>
      <el-menu
        :default-active="activePath"
        :collapse="collapsed"
        background-color="transparent"
        text-color="var(--el-text-color-regular)"
        active-text-color="#409eff"
        class="side-menu"
        router
      >
        <template v-for="group in menu" :key="group.key">
          <el-menu-item v-if="!group.children?.length && group.path" :index="group.path">
            <el-icon v-if="group.icon"><component :is="group.icon" /></el-icon>
            <template #title>{{ group.title }}</template>
          </el-menu-item>
          <el-sub-menu v-else-if="group.children?.length" :index="group.key">
            <template #title>
              <el-icon v-if="group.icon"><component :is="group.icon" /></el-icon>
              <span>{{ group.title }}</span>
            </template>
            <el-menu-item
              v-for="child in group.children"
              :key="child.key"
              :index="child.path!"
            >
              <el-icon v-if="child.icon"><component :is="child.icon" /></el-icon>
              <template #title>{{ child.title }}</template>
            </el-menu-item>
          </el-sub-menu>
        </template>
      </el-menu>
    </el-aside>

    <el-container>
      <el-header class="topbar">
        <div class="left">
          <el-button link @click="collapsed = !collapsed">
            <el-icon :size="18"><component :is="collapsed ? 'Expand' : 'Fold'" /></el-icon>
          </el-button>
          <span class="crumb">{{ currentTitle }}</span>
        </div>
        <div class="right">
          <el-tooltip :content="ui.isDark ? '切换到亮色' : '切换到暗色'" placement="bottom">
            <el-button link class="theme-btn" @click="ui.toggleDark()">
              <el-icon :size="18">
                <component :is="ui.isDark ? 'Sunny' : 'Moon'" />
              </el-icon>
            </el-button>
          </el-tooltip>
          <el-dropdown trigger="click" @command="(c: string) => c === 'logout' ? logout() : goto(c)">
            <span class="user-entry">
              <el-avatar :size="28" style="background:#409eff">
                {{ (user?.nickname || user?.email || 'U').slice(0, 1).toUpperCase() }}
              </el-avatar>
              <span class="nick">{{ user?.nickname || user?.email }}</span>
              <el-tag type="success" size="small">本地</el-tag>
              <el-icon><ArrowDown /></el-icon>
            </span>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="/personal/dashboard">
                  <el-icon><User /></el-icon> 本地总览
                </el-dropdown-item>
                <el-dropdown-item command="/admin/settings">
                  <el-icon><Setting /></el-icon> 系统设置
                </el-dropdown-item>
                <el-dropdown-item divided command="logout">
                  <el-icon><SwitchButton /></el-icon> 返回总览
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
        </div>
      </el-header>

      <el-main class="main" v-loading="loadingMenu">
        <router-view v-slot="{ Component }">
          <transition name="fade" mode="out-in">
            <component :is="Component" />
          </transition>
        </router-view>
      </el-main>

      <el-footer class="footer">
        <div v-if="siteFooter" class="footer-line footer-custom">{{ siteFooter }}</div>
        <div v-else class="footer-line">&copy; {{ new Date().getFullYear() }} {{ siteName }}</div>
      </el-footer>
    </el-container>
  </el-container>
</template>

<style scoped lang="scss">
.layout-root { height: 100vh; }

/* ===================== Sidebar ===================== */
.sidebar {
  background: var(--el-bg-color);
  border-right: 1px solid var(--el-border-color-lighter);
  transition: width .25s cubic-bezier(.4, 0, .2, 1);
  overflow-x: hidden;
  overflow-y: auto;
  display: flex;
  flex-direction: column;
}

.logo {
  height: 64px;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 0 18px;
  color: #fff;
  font-weight: 700;
  flex-shrink: 0;
  border-bottom: 1px solid var(--el-border-color-lighter);
  .logo-img {
    width: 34px; height: 34px; border-radius: 10px; object-fit: contain;
  }
  .mark {
    display: inline-flex;
    width: 34px;
    height: 34px;
    border-radius: 10px;
    background: linear-gradient(135deg,#409eff,#67c23a);
    align-items: center; justify-content: center;
    font-size: 15px;
    font-weight: 800;
    color: #fff;
    flex-shrink: 0;
  }
  .title { font-size: 15px; white-space: nowrap; letter-spacing: 0.5px; color: var(--el-text-color-primary); }
}

.side-menu {
  border-right: none;
  flex: 1;
  padding: 8px 0;
  --el-menu-hover-bg-color: transparent;
  --el-menu-bg-color: transparent;
  --el-menu-active-color: #fff;

  // 分组标题(sub-menu title)
  :deep(.el-sub-menu__title) {
    height: 40px;
    line-height: 40px;
    font-size: 12px;
    font-weight: 600;
    letter-spacing: 0.5px;
    color: var(--el-text-color-placeholder) !important;
    padding-left: 20px !important;
    margin-top: 8px;
    cursor: default;
    &:hover { background: transparent !important; }
    .el-sub-menu__icon-arrow { display: none; }
    .el-icon { font-size: 14px; margin-right: 6px; color: var(--el-text-color-disabled); }
  }

  // 菜单项
  :deep(.el-menu-item) {
    height: 42px;
    line-height: 42px;
    margin: 2px 8px;
    padding: 0 14px !important;
    border-radius: 8px;
    font-size: 14px;
    color: var(--el-text-color-regular);
    transition: all .15s;
    .el-icon { font-size: 17px; margin-right: 10px; color: var(--el-text-color-secondary); }
    &:hover {
      background: var(--el-fill-color-light);
      color: var(--el-text-color-primary);
    }
    &.is-active {
      background: rgba(64,158,255,0.08);
      color: #409eff;
      font-weight: 500;
      .el-icon { color: #409eff; }
    }
  }

  // 子菜单容器内间距
  :deep(.el-sub-menu .el-menu) {
    padding: 0;
  }
}

/* ===================== Topbar ===================== */
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 56px;
  background: var(--el-bg-color);
  color: var(--el-text-color-primary);
  border-bottom: 1px solid var(--el-border-color-light);
  padding: 0 20px;
  .left { display: flex; align-items: center; gap: 12px; }
  .crumb { font-size: 16px; font-weight: 600; }
  .user-entry {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    cursor: pointer;
    color: var(--el-text-color-primary);
    .nick { font-size: 14px; }
  }
  .right {
    display: inline-flex;
    align-items: center;
    gap: 12px;
  }
  .theme-btn { padding: 0 6px; }
}

/* ===================== Main ===================== */
.main {
  background: var(--gp-bg);
  padding: 0;
}

/* ===================== Footer ===================== */
.footer {
  background: transparent;
  text-align: center;
  color: var(--el-text-color-placeholder);
  font-size: 12px;
  padding: 8px 12px;
  height: auto;
  min-height: 32px;
}
.footer-line { line-height: 1.6; }
.footer-custom { font-size: 11px; }

.fade-enter-active, .fade-leave-active { transition: opacity .15s; }
.fade-enter-from, .fade-leave-to { opacity: 0; }
</style>
