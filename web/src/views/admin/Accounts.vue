<script setup lang="ts">
import { ref, reactive, onMounted, computed } from 'vue'
import { ElMessage, ElMessageBox, ElNotification } from 'element-plus'
import { Upload } from '@element-plus/icons-vue'
import * as accountApi from '@/api/accounts'
import * as proxyApi from '@/api/proxies'
import { formatDateShort } from '@/utils/format'

// ========== 列表 & 筛选 ==========
const loading = ref(false)
const filter = reactive<{ status?: string; keyword?: string }>({ status: '', keyword: '' })
const rows = ref<accountApi.Account[]>([])
const total = ref(0)
const pager = reactive({ page: 1, page_size: 10 })
const proxies = ref<proxyApi.Proxy[]>([])

async function fetchList() {
  loading.value = true
  try {
    const data = await accountApi.listAccounts({
      page: pager.page,
      page_size: pager.page_size,
      status: filter.status || undefined,
      keyword: filter.keyword || undefined,
    })
    rows.value = data.list || []
    total.value = data.total || 0
  } catch (e: any) {
    ElMessage.error(e?.message || '加载失败')
  } finally {
    loading.value = false
  }
}

async function fetchProxies() {
  try {
    const d = await proxyApi.listProxies({ page: 1, page_size: 500 })
    proxies.value = (d.list || []).filter((p) => p.enabled)
  } catch {
    /* noop */
  }
}

function onSearch() {
  pager.page = 1
  fetchList()
}
function onReset() {
  filter.status = ''
  filter.keyword = ''
  pager.page = 1
  fetchList()
}

// ========== 自动刷新开关 ==========
const autoRefreshEnabled = ref(false)
const autoRefreshSaving = ref(false)

async function loadAutoRefresh() {
  try {
    const cfg = await accountApi.getAutoRefresh()
    autoRefreshEnabled.value = !!cfg.enabled
  } catch {
    /* noop */
  }
}
async function onToggleAutoRefresh(val: boolean | string | number) {
  const enabled = !!val
  autoRefreshSaving.value = true
  try {
    await accountApi.setAutoRefresh(enabled)
    autoRefreshEnabled.value = enabled
    ElMessage.success(
      enabled
        ? '已开启自动刷新:AT 距离过期 < 1 天时自动续期,失效/可疑账号不刷新'
        : '已关闭自动刷新',
    )
  } catch (e: any) {
    // 回滚 UI
    autoRefreshEnabled.value = !enabled
    ElMessage.error(e?.message || '保存失败')
  } finally {
    autoRefreshSaving.value = false
  }
}

// ========== 批量删除 ==========
const BULK_DELETE_LABELS: Record<string, string> = {
  dead:       '失效账号',
  suspicious: '可疑 / 已封账号',
  warned:     '风险账号',
  throttled:  '限流账号',
  all:        '全部账号',
}
async function onBulkDelete(scope: accountApi.BulkDeleteScope) {
  const label = BULK_DELETE_LABELS[scope] || scope
  try {
    await ElMessageBox.confirm(
      `确认将「${label}」全部删除?此操作会软删所有匹配条目,不可在当前界面恢复。`,
      scope === 'all' ? '⚠ 删除全部账号' : '批量删除',
      {
        confirmButtonText: '删除',
        cancelButtonText: '取消',
        type: scope === 'all' ? 'error' : 'warning',
      },
    )
  } catch { return }
  try {
    const r = await accountApi.bulkDeleteAccounts(scope)
    ElMessage.success(`已删除 ${r.deleted} 个账号`)
    pager.page = 1
    fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '删除失败')
  }
}

// ========== 日期工具(兼容 sql.NullTime 返回形态) ==========
function asDate(v: any): string {
  if (!v) return ''
  if (typeof v === 'string') return v
  if (typeof v === 'object') {
    if ('Valid' in v && !v.Valid) return ''
    if ('Time' in v) return v.Time
  }
  return ''
}
function fmtTime(v: any) {
  const s = asDate(v)
  return s ? formatDateShort(s) : '-'
}

// ========== 状态/类型展示 ==========
type TagType = 'success' | 'warning' | 'info' | 'danger' | 'primary'
const statusMap: Record<string, { label: string; type: TagType }> = {
  healthy:    { label: '健康',   type: 'success' },
  warned:     { label: '风险',   type: 'warning' },
  throttled:  { label: '限流',   type: 'warning' },
  suspicious: { label: '可疑',   type: 'info'    },
  dead:       { label: '失效',   type: 'danger'  },
}
function statusText(s: string): string { return statusMap[s]?.label || s || '-' }
function statusType(s: string): TagType { return statusMap[s]?.type || 'info' }

function typeLabel(t: string) {
  const map: Record<string, string> = { codex: 'Codex', chatgpt: 'ChatGPT', openai: 'OpenAI' }
  return map[t] || t || '-'
}

// ========== 即将过期高亮 ==========
function expiresClass(v: any): string {
  const s = asDate(v)
  if (!s) return 'muted'
  const t = new Date(s).getTime()
  if (Number.isNaN(t)) return 'muted'
  const diffMin = (t - Date.now()) / 60000
  if (diffMin < 0) return 'err'
  if (diffMin < 30) return 'warn'
  return ''
}

// ========== 新建 / 编辑 ==========
const dlg = ref(false)
const isEdit = ref(false)
const submitting = ref(false)
const formDefault = {
  id: 0,
  email: '',
  auth_token: '',
  refresh_token: '',
  session_token: '',
  token_expires_at: '',
  oai_session_id: '',
  oai_device_id: '',
  client_id: 'app_LlGpXReQgckcGGUo2JrYvtJK',
  chatgpt_account_id: '',
  account_type: 'codex',
  subscription_type: 'plus',
  notes: '',
  cookies: '',
  proxy_id: 0,
  status: 'healthy',
}
const form = reactive({ ...formDefault })

function openCreate() {
  isEdit.value = false
  Object.assign(form, { ...formDefault })
  dlg.value = true
}

