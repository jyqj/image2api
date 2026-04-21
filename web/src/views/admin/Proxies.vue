<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as proxyApi from '@/api/proxies'
import { formatDateTime } from '@/utils/format'

const loading = ref(false)
const rows = ref<proxyApi.Proxy[]>([])
const total = ref(0)
const pager = reactive({ page: 1, page_size: 20 })

async function fetchList() {
  loading.value = true
  try {
    const data = await proxyApi.listProxies(pager)
    rows.value = data.list
    total.value = data.total
  } finally { loading.value = false }
}

const dlg = ref(false)
const isEdit = ref(false)
const form = reactive<proxyApi.ProxyCreate & { id?: number }>({
  id: 0, scheme: 'http', host: '', port: 0, username: '', password: '',
  country: '', isp: '', enabled: true, remark: '',
})

function openCreate() {
  isEdit.value = false
  Object.assign(form, {
    id: 0, scheme: 'http', host: '', port: 0, username: '', password: '',
    country: '', isp: '', enabled: true, remark: '',
  })
  dlg.value = true
}
function openEdit(row: proxyApi.Proxy) {
  isEdit.value = true
  Object.assign(form, {
    id: row.id, scheme: row.scheme, host: row.host, port: row.port,
    username: row.username, password: '',
    country: row.country, isp: row.isp, enabled: row.enabled, remark: row.remark,
  })
  dlg.value = true
}

async function submit() {
  if (!form.host) return ElMessage.warning('host 不能为空')
  if (!form.port) return ElMessage.warning('port 不能为空')
  const payload: proxyApi.ProxyUpdate = {
    scheme: form.scheme!,
    host: form.host,
    port: Number(form.port),
    username: form.username || '',
    password: form.password || '',
    country: form.country || '',
    isp: form.isp || '',
    enabled: !!form.enabled,
    remark: form.remark || '',
  }
  if (isEdit.value && form.id) await proxyApi.updateProxy(form.id, payload)
  else await proxyApi.createProxy(payload)
  ElMessage.success('保存成功')
  dlg.value = false
  fetchList()
}

async function toggleEnabled(row: proxyApi.Proxy) {
  await proxyApi.updateProxy(row.id, {
    scheme: row.scheme, host: row.host, port: row.port,
    username: row.username, password: '',
    country: row.country, isp: row.isp, remark: row.remark,
    enabled: !row.enabled,
  })
  ElMessage.success(row.enabled ? '已禁用' : '已启用')
  fetchList()
}

async function onDelete(row: proxyApi.Proxy) {
  await ElMessageBox.confirm(`确认删除代理 ${row.host}:${row.port}?`, '删除代理', {
    type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消',
  })
  await proxyApi.deleteProxy(row.id)
  ElMessage.success('已删除')
  fetchList()
}

// ---------- 健康探测 ----------
const probingIds = ref<Set<number>>(new Set())
const probeAllLoading = ref(false)

async function onProbe(row: proxyApi.Proxy) {
  if (probingIds.value.has(row.id)) return
  probingIds.value.add(row.id)
  try {
    const res = await proxyApi.probeProxy(row.id)
    // 同步更新当前行,避免等列表刷新才看到反馈
    row.health_score = res.health_score
    row.last_probe_at = res.tried_at
    row.last_error = res.ok ? '' : (res.error || 'failed')
    if (res.ok) {
      ElMessage.success(`连通正常 · ${res.latency_ms} ms`)
    } else {
      ElMessage.error(`探测失败:${res.error || 'unknown'}`)
    }
  } catch (e: any) {
    ElMessage.error(e?.message || '探测失败')
  } finally {
    probingIds.value.delete(row.id)
    // 重新 new 一份触发 reactive(因为直接修改 Set 内部,模板里 has() 不会响应)
    probingIds.value = new Set(probingIds.value)
  }
}

async function onProbeAll() {
  await ElMessageBox.confirm(
    '将对所有启用的代理发起连通性探测,耗时取决于代理数量。是否继续?',
    '全部探测',
    { type: 'info', confirmButtonText: '开始', cancelButtonText: '取消' },
  )
  probeAllLoading.value = true
  try {
    const res = await proxyApi.probeAllProxies()
    ElMessage.success(`探测完成 · 共 ${res.total} · 通 ${res.ok} · 断 ${res.bad}`)
    fetchList()
  } catch (e: any) {
    ElMessage.error(e?.message || '探测失败')
  } finally {
    probeAllLoading.value = false
  }
}

// ---------- 批量导入 ----------
const importDlg = ref(false)
const importLoading = ref(false)
const importForm = reactive({
  text: '',
  enabled: true,
  country: '',
  isp: '',
  remark: '',
  overwrite: false,
  probe_after_import: true,
})
const importResult = ref<proxyApi.ProxyImportResp | null>(null)

function openImport() {
  Object.assign(importForm, {
    text: '', enabled: true, country: '', isp: '', remark: '', overwrite: false, probe_after_import: true,
  })
  importResult.value = null
  importDlg.value = true
}

