import { defineStore } from 'pinia'
import { useDark, useToggle } from '@vueuse/core'

/**
 * UI 偏好:黑暗模式切换。
 * Element Plus 的 dark 模式通过在 <html> 上加 `class="dark"` 生效,
 * 所以这里配置 useDark 去改根元素 class。
 *
 * 持久化:由 @vueuse 的 useStorage 接管(默认 key=vueuse-color-scheme)。
 */
export const useUIStore = defineStore('ui', () => {
  const isDark = useDark({
    selector: 'html',
    attribute: 'class',
    valueDark: 'dark',
    valueLight: '',
    storageKey: 'gpt2api.theme',
  })
  const toggleDark = useToggle(isDark)
  return { isDark, toggleDark }
})