const secretsLoading = ref(false)

async function openEdit(row: accountApi.Account) {
  isEdit.value = true
  Object.assign(form, {
    id: row.id,
    email: row.email,
    auth_token: '',
    refresh_token: '',
    session_token: '',
    token_expires_at: asDate(row.token_expires_at),
    oai_session_id: row.oai_session_id || '',
    oai_device_id: row.oai_device_id || '',
    client_id: row.client_id || formDefault.client_id,
    chatgpt_account_id: row.chatgpt_account_id || '',
    account_type: row.account_type || 'codex',
    subscription_type: row.subscription_type || 'plus',
    notes: row.notes || '',
    cookies: '',
    proxy_id: 0,
    status: row.status || 'healthy',
  })
  dlg.value = true
  // 异步拉取 AT / RT / ST 明文并回填,方便查看/修改
  secretsLoading.value = true
  try {
    const s = await accountApi.getAccountSecrets(row.id)
    form.auth_token    = s.auth_token    || ''
    form.refresh_token = s.refresh_token || ''
    form.session_token = s.session_token || ''
  } catch (e: any) {
    ElMessage.warning('未能加载 AT/RT/ST 明文,留空即不修改')
  } finally {
    secretsLoading.value = false
  }
}

async function copyText(text: string, label: string) {
  if (!text) { ElMessage.info('内容为空'); return }
  try {
    await navigator.clipboard.writeText(text)
    ElMessage.success(`${label} 已复制`)
  } catch {
    ElMessage.error('复制失败,请手动选中复制')
  }
}

async function submitForm() {
  if (!form.email) { ElMessage.warning('请输入邮箱'); return }
  submitting.value = true
  try {
    if (!isEdit.value) {
      if (!form.auth_token) { ElMessage.warning('新建账号必须提供 access_token'); submitting.value = false; return }
      await accountApi.createAccount({ ...form })
      ElMessage.success('创建成功')
    } else {
      const body: any = {
        email: form.email,
        subscription_type: form.subscription_type,
        client_id: form.client_id,
        chatgpt_account_id: form.chatgpt_account_id,
        account_type: form.account_type,
        notes: form.notes,
        status: form.status,
      }
      if (form.auth_token)    body.auth_token    = form.auth_token
      if (form.refresh_token) body.refresh_token = form.refresh_token
      if (form.session_token) body.session_token = form.session_token
      if (form.cookies)       body.cookies       = form.cookies
      if (form.token_expires_at) body.token_expires_at = form.token_expires_at
      await accountApi.updateAccount(form.id, body)
      ElMessage.success('更新成功')
    }
    dlg.value = false
    await fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '提交失败')
  } finally {
    submitting.value = false
  }
}

async function onDelete(row: accountApi.Account) {
  try {
    await ElMessageBox.confirm(`确定删除账号「${row.email}」?该操作不可恢复。`, '删除确认', {
      confirmButtonText: '删除', cancelButtonText: '取消', type: 'warning',
    })
  } catch { return }
  try {
    await accountApi.deleteAccount(row.id)
    ElMessage.success('已删除')
    fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '删除失败')
  }
}

// ========== 绑定代理 ==========
const bindDlg = ref(false)
const bindForm = reactive({ id: 0, email: '', proxy_id: 0 })
function openBind(row: accountApi.Account) {
  bindForm.id = row.id
  bindForm.email = row.email
  bindForm.proxy_id = 0
  bindDlg.value = true
}
async function submitBind() {
  try {
    if (bindForm.proxy_id > 0) {
      await accountApi.bindProxy(bindForm.id, bindForm.proxy_id)
      ElMessage.success('已绑定代理')
    } else {
      await accountApi.unbindProxy(bindForm.id)
      ElMessage.success('已解绑')
    }
    bindDlg.value = false
    fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '操作失败')
  }
}

// ========== 刷新 / 探测(单条) ==========
const refreshingIds = ref<Set<number>>(new Set())
const probingIds = ref<Set<number>>(new Set())

async function onRefreshOne(row: accountApi.Account) {
  refreshingIds.value.add(row.id)
  try {
    const r = await accountApi.refreshAccount(row.id)
    if (r.ok) {
      if (r.at_verified === false) {
        // RT 刷成功但新 AT 没通过 chatgpt.com web 校验,提示运维侧
        ElMessage.warning(
          `刷新成功(来源:${r.source.toUpperCase()}),但新 AT 未通过 chatgpt.com 校验,可能无法用于聊天/图像接口`
        )
      } else {
        ElMessage.success(`刷新成功(来源:${r.source.toUpperCase()})`)
      }
    } else if (r.web_unauthorized) {
      ElMessage.error(
        r.error || 'RT 换出的 AT 被 chatgpt.com 拒绝,请为该账号补充 Session Token'
      )
    } else {
      ElMessage.error(r.error || '刷新失败')
    }
    fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '刷新失败')
  } finally {
    refreshingIds.value.delete(row.id)
  }
}
async function onProbeOne(row: accountApi.Account) {
  probingIds.value.add(row.id)
  try {
    const r = await accountApi.probeAccountQuota(row.id)
    if (r.ok) {
      const parts: string[] = [`生图剩余 ${r.remaining}`]
      if (r.default_model) parts.push(`模型 ${r.default_model}`)
      if (r.blocked_features && r.blocked_features.length) {
        parts.push(`受限:${r.blocked_features.join(',')}`)
      }
      ElMessage.success(parts.join(' · '))
    } else {
      ElMessage.error(r.error || '探测失败')
    }
    fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '探测失败')
  } finally {
    probingIds.value.delete(row.id)
  }
}

// ========== 全部刷新 / 全部探测 ==========
const batchRunning = ref<'none' | 'refresh' | 'probe'>('none')