async function doImport() {
  if (!importForm.text.trim()) return ElMessage.warning('请粘贴至少一行代理 URL')
  importLoading.value = true
  try {
    importResult.value = await proxyApi.importProxies({
      text: importForm.text,
      enabled: importForm.enabled,
      country: importForm.country,
      isp: importForm.isp,
      remark: importForm.remark,
      overwrite: importForm.overwrite,
      probe_after_import: importForm.probe_after_import,
    })
    const r = importResult.value
    const probeText = r.probe ? ` · 探测通 ${r.probe.ok}/${r.probe.total}` : (r.probe_queued ? ' · 已排队探测' : '')
    ElMessage.success(
      `完成 · 新增 ${r.created} · 更新 ${r.updated} · 跳过 ${r.skipped} · 无效 ${r.invalid}${probeText}`,
    )
    fetchList()
  } finally { importLoading.value = false }
}

function importStatusTag(s: string) {
  switch (s) {
    case 'created': return 'success'
    case 'updated': return 'primary'
    case 'skipped': return 'info'
    default: return 'danger'
  }
}
function importStatusText(s: string) {
  return { created: '新增', updated: '更新', skipped: '跳过', invalid: '无效' }[s] || s
}

function healthColor(score: number) {
  if (score >= 80) return 'success'
  if (score >= 50) return 'warning'
  return 'danger'
}

