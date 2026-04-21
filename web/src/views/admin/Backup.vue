<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import * as backupApi from '@/api/backup'
import { formatBytes, formatDateTime } from '@/utils/format'

const loading = ref(false)
const items = ref<backupApi.BackupFile[]>([])
const total = ref(0)
const allowRestore = ref(false)
const maxUploadMB = ref(512)
const page = reactive({ limit: 50, offset: 0 })
const creating = ref(false)

async function load() {
  loading.value = true
  try {
    const d = await backupApi.listBackups(page.limit, page.offset)
    items.value = d.items
    total.value = d.total
    allowRestore.value = d.allow_restore
    maxUploadMB.value = d.max_upload_mb
  } finally { loading.value = false }
}

async function onCreate() {
  creating.value = true
  try {
    await backupApi.createBackup(true)
    ElMessage.success('备份已创建')
    load()
  } finally { creating.value = false }
}

function download(row: backupApi.BackupFile) {
  return backupApi.downloadBackup(row.backup_id, row.file_name)
}

async function onDelete(row: backupApi.BackupFile) {
  const ok = await ElMessageBox.confirm(`确认删除 ${row.file_name}?`, '删除备份', { type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消' }).catch(() => null)
  if (!ok) return
  await backupApi.deleteBackup(row.backup_id)
  ElMessage.success('已删除')
  load()
}

async function onRestore(row: backupApi.BackupFile) {
  if (!allowRestore.value) return ElMessage.error('后端未启用恢复功能')
  await ElMessageBox.confirm(
    `恢复会覆盖当前数据库!此操作不可撤销。你已理解风险并希望继续?`,
    '恢复数据库', { type: 'error', confirmButtonText: '我确认继续', cancelButtonText: '取消' },
  )
  const ok = await ElMessageBox.confirm('最后一次确认:立即执行恢复?', '恢复数据库', { type: 'error', confirmButtonText: '执行恢复', cancelButtonText: '取消' }).catch(() => null)
  if (!ok) return
  ElMessage.info('正在恢复,请稍候…')
  await backupApi.restoreBackup(row.backup_id)
  ElMessage.success('恢复成功,请刷新页面')
}

// ---- 上传 ----
const uploadDlg = ref(false)
const uploadFile = ref<File | null>(null)
const uploadPct = ref(0)
const uploading = ref(false)

function pickFile(e: Event) {
  const t = e.target as HTMLInputElement
  uploadFile.value = t.files?.[0] || null
}
async function doUpload() {
  if (!uploadFile.value) return ElMessage.warning('请选择 .sql.gz 文件')
  uploading.value = true
  uploadPct.value = 0
  try {
    await backupApi.uploadBackup(uploadFile.value, (p) => (uploadPct.value = p))
    ElMessage.success('上传成功')
    uploadDlg.value = false
    uploadFile.value = null
    load()
  } finally { uploading.value = false }
}

onMounted(load)
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <div class="flex-between" style="margin-bottom:12px">
        <div>
          <h2 class="page-title" style="margin:0">数据备份</h2>
          <div style="color:var(--el-text-color-secondary);font-size:13px;margin-top:4px">
            本地最多保留 {{ total }} 个备份文件,上传单文件上限
            <el-tag size="small">{{ maxUploadMB }} MB</el-tag>
            · 恢复功能:
            <el-tag :type="allowRestore ? 'success' : 'info'" size="small">
              {{ allowRestore ? '已启用' : '已禁用(需后端 backup.allow_restore=true)' }}
            </el-tag>
          </div>
        </div>
        <div class="flex-wrap-gap">
          <el-button @click="uploadDlg = true"><el-icon><Upload /></el-icon> 上传</el-button>
          <el-button type="primary" :loading="creating" @click="onCreate">
            <el-icon><FolderAdd /></el-icon> 立即备份
          </el-button>
        </div>
      </div>

      <el-table v-loading="loading" :data="items" stripe>
        <el-table-column prop="backup_id" label="ID" width="220" />
        <el-table-column prop="file_name" label="文件" min-width="240" show-overflow-tooltip />
        <el-table-column label="大小" width="100">
          <template #default="{ row }">{{ formatBytes(row.size_bytes) }}</template>
        </el-table-column>
        <el-table-column label="来源" width="90">
          <template #default="{ row }">
            <el-tag size="small">{{ row.trigger }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="90">
          <template #default="{ row }">
            <el-tag :type="row.status === 'ready' ? 'success' : row.status === 'failed' ? 'danger' : 'info'"
                    size="small">
              {{ row.status }}
            </el-tag>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="170">
          <template #default="{ row }">{{ formatDateTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column prop="sha256" label="SHA256" min-width="180" show-overflow-tooltip />
        <el-table-column label="操作" width="240" fixed="right">
          <template #default="{ row }">
            <el-button size="small" link type="primary"
                       :disabled="row.status !== 'ready'" @click="download(row)">下载</el-button>
            <el-button size="small" link type="warning"
                       :disabled="!allowRestore || row.status !== 'ready'" @click="onRestore(row)">恢复</el-button>
            <el-button size="small" link type="danger" @click="onDelete(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="uploadDlg" title="上传备份" width="460px">
      <el-alert type="info" :closable="false" show-icon style="margin-bottom:12px"
                title="仅接受 .sql.gz 格式;恢复仍需单独操作。" />
      <el-form label-width="110px">
        <el-form-item label="文件">
          <input type="file" accept=".gz,.sql.gz" @change="pickFile" />
          <div v-if="uploadFile" style="font-size:12px;margin-top:6px;color:var(--el-text-color-secondary)">
            已选择 {{ uploadFile.name }} · {{ formatBytes(uploadFile.size) }}
          </div>
        </el-form-item>
        <el-form-item v-if="uploading" label="进度">
          <el-progress :percentage="uploadPct" />
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="uploadDlg = false">取消</el-button>
        <el-button type="primary" :loading="uploading" @click="doUpload">上传</el-button>
      </template>
    </el-dialog>
  </div>
</template>