async function onRefreshAll() {
  if (total.value === 0) { ElMessage.info('暂无账号'); return }
  try {
    await ElMessageBox.confirm(`将并发刷新全部账号(共 ${total.value} 个),可能耗时较久,是否继续?`, '批量刷新', {
      confirmButtonText: '开始', cancelButtonText: '取消',
    })
  } catch { return }
  batchRunning.value = 'refresh'
  try {
    const r = await accountApi.refreshAllAccounts()
    ElNotification.success({
      title: '批量刷新完成',
      message: `成功 ${r.success} · 失败 ${r.failed} · 合计 ${r.total}`,
      duration: 4000,
    })
    fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '刷新失败')
  } finally {
    batchRunning.value = 'none'
  }
}

async function onProbeAll() {
  if (total.value === 0) { ElMessage.info('暂无账号'); return }
  try {
    await ElMessageBox.confirm(`将并发探测全部账号的图片额度(共 ${total.value} 个)?`, '批量探测', {
      confirmButtonText: '开始', cancelButtonText: '取消',
    })
  } catch { return }
  batchRunning.value = 'probe'
  try {
    const r = await accountApi.probeAllAccountsQuota()
    ElNotification.success({
      title: '批量探测完成',
      message: `成功 ${r.success} · 失败 ${r.failed} · 合计 ${r.total}`,
      duration: 4000,
    })
    fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '探测失败')
  } finally {
    batchRunning.value = 'none'
  }
}

// ========== 批量导入(多文件 + 分批) ==========
// 4 种模式:
//   - json: 文件/JSON 文本,原有行为
//   - at:   一行一个 access_token
//   - rt:   一行一个 refresh_token,必须提供 APPID(client_id)
//   - st:   一行一个 session_token
type ImportMode = 'json' | 'at' | 'rt' | 'st' | 'session_json'
const importDlg = ref(false)
const importMode = ref<ImportMode>('session_json')
const importForm = reactive({
  files: [] as File[],
  text: '',
  tokens_text: '',
  update_existing: true,
  default_client_id: 'app_LlGpXReQgckcGGUo2JrYvtJK',
  default_proxy_id: 0,
})
const importing = ref(false)
const importProgress = reactive({
  running: false,
  current: 0,
  totalBatches: 0,
  created: 0,
  updated: 0,
  skipped: 0,
  failed: 0,
})
const importResult = ref<accountApi.ImportSummary | null>(null)
const importLastErrors = ref<accountApi.ImportLineResult[]>([])

function openImport() {
  importMode.value = 'json'
  importForm.files = []
  importForm.text = ''
  importForm.tokens_text = ''
  importForm.update_existing = true
  importForm.default_client_id = 'app_LlGpXReQgckcGGUo2JrYvtJK'
  importForm.default_proxy_id = 0
  importResult.value = null
  importLastErrors.value = []
  importProgress.running = false
  importProgress.current = 0
  importProgress.totalBatches = 0
  importProgress.created = 0
  importProgress.updated = 0
  importProgress.skipped = 0
  importProgress.failed = 0
  importDlg.value = true
}

// 当前 tokens 模式下,每行 token 的数量预览
const tokenLineCount = computed(() => {
  if (importMode.value === 'json') return 0
  return importForm.tokens_text
    .split(/\r?\n/)
    .map((s) => s.trim())
    .filter(Boolean).length
})

function onPickFiles(e: Event) {
  const input = e.target as HTMLInputElement
  if (!input.files) return
  const arr = Array.from(input.files)
  importForm.files.push(...arr)
  input.value = ''
}
function onDropFiles(e: DragEvent) {
  e.preventDefault()
  if (!e.dataTransfer) return
  const arr = Array.from(e.dataTransfer.files).filter((f) => f.name.endsWith('.json') || f.name.endsWith('.txt'))
  importForm.files.push(...arr)
}
function removeFile(i: number) {
  importForm.files.splice(i, 1)
}
function clearFiles() {
  importForm.files = []
}

const totalFileSize = computed(() => importForm.files.reduce((s, f) => s + f.size, 0))
function humanSize(n: number) {
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(2) + ' MB'
}

/**
 * 前端分批:
 *   - 一批最多 BATCH_FILES 个文件或 BATCH_BYTES 字节,两者取先到者
 *   - 每批通过 multipart 上传,后端会解析并 upsert
 *   - 每批完成后更新进度,并 yield 事件循环
 * 选 10000 个文件时会自动切分 ~50 批,每批 ~200 个
 */
const BATCH_FILES = 200
const BATCH_BYTES = 8 * 1024 * 1024 // 8MB