onMounted(fetchList)
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <div class="flex-between" style="margin-bottom:12px">
        <div>
          <h2 class="page-title" style="margin:0">代理管理</h2>
          <div style="color:var(--el-text-color-secondary);font-size:13px;margin-top:4px">
            维护 HTTP / SOCKS5 代理池,所有 GPT 账号都应绑定独立代理以分散风控指纹;健康分由定时探测自动维护,探测参数可在「系统设置 → 网关与调度」调整。
          </div>
        </div>
        <div class="flex-wrap-gap">
          <el-button :loading="probeAllLoading" @click="onProbeAll">
            <el-icon><Promotion /></el-icon> 全部探测
          </el-button>
          <el-button @click="openImport"><el-icon><Upload /></el-icon> 批量导入</el-button>
          <el-button type="primary" @click="openCreate"><el-icon><Plus /></el-icon> 新建代理</el-button>
        </div>
      </div>

      <el-table v-loading="loading" :data="rows" stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column label="地址" min-width="220">
          <template #default="{ row }">
            <code>{{ row.scheme }}://{{ row.host }}:{{ row.port }}</code>
            <div v-if="row.username" style="font-size:12px;color:var(--el-text-color-secondary)">
              auth: {{ row.username }} / ******
            </div>
          </template>
        </el-table-column>
        <el-table-column label="区域" width="130">
          <template #default="{ row }">
            <div>{{ row.country || '-' }}</div>
            <div style="font-size:12px;color:var(--el-text-color-secondary)">{{ row.isp || '' }}</div>
          </template>
        </el-table-column>
        <el-table-column label="健康" width="150">
          <template #default="{ row }">
            <el-progress :percentage="Math.max(0, Math.min(100, row.health_score))"
                         :status="row.health_score >= 80 ? 'success' : row.health_score >= 50 ? 'warning' : 'exception'" />
            <el-tag v-if="row.last_error" :type="healthColor(row.health_score)" size="small" style="margin-top:4px">
              {{ row.last_error.slice(0, 30) }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="最近探测" width="170">
          <template #default="{ row }">{{ formatDateTime(row.last_probe_at) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-switch :model-value="row.enabled" @change="() => toggleEnabled(row)" />
          </template>
        </el-table-column>
        <el-table-column prop="remark" label="备注" min-width="180" show-overflow-tooltip />
        <el-table-column label="操作" width="200" fixed="right">
          <template #default="{ row }">
            <el-button link type="success" :loading="probingIds.has(row.id)" @click="onProbe(row)">探测</el-button>
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="onDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination style="margin-top:16px;display:flex;justify-content:flex-end"
        v-model:current-page="pager.page"
        v-model:page-size="pager.page_size"
        :total="total"
        :page-sizes="[20, 50, 100]"
        layout="total, sizes, prev, pager, next"
        @current-change="fetchList"
        @size-change="fetchList"
      />
    </div>

    <el-dialog v-model="dlg" :title="isEdit ? '编辑代理' : '新建代理'" width="520px">
      <el-form :model="form" label-width="100px">
        <el-form-item label="协议">
          <el-select v-model="form.scheme" style="width:100%">
            <el-option label="http" value="http" />
            <el-option label="https" value="https" />
            <el-option label="socks5" value="socks5" />
          </el-select>
        </el-form-item>
        <el-form-item label="Host" required><el-input v-model="form.host" placeholder="192.0.2.1" /></el-form-item>
        <el-form-item label="Port" required>
          <el-input-number v-model="form.port" :min="1" :max="65535" style="width:100%" />
        </el-form-item>
        <el-form-item label="用户名"><el-input v-model="form.username" autocomplete="off" /></el-form-item>
        <el-form-item label="密码">
          <el-input v-model="form.password" type="password" show-password autocomplete="new-password"
                    :placeholder="isEdit ? '留空表示不改' : ''" />
        </el-form-item>
        <el-form-item label="国家/地区"><el-input v-model="form.country" placeholder="US / JP / HK …" /></el-form-item>
        <el-form-item label="ISP"><el-input v-model="form.isp" /></el-form-item>
        <el-form-item label="启用"><el-switch v-model="form.enabled" /></el-form-item>
        <el-form-item label="备注"><el-input v-model="form.remark" type="textarea" :rows="2" /></el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="dlg = false">取消</el-button>
        <el-button type="primary" @click="submit">保存</el-button>
      </template>
    </el-dialog>

    <!-- 批量导入 -->
    <el-dialog v-model="importDlg" title="批量导入代理" width="720px">
      <el-form label-width="88px" @submit.prevent>
        <el-form-item label="代理列表">
          <el-input
            v-model="importForm.text"
            type="textarea"
            :rows="10"
            resize="vertical"
            placeholder="每行一个,支持以下格式:
http://user:pass@host:port
https://host:port
socks5://user:pass@host:port
user:pass@host:port    (省略 scheme 默认 http)
host:port:user:pass    (代理商常见格式)
user:pass:host:port    (另一种代理商格式)
# 以 # 或 // 开头的行会被跳过"
          />
          <div class="import-hint">
            支持 http / https / socks5。同一 scheme + host + port + username 视为已存在。
          </div>
        </el-form-item>
        <el-form-item label="默认启用">
          <el-switch v-model="importForm.enabled" />
        </el-form-item>
        <el-form-item label="国家/地区">
          <el-input v-model="importForm.country" placeholder="如 US / HK,空则每条自行为空" style="max-width:240px" />
        </el-form-item>
        <el-form-item label="ISP">
          <el-input v-model="importForm.isp" placeholder="如 Arxlabs" style="max-width:240px" />
        </el-form-item>
        <el-form-item label="备注">
          <el-input v-model="importForm.remark" placeholder="将填到所有新增行的 remark" />
        </el-form-item>
        <el-form-item label="覆盖已有">
          <el-switch v-model="importForm.overwrite" />
          <span class="import-hint" style="margin-left:8px">
            开启后:同 endpoint 已存在时更新密码/国家/ISP/备注;关闭则跳过。
          </span>
        </el-form-item>
        <el-form-item label="导入后探测">
          <el-switch v-model="importForm.probe_after_import" />
          <span class="import-hint" style="margin-left:8px">
            新增/更新数量 ≤ 50 时同步探测;更多时交给后台探测队列。
          </span>
        </el-form-item>
      </el-form>

      <div v-if="importResult" class="import-result">
        <div class="import-summary">
          共 {{ importResult.total }} 行 ·
          <el-tag type="success" effect="plain">新增 {{ importResult.created }}</el-tag>
          <el-tag type="primary" effect="plain">更新 {{ importResult.updated }}</el-tag>
          <el-tag type="info" effect="plain">跳过 {{ importResult.skipped }}</el-tag>
          <el-tag type="danger" effect="plain">无效 {{ importResult.invalid }}</el-tag>
          <el-tag v-if="importResult.probe" type="warning" effect="plain">
            探测 {{ importResult.probe.ok }}/{{ importResult.probe.total }}
          </el-tag>
          <el-tag v-else-if="importResult.probe_queued" type="warning" effect="plain">已排队探测</el-tag>
        </div>
        <el-table :data="importResult.items" size="small" max-height="260" border>
          <el-table-column prop="line" label="行" width="60" />
          <el-table-column label="状态" width="80">
            <template #default="{ row }">
              <el-tag :type="importStatusTag(row.status)" size="small">{{ importStatusText(row.status) }}</el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="raw" label="内容" show-overflow-tooltip />
          <el-table-column prop="error" label="说明" show-overflow-tooltip />
        </el-table>
      </div>

      <template #footer>
        <el-button @click="importDlg = false">关闭</el-button>
        <el-button type="primary" :loading="importLoading" @click="doImport">开始导入</el-button>
      </template>
    </el-dialog>
  </div>
</template>

<style scoped>
code {
  background: #f2f3f5;
  padding: 1px 6px;
  border-radius: 4px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
}
.flex-wrap-gap {
  display: inline-flex;
  gap: 8px;
}
.import-hint {
  font-size: 12px;
  color: var(--el-text-color-secondary);
  line-height: 1.5;
  margin-top: 4px;
}
.import-result {
  margin-top: 8px;
}
.import-summary {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-wrap: wrap;
  margin-bottom: 8px;
  color: var(--el-text-color-regular);
  font-size: 13px;
}
</style>
