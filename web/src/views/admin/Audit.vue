<script setup lang="ts">
import { ref, reactive, onMounted } from 'vue'
import * as auditApi from '@/api/audit'
import { formatDateTime } from '@/utils/format'

const loading = ref(false)
const filter = reactive<auditApi.AuditFilter>({ action: '', actor_id: undefined, limit: 50, offset: 0 })
const items = ref<auditApi.AuditLog[]>([])
const total = ref(0)

async function load() {
  loading.value = true
  try {
    const d = await auditApi.listAudit({ ...filter, actor_id: filter.actor_id || undefined })
    items.value = d.items
    total.value = d.total
  } finally { loading.value = false }
}

const detailDlg = ref(false)
const detailRow = ref<auditApi.AuditLog | null>(null)
function openDetail(row: auditApi.AuditLog) {
  detailRow.value = row
  detailDlg.value = true
}

onMounted(load)
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <h2 class="page-title" style="margin:0">审计日志</h2>
      <div style="color:var(--el-text-color-secondary);font-size:13px;margin:4px 0 12px">
        记录控制台写操作(新增 / 修改 / 删除 / 备份恢复),按 action 与操作者 ID 可精确回溯。
      </div>
      <el-form inline class="flex-wrap-gap" @submit.prevent="load">
        <el-input v-model="filter.action" placeholder="action(如 accounts.update)" clearable style="width:220px" />
        <el-input-number v-model="filter.actor_id" placeholder="操作者 ID" :min="0" style="width:170px" />
        <el-button type="primary" @click="load"><el-icon><Search /></el-icon> 查询</el-button>
      </el-form>

      <el-table v-loading="loading" :data="items" stripe style="margin-top:12px">
        <el-table-column prop="id" label="ID" width="80" />
        <el-table-column label="操作者" min-width="200">
          <template #default="{ row }">
            <div>{{ row.actor_email || '-' }}</div>
            <div style="font-size:12px;color:var(--el-text-color-secondary)">ID: {{ row.actor_id }}</div>
          </template>
        </el-table-column>
        <el-table-column prop="action" label="Action" min-width="180" />
        <el-table-column prop="method" label="Method" width="90" />
        <el-table-column prop="path" label="Path" min-width="220" show-overflow-tooltip />
        <el-table-column prop="status_code" label="Status" width="80" />
        <el-table-column prop="target" label="Target" min-width="100" show-overflow-tooltip />
        <el-table-column prop="ip" label="IP" width="120" />
        <el-table-column label="时间" width="170">
          <template #default="{ row }">{{ formatDateTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="" width="70" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="openDetail(row)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>

      <el-pagination style="margin-top:16px;display:flex;justify-content:flex-end"
        :current-page="Math.floor((filter.offset || 0) / (filter.limit || 50)) + 1"
        @current-change="(p: number) => { filter.offset = (p - 1) * (filter.limit || 50); load() }"
        :page-size="filter.limit"
        :total="total"
        :page-sizes="[50, 100, 200]"
        @size-change="(s: number) => { filter.limit = s; filter.offset = 0; load() }"
        layout="total, sizes, prev, pager, next"
      />
    </div>

    <el-dialog v-model="detailDlg" title="审计详情" width="620px">
      <el-descriptions v-if="detailRow" :column="2" border size="small">
        <el-descriptions-item label="ID">{{ detailRow.id }}</el-descriptions-item>
        <el-descriptions-item label="时间">{{ formatDateTime(detailRow.created_at) }}</el-descriptions-item>
        <el-descriptions-item label="Action">{{ detailRow.action }}</el-descriptions-item>
        <el-descriptions-item label="Status">{{ detailRow.status_code }}</el-descriptions-item>
        <el-descriptions-item label="Method">{{ detailRow.method }}</el-descriptions-item>
        <el-descriptions-item label="Path">{{ detailRow.path }}</el-descriptions-item>
        <el-descriptions-item label="Actor">{{ detailRow.actor_email }} (#{{ detailRow.actor_id }})</el-descriptions-item>
        <el-descriptions-item label="IP">{{ detailRow.ip }}</el-descriptions-item>
        <el-descriptions-item label="UA" :span="2">{{ detailRow.ua }}</el-descriptions-item>
        <el-descriptions-item label="Target" :span="2">{{ detailRow.target || '-' }}</el-descriptions-item>
        <el-descriptions-item label="Meta" :span="2">
          <pre class="meta">{{ typeof detailRow.meta === 'string' ? detailRow.meta : JSON.stringify(detailRow.meta, null, 2) }}</pre>
        </el-descriptions-item>
      </el-descriptions>
    </el-dialog>
  </div>
</template>

<style scoped lang="scss">
.meta {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  background: #f7f8fa;
  padding: 8px;
  border-radius: 4px;
  max-height: 280px;
  overflow: auto;
  margin: 0;
}
</style>