async function doImport() {
  importLastErrors.value = []
  importResult.value = null

  // 情况 0:AT/RT/ST 纯 token 模式
  if (importMode.value !== 'json') {
    const mode = importMode.value
    const tokens = importForm.tokens_text
      .split(/\r?\n/)
      .map((s) => s.trim())
      .filter(Boolean)
    if (tokens.length === 0) {
      ElMessage.warning('请粘贴 token,每行一个')
      return
    }
    if (mode === 'rt' && !importForm.default_client_id.trim()) {
      ElMessage.warning('RT 模式必须填写 APPID(client_id)')
      return
    }
    if ((mode === 'rt' || mode === 'st') && !importForm.default_proxy_id) {
      try {
        await ElMessageBox.confirm(
          `${mode.toUpperCase()} 模式需要访问 chatgpt.com / auth.openai.com 换取 AT。未选择代理时会直连,国内网络大概率失败。确认继续吗?`,
          '建议选一个代理',
          { confirmButtonText: '继续直连', cancelButtonText: '取消', type: 'warning' },
        )
      } catch {
        return
      }
    }
    importing.value = true
    importProgress.running = true
    importProgress.current = 0
    importProgress.totalBatches = 1
    try {
      const r = await accountApi.importAccountsTokens({
        mode,
        tokens,
        client_id: importForm.default_client_id.trim() || undefined,
        update_existing: importForm.update_existing,
        default_proxy_id: importForm.default_proxy_id || undefined,
      })
      mergeSummary(r)
      importResult.value = cloneAgg()
      importLastErrors.value = r.results
        .filter((x) => x.status === 'failed' || x.status === 'skipped')
        .slice(0, 200)
      const tip = `${mode.toUpperCase()} 导入完成:+${r.created} / ~${r.updated} / 跳过${r.skipped} / 失败${r.failed}`
      if (r.failed > 0) ElNotification.warning({ title: '批量导入完成(部分失败)', message: tip })
      else ElMessage.success(tip)
    } catch (e: any) {
      ElMessage.error(e?.message || '导入失败')
    } finally {
      importing.value = false
      importProgress.running = false
      fetchList()
    }
    return
  }

  // 情况一:纯文本导入(JSON 模式)
  if (importForm.files.length === 0) {
    if (!importForm.text.trim()) { ElMessage.warning('请选择 JSON 文件或粘贴 JSON 文本'); return }
    importing.value = true
    importProgress.running = true
    importProgress.current = 0
    importProgress.totalBatches = 1
    try {
      const r = await accountApi.importAccountsJSON({
        text: importForm.text,
        update_existing: importForm.update_existing,
        default_client_id: importForm.default_client_id || undefined,
        default_proxy_id: importForm.default_proxy_id || undefined,
      })
      mergeSummary(r)
      importResult.value = cloneAgg()
      importLastErrors.value = r.results.filter((x) => x.status === 'failed' || x.status === 'skipped').slice(0, 200)
      ElMessage.success(`导入完成:+${r.created} / ~${r.updated} / 跳过${r.skipped} / 失败${r.failed}`)
    } catch (e: any) {
      ElMessage.error(e?.message || '导入失败')
    } finally {
      importing.value = false
      importProgress.running = false
      fetchList()
    }
    return
  }

  // 情况二:多文件分批
  const batches: File[][] = []
  let curBatch: File[] = []
  let curBytes = 0
  for (const f of importForm.files) {
    if ((curBatch.length >= BATCH_FILES) || (curBytes + f.size > BATCH_BYTES && curBatch.length > 0)) {
      batches.push(curBatch)
      curBatch = []
      curBytes = 0
    }
    curBatch.push(f)
    curBytes += f.size
  }
  if (curBatch.length) batches.push(curBatch)

  importing.value = true
  importProgress.running = true
  importProgress.current = 0
  importProgress.totalBatches = batches.length
  importProgress.created = 0
  importProgress.updated = 0
  importProgress.skipped = 0
  importProgress.failed = 0
  const errList: accountApi.ImportLineResult[] = []

  try {
    for (let i = 0; i < batches.length; i++) {
      const b = batches[i]
      try {
        const r = await accountApi.importAccountsFiles(b, {
          update_existing: importForm.update_existing,
          default_client_id: importForm.default_client_id || undefined,
          default_proxy_id: importForm.default_proxy_id || undefined,
        })
        mergeSummary(r)
        for (const it of r.results) {
          if ((it.status === 'failed' || it.status === 'skipped') && errList.length < 500) {
            errList.push(it)
          }
        }
      } catch (e: any) {
        importProgress.failed += b.length
        errList.push({ index: i, email: `[批次#${i + 1}]`, status: 'failed', reason: e?.message || '上传失败' })
      }
      importProgress.current = i + 1
      // 让出事件循环,避免阻塞 UI
      await new Promise((r) => setTimeout(r, 0))
    }
    importResult.value = cloneAgg()
    importLastErrors.value = errList
    ElNotification.success({
      title: '批量导入完成',
      message: `+${importProgress.created}  ~${importProgress.updated}  跳过 ${importProgress.skipped}  失败 ${importProgress.failed}`,
      duration: 5000,
    })
  } finally {
    importing.value = false
    importProgress.running = false
    fetchList()
  }
}

function mergeSummary(r: accountApi.ImportSummary) {
  importProgress.created += r.created
  importProgress.updated += r.updated
  importProgress.skipped += r.skipped
  importProgress.failed  += r.failed
}
function cloneAgg(): accountApi.ImportSummary {
  return {
    total:   importProgress.created + importProgress.updated + importProgress.skipped + importProgress.failed,
    created: importProgress.created,
    updated: importProgress.updated,
    skipped: importProgress.skipped,
    failed:  importProgress.failed,
    results: [],
  }
}

onMounted(() => {
  fetchList()
  fetchProxies()
  loadAutoRefresh()
})
</script>

