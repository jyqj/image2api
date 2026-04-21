<script setup lang="ts">
import { ref, computed, reactive, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import {
  Refresh,
  Check,
  Setting,
  Connection,
  Message as MailIcon,
} from '@element-plus/icons-vue'
import {
  listSettings,
  updateSettings,
  reloadSettings,
  sendTestEmail,
  type SettingItem,
} from '@/api/settings'
import { useSiteStore } from '@/stores/site'

const loading = ref(false)
const saving = ref(false)
const items = ref<SettingItem[]>([])
// 本地编辑态,key -> value(string)
const draft = reactive<Record<string, string>>({})

const tabs = [
  { name: 'site', label: '通用设置', icon: Setting },
  { name: 'gateway', label: '网关服务', icon: Connection },
  { name: 'account', label: '账号池', icon: Connection },
  { name: 'api', label: 'API 认证', icon: Connection },
  { name: 'mail', label: '邮件设置', icon: MailIcon },
] as const
const activeTab = ref<(typeof tabs)[number]['name']>('site')

const grouped = computed(() => {
  const map: Record<string, SettingItem[]> = {
    site: [], gateway: [], account: [], api: [], mail: [],
  }
  for (const it of items.value) {
    const cat = it.category
    ;(map[cat] ||= []).push(it)
  }
  for (const k of Object.keys(map)) map[k].sort((a, b) => a.key.localeCompare(b.key))
  return map
})

const dirtyCount = computed(() => {
  let n = 0
  for (const it of items.value) {
    if (String(draft[it.key] ?? '') !== String(it.value)) n++
  }
  return n
})

async function load() {
  loading.value = true
  try {
    const d = await listSettings()
    items.value = d.items
    for (const it of d.items) draft[it.key] = it.value
  } finally {
    loading.value = false
  }
}

function reset() {
  for (const it of items.value) draft[it.key] = it.value
  ElMessage.info('已重置为服务端当前值')
}

function isBool(it: SettingItem) { return it.type === 'bool' }
function isInt(it: SettingItem) { return it.type === 'int' }
function isFloat(it: SettingItem) { return it.type === 'float' }
function inputType(it: SettingItem) {
  if (it.type === 'email') return 'email'
  if (it.type === 'url') return 'url'
  return 'text'
}

async function save() {
  const diff: Record<string, string> = {}
  for (const it of items.value) {
    const v = draft[it.key] ?? ''
    if (String(v) !== String(it.value)) diff[it.key] = String(v)
  }
  if (Object.keys(diff).length === 0) {
    ElMessage.info('没有需要保存的修改')
    return
  }
  saving.value = true
  try {
    await updateSettings(diff)
    ElMessage.success(`已保存 ${Object.keys(diff).length} 项`)
    await load()
    useSiteStore().refresh()
  } finally {
    saving.value = false
  }
}

async function doReload() {
  await ElMessageBox.confirm('从数据库强制重载最新值到内存缓存?', '确认', {
    type: 'warning',
  }).catch(() => 'cancel')
  try {
    await reloadSettings()
    ElMessage.success('已重载')
    await load()
  } catch { /* 拦截器已处理 */ }
}

// ---- 邮件测试 ----
const mailDlg = ref(false)
const mailTo = ref('')
const mailSending = ref(false)
async function submitTestMail() {
  if (!mailTo.value) {
    ElMessage.warning('请输入收件邮箱')
    return
  }
  mailSending.value = true
  try {
    await sendTestEmail(mailTo.value)
    ElMessage.success('测试邮件已发出')
    mailDlg.value = false
  } catch { /* 拦截器已处理 */ } finally {
    mailSending.value = false
  }
}

function generateAPIKey() {
  const arr = new Uint8Array(32)
  crypto.getRandomValues(arr)
  const key = 'sk-' + Array.from(arr).map(b => b.toString(16).padStart(2, '0')).join('')
  draft['api.v1_key'] = key
  ElMessage.success('已生成随机 API Key，记得点保存')
}

onMounted(load)
</script>

<template>
  <div class="page-container">
    <div class="card-block" v-loading="loading">
      <!-- 顶部:标题 + 操作栏(始终可见) -->
      <div class="flex-between settings-head">
        <div>
          <div class="page-title" style="margin:0">系统设置</div>
          <div class="settings-subtitle">
            所有修改在点击"保存修改"后立即生效,无需重启服务
          </div>
        </div>
        <div class="flex-wrap-gap">
          <el-button :icon="Refresh" @click="doReload">强制重载</el-button>
          <el-button :icon="MailIcon" @click="mailDlg = true">发测试邮件</el-button>
          <el-button :disabled="dirtyCount === 0" @click="reset">重置</el-button>
          <el-button
            type="primary"
            :icon="Check"
            :loading="saving"
            @click="save"
          >
            保存修改<span v-if="dirtyCount > 0"> ({{ dirtyCount }})</span>
          </el-button>
        </div>
      </div>

      <el-tabs v-model="activeTab" class="settings-tabs">
        <el-tab-pane v-for="t in tabs" :key="t.name" :name="t.name">
          <template #label>
            <span class="tab-label">
              <el-icon><component :is="t.icon" /></el-icon>
              <span>{{ t.label }}</span>
            </span>
          </template>

          <div class="tab-body">
            <el-empty
              v-if="!grouped[t.name] || grouped[t.name].length === 0"
              description="暂无可配置项"
            />
            <el-form
              v-else
              label-width="170px"
              label-position="right"
              class="setting-form"
            >
              <el-form-item
                v-for="it in grouped[t.name]"
                :key="it.key"
                :label="it.label || it.key"
              >
                <div class="field-wrap">
                  <el-switch
                    v-if="isBool(it)"
                    :model-value="draft[it.key] === 'true'"
                    @update:model-value="(v) => (draft[it.key] = v ? 'true' : 'false')"
                  />
                  <el-input-number
                    v-else-if="isInt(it)"
                    :model-value="Number(draft[it.key] || 0)"
                    :min="0"
                    :controls-position="'right'"
                    style="width: 240px"
                    @update:model-value="(v) => (draft[it.key] = String(v ?? 0))"
                  />
                  <el-input-number
                    v-else-if="isFloat(it)"
                    :model-value="Number(draft[it.key] || 0)"
                    :min="0"
                    :max="1"
                    :step="0.05"
                    :precision="2"
                    :controls-position="'right'"
                    style="width: 240px"
                    @update:model-value="(v) => (draft[it.key] = String(v ?? 0))"
                  />
                  <div v-else class="input-with-action">
                    <el-input
                      v-model="draft[it.key]"
                      :placeholder="it.desc || it.label"
                      :type="inputType(it)"
                      clearable
                      style="max-width: 520px"
                    />
                    <el-button
                      v-if="it.key === 'api.v1_key'"
                      size="small"
                      @click="generateAPIKey"
                    >随机生成</el-button>
                  </div>
                  <div v-if="it.desc" class="hint">{{ it.desc }}</div>
                </div>
              </el-form-item>
            </el-form>
          </div>
        </el-tab-pane>
      </el-tabs>
    </div>

    <!-- 测试邮件 -->
    <el-dialog v-model="mailDlg" title="发送 SMTP 测试邮件" width="420px">
      <el-form label-width="80px">
        <el-form-item label="收件人">
          <el-input v-model="mailTo" placeholder="your@mail.com" type="email" clearable />
        </el-form-item>
        <div style="font-size:12px;color:var(--el-text-color-secondary)">
          使用 <code>configs/config.yaml</code> 的 SMTP 配置发送;未配置时会直接失败。
        </div>
      </el-form>
      <template #footer>
        <el-button @click="mailDlg = false">取消</el-button>
        <el-button type="primary" :loading="mailSending" @click="submitTestMail">发送</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
.settings-head {
  margin-bottom: 4px;
}
.settings-subtitle {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 4px;
}

.settings-tabs {
  margin-top: 8px;
}
.settings-tabs :deep(.el-tabs__header) {
  margin-bottom: 16px;
}
.tab-label {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.tab-body {
  padding-top: 4px;
}
.setting-form .el-form-item {
  margin-bottom: 18px;
}
.field-wrap {
  width: 100%;
}
.input-with-action {
  display: flex;
  align-items: center;
  gap: 8px;
}
.hint {
  margin-top: 4px;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.5;
}

@media (max-width: 640px) {
  .setting-form :deep(.el-form-item__label) {
    width: auto !important;
    padding-right: 8px !important;
    line-height: 1.5;
  }
}
</style>
