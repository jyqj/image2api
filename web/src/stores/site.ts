import { defineStore } from 'pinia'
import { ref } from 'vue'
import { fetchSiteInfo } from '@/api/settings'

/**
 * Site store 缓存站点公开信息:
 *   site.name / site.description / site.logo_url / site.footer / site.contact_email
 *
 * 页面启动时 refresh() 一次即可;本地设置改完会再触发一次 refresh。
 */
export const useSiteStore = defineStore('site', () => {
  const info = ref<Record<string, string>>({
    'site.name': 'GPT2API',
    'site.description': '自用 OpenAI 兼容中转',
    'site.logo_url': '',
    'site.footer': '',
    'site.contact_email': '',
  })
  const loaded = ref(false)

  async function refresh() {
    try {
      const d = await fetchSiteInfo()
      info.value = { ...info.value, ...d }
    } catch {
      // 静默失败,保持默认值。后端未起或权限中间件变化时,前端仍可渲染。
    } finally {
      loaded.value = true
      applyDocumentTitle()
      applyFavicon()
    }
  }

  function applyDocumentTitle() {
    const n = info.value['site.name'] || 'GPT2API'
    document.title = `${n} 控制台`
  }

  function applyFavicon() {
    const url = info.value['site.logo_url']
    if (!url) return
    let link = document.querySelector<HTMLLinkElement>('link[rel~="icon"]')
    if (!link) {
      link = document.createElement('link')
      link.rel = 'icon'
      document.head.appendChild(link)
    }
    link.href = url
  }

  function get(key: string, fallback = ''): string {
    const v = info.value[key]
    return v == null || v === '' ? fallback : v
  }
  return { info, loaded, refresh, get }
})