<template>
  <div class="page-container">
    <!-- 顶栏:标题 + 动作 -->
    <div class="card-block hdr">
      <div class="flex-between">
        <div class="hdr-left">
          <h2 class="page-title">GPT 账号池</h2>
          <div class="page-sub">
            统一管理 ChatGPT Plus / Team / Codex 账号:JSON / AT / RT / ST 批量导入 · 自动刷新 · 图片额度探测 · 风控熔断轮转
          </div>
        </div>
        <div class="actions">
          <el-button :loading="batchRunning === 'probe'" :disabled="loading" @click="onProbeAll">
            全部探测
          </el-button>
          <el-button :loading="batchRunning === 'refresh'" :disabled="loading" @click="onRefreshAll">
            全部刷新
          </el-button>
          <el-dropdown trigger="click" @command="onBulkDelete">
            <el-button>批量删除</el-button>
            <template #dropdown>
              <el-dropdown-menu>
                <el-dropdown-item command="dead">删除失效账号</el-dropdown-item>
                <el-dropdown-item command="suspicious">删除可疑/已封账号</el-dropdown-item>
                <el-dropdown-item command="warned">删除风险账号</el-dropdown-item>
                <el-dropdown-item command="throttled">删除限流账号</el-dropdown-item>
                <el-dropdown-item divided command="all">
                  <span style="color: var(--el-color-danger)">删除全部账号</span>
                </el-dropdown-item>
              </el-dropdown-menu>
            </template>
          </el-dropdown>
          <el-button @click="openImport">批量导入</el-button>
          <el-button type="primary" @click="openCreate">新建账号</el-button>
        </div>
      </div>
    </div>

    <!-- 筛选栏 -->
    <div class="card-block">
      <el-form :inline="true" size="default" class="filter-form" @submit.prevent="onSearch">
        <el-form-item label="状态">
          <el-select v-model="filter.status" placeholder="全部" clearable style="width: 140px">
            <el-option label="全部" value="" />
            <el-option label="健康" value="healthy" />
            <el-option label="风险" value="warned" />
            <el-option label="限流" value="throttled" />
            <el-option label="可疑" value="suspicious" />
            <el-option label="失效" value="dead" />
          </el-select>
        </el-form-item>
        <el-form-item label="关键词">
          <el-input
            v-model="filter.keyword"
            placeholder="邮箱 / 备注"
            clearable
            style="width: 260px"
            @keyup.enter="onSearch"
          />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" @click="onSearch">搜索</el-button>
          <el-button @click="onReset">重置</el-button>
        </el-form-item>
        <el-form-item class="auto-refresh-item">
          <el-tooltip
            placement="top"
            content="开启后:AT 距离过期 < 1 天的账号会被后台自动续期;状态为「失效 / 可疑」的账号不会刷新"
          >
            <el-checkbox
              v-model="autoRefreshEnabled"
              :disabled="autoRefreshSaving"
              @change="onToggleAutoRefresh"
            >
              自动刷新 AT
              <span class="auto-refresh-hint">(&lt; 1 天过期时)</span>
            </el-checkbox>
          </el-tooltip>
        </el-form-item>
      </el-form>
    </div>

    <!-- 表格 -->
    <div class="card-block">
      <el-table
        v-loading="loading" :data="rows" stripe size="default" row-key="id"
        table-layout="auto" style="width: 100%"
      >
        <el-table-column label="邮箱" min-width="200" show-overflow-tooltip>
          <template #default="{ row }">
            <el-tooltip
              v-if="row.notes"
              placement="top"
              :content="row.notes"
            >
              <span class="email">{{ row.email }}</span>
            </el-tooltip>
            <span v-else class="email">{{ row.email }}</span>
          </template>
        </el-table-column>
        <el-table-column label="类型" width="76">
          <template #default="{ row }">
            <el-tag size="small" effect="plain">{{ typeLabel(row.account_type) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="76">
          <template #default="{ row }">
            <el-tag :type="statusType(row.status)" size="small">{{ statusText(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="凭证" width="86">
          <template #default="{ row }">
            <div class="creds">
              <el-tooltip content="存在 Refresh Token,可用 RT 自动刷新 AT" placement="top">
                <el-tag :type="row.has_rt ? 'success' : 'info'" size="small" effect="plain">
                  {{ row.has_rt ? 'RT' : '—' }}
                </el-tag>
              </el-tooltip>
              <el-tooltip content="存在 Session Token,可用 ST 回退刷新" placement="top">
                <el-tag :type="row.has_st ? 'success' : 'info'" size="small" effect="plain">
                  {{ row.has_st ? 'ST' : '—' }}
                </el-tag>
              </el-tooltip>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="AT 过期" min-width="148" show-overflow-tooltip>
          <template #default="{ row }">
            <span :class="expiresClass(row.token_expires_at)">{{ fmtTime(row.token_expires_at) }}</span>
          </template>
        </el-table-column>
        <el-table-column label="生图剩余" width="96" align="center">
          <template #default="{ row }">
            <template v-if="row.image_quota_remaining >= 0">
              <el-tooltip
                placement="top"
                :disabled="!asDate(row.image_quota_reset_at)"
                :content="'下次重置:' + fmtTime(row.image_quota_reset_at)"
              >
                <span class="quota"><b>{{ row.image_quota_remaining }}</b></span>
              </el-tooltip>
            </template>
            <span v-else class="muted">未探测</span>
          </template>
        </el-table-column>
        <el-table-column label="今日已用 / 上限" width="140" align="center">
          <template #default="{ row }">
            <el-tooltip placement="top">
              <template #content>
                <div style="line-height:1.8">
                  <div>今日已用 {{ row.today_used_count }} 张</div>
                </div>
              </template>
              <b>{{ row.today_used_count }}</b>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column label="最近刷新" min-width="148" show-overflow-tooltip>
          <template #default="{ row }">
            <div class="refresh-cell">
              <span>{{ fmtTime(row.last_refresh_at) }}</span>
              <el-tag
                v-if="row.last_refresh_source"
                size="small" effect="plain"
                :type="row.last_refresh_source === 'rt' ? 'success' : 'warning'"
              >{{ row.last_refresh_source.toUpperCase() }}</el-tag>
            </div>
            <el-tooltip
              v-if="row.refresh_error"
              placement="top"
              :content="row.refresh_error"
            >
              <div class="err">{{ row.refresh_error }}</div>
            </el-tooltip>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <el-button
              link type="primary" size="small"
              :loading="refreshingIds.has(row.id)"
              @click="onRefreshOne(row)"
            >刷新</el-button>
            <el-button
              link type="primary" size="small"
              :loading="probingIds.has(row.id)"
              @click="onProbeOne(row)"
            >探测</el-button>
            <el-button link type="primary" size="small" @click="openBind(row)">代理</el-button>
            <el-button link type="primary" size="small" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger"  size="small" @click="onDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pager">
        <el-pagination
          v-model:current-page="pager.page"
          v-model:page-size="pager.page_size"
          :total="total"
          :page-sizes="[10, 20, 50, 100, 200, 500, 1000]"
          layout="total, sizes, prev, pager, next, jumper"
          @current-change="fetchList"
          @size-change="fetchList"
        />
      </div>
    </div>

    <!-- 新建 / 编辑弹窗 -->
    <el-dialog v-model="dlg" :title="isEdit ? '编辑账号' : '新建账号'" width="720px" destroy-on-close>
      <el-form label-width="120px" size="default">
        <el-form-item label="邮箱">
          <el-input v-model="form.email" placeholder="user@example.com" />
        </el-form-item>
        <el-form-item label="账号类型">
          <el-select v-model="form.account_type" style="width: 180px">
            <el-option label="Codex" value="codex" />
            <el-option label="ChatGPT" value="chatgpt" />
          </el-select>
        </el-form-item>
        <el-form-item label="Access Token">
          <div class="token-field">
            <el-input
              v-model="form.auth_token"
              type="textarea" :rows="3"
              :placeholder="isEdit
                ? (secretsLoading ? '正在加载当前 AT……' : '当前为空,粘贴新的 access_token 可更新')
                : '粘贴 access_token(必填)'"
              spellcheck="false"
            />
            <el-button
              v-if="isEdit"
              size="small" link
              :disabled="!form.auth_token"
              @click="copyText(form.auth_token, 'Access Token')"
            >复制</el-button>
          </div>
        </el-form-item>
        <el-form-item label="Refresh Token">
          <div class="token-field">
            <el-input
              v-model="form.refresh_token"
              type="textarea" :rows="2"
              :placeholder="isEdit
                ? (secretsLoading ? '正在加载当前 RT……' : '该账号暂无 Refresh Token')
                : '可选;有 RT 则支持自动刷新'"
              spellcheck="false"
            />
            <el-button
              v-if="isEdit"
              size="small" link
              :disabled="!form.refresh_token"
              @click="copyText(form.refresh_token, 'Refresh Token')"
            >复制</el-button>
          </div>
        </el-form-item>
        <el-form-item label="Session Token">
          <div class="token-field">
            <el-input
              v-model="form.session_token"
              type="textarea" :rows="2"
              :placeholder="isEdit
                ? (secretsLoading ? '正在加载当前 ST……' : '该账号暂无 Session Token')
                : '可选;__Secure-next-auth.session-token 的值'"
              spellcheck="false"
            />
            <el-button
              v-if="isEdit"
              size="small" link
              :disabled="!form.session_token"
              @click="copyText(form.session_token, 'Session Token')"
            >复制</el-button>
          </div>
        </el-form-item>
        <el-form-item label="Token 过期时间">
          <el-date-picker
            v-model="form.token_expires_at"
            type="datetime" format="YYYY-MM-DD HH:mm:ss" value-format="YYYY-MM-DDTHH:mm:ssZ"
            placeholder="留空则从 JWT 自动解析"
            style="width: 260px"
          />
        </el-form-item>
        <el-form-item label="Client ID">
          <el-input v-model="form.client_id" />
        </el-form-item>
        <el-form-item label="ChatGPT AccountID">
          <el-input v-model="form.chatgpt_account_id" placeholder="可选;JSON 里有则自动填充" />
        </el-form-item>
        <el-form-item label="账号订阅">
          <el-select v-model="form.subscription_type" style="width: 180px">
            <el-option label="Plus"  value="plus" />
            <el-option label="Team"  value="team" />
            <el-option label="Free"  value="free" />
            <el-option label="Codex" value="codex" />
          </el-select>
        </el-form-item>
        <el-form-item v-if="isEdit" label="状态">
          <el-select v-model="form.status" style="width: 180px">
            <el-option label="健康"  value="healthy" />
            <el-option label="风险"  value="warned" />
            <el-option label="限流"  value="throttled" />
            <el-option label="可疑"  value="suspicious" />
            <el-option label="失效"  value="dead" />
          </el-select>
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="form.notes" type="textarea" :rows="2" />
        </el-form-item>
        <el-form-item v-if="!isEdit" label="绑定代理">
          <el-select v-model="form.proxy_id" clearable placeholder="不绑定" style="width: 100%">
            <el-option :value="0" label="不绑定" />
            <el-option
              v-for="p in proxies"
              :key="p.id"
              :label="`#${p.id} ${p.remark || p.host}:${p.port}`"
              :value="p.id"
            />
          </el-select>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" :loading="submitting" @click="submitForm">确定</el-button>
      </template>
    </el-dialog>

    <!-- 绑定代理弹窗 -->
    <el-dialog v-model="bindDlg" title="绑定代理" width="420px">
      <div style="margin-bottom: 10px; color: var(--el-text-color-secondary)">
        账号:<b>{{ bindForm.email }}</b>
      </div>
      <el-select v-model="bindForm.proxy_id" clearable placeholder="选择代理(留空=解绑)" style="width: 100%">
        <el-option :value="0" label="不绑定 / 解绑" />
        <el-option
          v-for="p in proxies"
          :key="p.id"
          :label="`#${p.id} ${p.remark || p.host}:${p.port}`"
          :value="p.id"
        />
      </el-select>
      <template #footer>
        <el-button @click="bindDlg = false">取消</el-button>
        <el-button type="primary" @click="submitBind">确定</el-button>
      </template>
    </el-dialog>

    <!-- 批量导入弹窗 -->
    <el-dialog v-model="importDlg" title="批量导入账号" width="760px" destroy-on-close>
      <!-- 模式 tab -->
      <el-tabs v-model="importMode" class="import-tabs">
        <el-tab-pane label="Session JSON" name="session_json" />
        <el-tab-pane label="JSON 文件" name="json" />
        <el-tab-pane label="Access Token" name="at" />
        <el-tab-pane label="Refresh Token" name="rt" />
        <el-tab-pane label="Session Token" name="st" />
      </el-tabs>

      <!-- Session JSON 模式 -->
      <template v-if="importMode === 'session_json'">
        <div class="tip">
          直接粘贴 <code>chatgpt.com/api/auth/session</code> 返回的<b>完整 JSON</b>。
          系统会自动提取 accessToken、email、过期时间和订阅类型。每个 JSON 一行，支持多行（多个账号）。
        </div>
        <el-input
          v-model="importForm.tokens_text"
          type="textarea" :rows="10"
          placeholder='{"WARNING_BANNER":"...","user":{"email":"..."},"expires":"...","accessToken":"eyJ..."}'
          spellcheck="false"
        />
        <div class="token-count">共 {{ tokenLineCount }} 行</div>
      </template>

      <!-- JSON 模式 -->
      <template v-else-if="importMode === 'json'">
        <div class="tip">
          支持两种 JSON:<b>sub2api-account-*.json</b>(多账号)与 <b>token_*.json</b>(单账号)。
          可一次选择多个文件,前端会按每批 {{ BATCH_FILES }} 个文件 / {{ humanSize(BATCH_BYTES) }} 自动分批上传,不会卡页面。
        </div>

        <!-- 文件选择 + 拖拽 -->
        <div class="drop-zone" @dragover.prevent @drop="onDropFiles">
          <el-icon class="drop-ic"><Upload /></el-icon>
          <div class="drop-text">
            把 JSON 文件拖到这里,或
            <label class="link">
              <input type="file" accept=".json,.txt,application/json" multiple hidden @change="onPickFiles" />
              选择文件
            </label>
          </div>
          <div class="drop-sub">
            已选 <b>{{ importForm.files.length }}</b> 个文件
            <span v-if="importForm.files.length > 0"> · 合计 {{ humanSize(totalFileSize) }}</span>
          </div>
        </div>

        <div v-if="importForm.files.length" class="file-list">
          <div class="file-list-head">
            <span>{{ importForm.files.length }} 个文件</span>
            <el-button link type="danger" size="small" @click="clearFiles">清空</el-button>
          </div>
          <div class="file-list-body">
            <div v-for="(f, i) in importForm.files.slice(0, 50)" :key="i" class="file-row">
              <span class="fname">{{ f.name }}</span>
              <span class="fsize">{{ humanSize(f.size) }}</span>
              <el-button link size="small" @click="removeFile(i)">×</el-button>
            </div>
            <div v-if="importForm.files.length > 50" class="muted" style="text-align:center;margin-top:6px">
              ……另有 {{ importForm.files.length - 50 }} 个文件未展示
            </div>
          </div>
        </div>

        <el-divider content-position="left">或粘贴 JSON 文本</el-divider>
        <el-input
          v-model="importForm.text"
          type="textarea" :rows="5"
          placeholder="粘贴 sub2api 或 token_*.json 内容,多个 JSON 可以直接换行拼接(JSONL)"
          spellcheck="false"
        />
      </template>

      <!-- AT 模式 -->
      <template v-else-if="importMode === 'at'">
        <div class="tip">
          一行一个 <b>access_token</b>(eyJ... 开头的 JWT)。
          服务端会解析 JWT payload 里的 email 作为账号唯一键,若 AT 里没有 email 字段,该行会进入失败列表。
        </div>
        <el-input
          v-model="importForm.tokens_text"
          type="textarea" :rows="10"
          placeholder="eyJhbGci...&#10;eyJhbGci...&#10;..."
          spellcheck="false"
        />
        <div class="token-count">共 {{ tokenLineCount }} 行</div>
      </template>

      <!-- RT 模式 -->
      <template v-else-if="importMode === 'rt'">
        <div class="tip">
          一行一个 <b>refresh_token</b>。系统会用你填写的 <b>APPID(client_id)</b> 向
          <code>auth.openai.com/oauth/token</code> 换出 AT,再从 AT 解出 email 后入库。
          <strong class="warn">需要选择代理,否则大概率超时。</strong>
        </div>
        <el-input
          v-model="importForm.tokens_text"
          type="textarea" :rows="9"
          placeholder="v1.rt_...&#10;v1.rt_...&#10;..."
          spellcheck="false"
        />
        <div class="token-count">共 {{ tokenLineCount }} 行</div>
      </template>

      <!-- ST 模式 -->
      <template v-else-if="importMode === 'st'">
        <div class="tip">
          一行一个 <b>session_token</b>(浏览器 cookie 里的 <code>__Secure-next-auth.session-token</code>)。
          系统会用它调 <code>chatgpt.com/api/auth/session</code> 换出 AT,再从 AT 解 email。
          <strong class="warn">ST 模式必须有代理(chatgpt.com 国内不可直连)。</strong>
        </div>
        <el-input
          v-model="importForm.tokens_text"
          type="textarea" :rows="10"
          placeholder="eyJhbGci...&#10;eyJhbGci...&#10;..."
          spellcheck="false"
        />
        <div class="token-count">共 {{ tokenLineCount }} 行</div>
      </template>

      <div style="margin-top: 14px; display: flex; flex-wrap: wrap; gap: 14px; align-items: center">
        <el-checkbox v-model="importForm.update_existing">邮箱已存在则更新 token</el-checkbox>
        <div>
          <span class="muted" style="margin-right: 6px">
            {{ importMode === 'rt' ? 'APPID(client_id,必填)' : 'client_id' }}
          </span>
          <el-input
            v-model="importForm.default_client_id"
            size="small" style="width: 280px"
            :placeholder="importMode === 'rt' ? 'app_xxxxxxxxxxxxxxxxxxxxxxxx(必填)' : '可选,默认 ChatGPT iOS'"
          />
        </div>
        <div>
          <span class="muted" style="margin-right: 6px">
            {{ importMode === 'st' || importMode === 'rt' ? '代理(强烈推荐)' : '默认代理' }}
          </span>
          <el-select v-model="importForm.default_proxy_id" clearable size="small" style="width: 220px">
            <el-option :value="0" label="不绑定" />
            <el-option v-for="p in proxies" :key="p.id" :label="`#${p.id} ${p.remark || p.host}:${p.port}`" :value="p.id" />
          </el-select>
        </div>
      </div>

      <!-- 进度条 -->
      <div v-if="importProgress.running || importResult" class="progress">
        <div class="progress-head">
          <span v-if="importProgress.running">
            正在导入:第 <b>{{ importProgress.current }}</b> / {{ importProgress.totalBatches }} 批
          </span>
          <span v-else>
            导入已完成
          </span>
          <span class="stat">
            <el-tag type="success" size="small">+{{ importProgress.created }}</el-tag>
            <el-tag type="warning" size="small">~{{ importProgress.updated }}</el-tag>
            <el-tag type="info" size="small">跳过 {{ importProgress.skipped }}</el-tag>
            <el-tag type="danger" size="small">失败 {{ importProgress.failed }}</el-tag>
          </span>
        </div>
        <el-progress
          :percentage="importProgress.totalBatches > 0
            ? Math.round((importProgress.current / importProgress.totalBatches) * 100)
            : 0"
          :status="importProgress.running ? '' : (importProgress.failed > 0 ? 'warning' : 'success')"
        />
      </div>

      <!-- 错误/跳过明细 -->
      <div v-if="importLastErrors.length" class="err-list">
        <div class="err-list-head">未成功明细({{ importLastErrors.length }})</div>
        <el-table :data="importLastErrors" size="small" max-height="220">
          <el-table-column prop="email" label="邮箱" min-width="200" />
          <el-table-column label="状态" width="90">
            <template #default="{ row }">
              <el-tag size="small" :type="row.status === 'failed' ? 'danger' : 'info'">
                {{ row.status === 'failed' ? '失败' : '跳过' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="reason" label="原因" min-width="240" show-overflow-tooltip />
        </el-table>
      </div>

      <template #footer>
        <el-button :disabled="importing" @click="importDlg = false">关闭</el-button>
        <el-button type="primary" :loading="importing" @click="doImport">
          开始导入
          <span v-if="importMode === 'json' && importForm.files.length > 0">
            ({{ importForm.files.length }} 个文件)
          </span>
          <span v-else-if="importMode !== 'json' && tokenLineCount > 0">
            ({{ tokenLineCount }} 条 {{ importMode.toUpperCase() }})
          </span>
        </el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.hdr { margin-bottom: 14px !important; }
.hdr-left .page-sub {
  color: var(--el-text-color-secondary);
  font-size: 13px;
  margin-top: 4px;
}
.actions {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.flex-between {
  display: flex; align-items: center; justify-content: space-between; gap: 16px;
}

.filter-form :deep(.el-form-item) { margin-bottom: 0; }
.auto-refresh-item { margin-left: 4px; }
.auto-refresh-hint {
  color: var(--el-text-color-secondary);
  font-size: 12px;
  margin-left: 4px;
}

.email {
  color: var(--el-text-color-primary);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  display: inline-block; max-width: 100%;
}

.refresh-cell {
  display: flex; align-items: center; gap: 6px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}

.creds { display: flex; gap: 4px; }
.quota b { color: var(--el-color-primary); font-weight: 600; }
.muted { color: var(--el-text-color-secondary); }
.warn  { color: var(--el-color-warning); font-weight: 500; }
.err   {
  color: var(--el-color-danger);
  font-size: 12px;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  margin-top: 2px;
}

.token-field {
  display: flex; align-items: flex-start; gap: 8px; width: 100%;
  :deep(.el-textarea) { flex: 1; }
}

.pager {
  display: flex; justify-content: flex-end;
  margin-top: 14px;
}

/* ====== 批量导入弹窗 ====== */
.import-tabs {
  margin-top: -8px;
  margin-bottom: 10px;
  :deep(.el-tabs__header) { margin-bottom: 12px; }
}
.tip {
  color: var(--el-text-color-secondary);
  font-size: 13px; line-height: 1.6;
  background: var(--el-fill-color-light);
  padding: 10px 12px;
  border-radius: 8px;
  margin-bottom: 12px;
  code {
    background: rgba(0, 0, 0, 0.06);
    padding: 1px 6px;
    border-radius: 4px;
    font-family: inherit;
  }
  .warn {
    color: var(--el-color-warning);
    margin-left: 4px;
  }
}
.token-count {
  text-align: right;
  font-size: 12px;
  color: var(--el-text-color-secondary);
  margin-top: 6px;
}

.drop-zone {
  border: 1.5px dashed var(--el-border-color);
  border-radius: 10px;
  padding: 22px 14px;
  text-align: center;
  background: var(--el-fill-color-lighter);
  transition: border-color 0.15s, background-color 0.15s;
  &:hover {
    border-color: var(--el-color-primary);
    background: var(--el-color-primary-light-9);
  }
  .drop-ic {
    font-size: 30px; color: var(--el-color-primary); margin-bottom: 6px;
  }
  .drop-text {
    font-size: 14px;
    color: var(--el-text-color-primary);
    .link {
      color: var(--el-color-primary);
      cursor: pointer;
      text-decoration: underline;
      margin-left: 2px;
    }
  }
  .drop-sub {
    margin-top: 6px;
    font-size: 12px;
    color: var(--el-text-color-secondary);
  }
}

.file-list {
  margin-top: 10px;
  border: 1px solid var(--el-border-color-lighter);
  border-radius: 8px;
  max-height: 200px;
  overflow: auto;
  padding: 8px 10px;
  .file-list-head {
    display: flex; justify-content: space-between; align-items: center;
    padding-bottom: 4px;
    color: var(--el-text-color-secondary);
    font-size: 12px;
    border-bottom: 1px dashed var(--el-border-color-lighter);
    margin-bottom: 4px;
  }
  .file-row {
    display: flex; align-items: center; justify-content: space-between;
    padding: 4px 0;
    font-size: 13px;
    .fname { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .fsize { color: var(--el-text-color-secondary); margin: 0 8px; font-variant-numeric: tabular-nums; }
  }
}

.progress {
  margin-top: 16px;
  padding: 12px 14px;
  background: var(--el-fill-color-lighter);
  border-radius: 8px;
  .progress-head {
    display: flex; justify-content: space-between; align-items: center;
    margin-bottom: 6px;
    .stat { display: flex; gap: 6px; }
  }
}

.err-list {
  margin-top: 12px;
  .err-list-head {
    color: var(--el-color-danger);
    font-weight: 500;
    margin-bottom: 6px;
  }
}
</style>
