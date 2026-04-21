<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import * as meApi from '@/api/me'

const loading = ref(false)
const stats = ref<meApi.MyStatsResp | null>(null)
const logs = ref<meApi.UsageItem[]>([])
const total = ref(0)
const filter = reactive({ type: '' as '' | 'chat' | 'image', status: '', limit: 20, offset: 0 })

const page = computed({
  get: () => Math.floor(filter.offset / filter.limit) + 1,
  set: (v: number) => { filter.offset = (v - 1) * filter.limit; loadLogs() },
})

async function loadStats() { stats.value = await meApi.getMyUsageStats({ days: 14, top_n: 10, type: filter.type }) }
async function loadLogs() {
  loading.value = true
  try {
    const d = await meApi.listMyUsageLogs(filter)
    logs.value = d.items || []
    total.value = d.total || 0
  } finally { loading.value = false }
}
async function reload() { filter.offset = 0; await Promise.all([loadStats(), loadLogs()]) }

onMounted(reload)
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <div class="flex-between">
        <div><h2 class="page-title">用量记录</h2><div class="sub">本页展示本地中转观测数据，仅用于运行排障和趋势观察。</div></div>
        <div class="flex-wrap-gap">
          <el-select v-model="filter.type" placeholder="类型" clearable style="width:120px" @change="reload"><el-option label="对话" value="chat" /><el-option label="生图" value="image" /></el-select>
          <el-select v-model="filter.status" placeholder="状态" clearable style="width:120px" @change="reload"><el-option label="成功" value="success" /><el-option label="失败" value="failed" /></el-select>
          <el-button @click="reload">刷新</el-button>
        </div>
      </div>
    </div>

    <div class="grid cards">
      <div class="card-block kpi"><span>请求</span><b>{{ stats?.overall.requests || 0 }}</b></div>
      <div class="card-block kpi"><span>失败</span><b>{{ stats?.overall.failures || 0 }}</b></div>
      <div class="card-block kpi"><span>输入 Token</span><b>{{ stats?.overall.input_tokens || 0 }}</b></div>
      <div class="card-block kpi"><span>输出 Token</span><b>{{ stats?.overall.output_tokens || 0 }}</b></div>
    </div>

    <div class="card-block" style="margin-top:16px">
      <el-table v-loading="loading" :data="logs" stripe>
        <el-table-column prop="created_at" label="时间" width="180" />
        <el-table-column prop="type" label="类型" width="90" />
        <el-table-column prop="model_slug" label="模型" min-width="150"><template #default="{ row }"><code>{{ row.model_slug || row.model_id }}</code></template></el-table-column>
        <el-table-column prop="status" label="状态" width="100" />
        <el-table-column prop="input_tokens" label="输入" width="100" />
        <el-table-column prop="output_tokens" label="输出" width="100" />
        <el-table-column prop="image_count" label="图片" width="90" />
        <el-table-column prop="duration_ms" label="耗时(ms)" width="110" />
        <el-table-column prop="error_code" label="错误" min-width="160" show-overflow-tooltip />
      </el-table>
      <div class="pager"><el-pagination layout="total, sizes, prev, pager, next" :total="total" v-model:current-page="page" v-model:page-size="filter.limit" @size-change="reload" /></div>
    </div>
  </div>
</template>

<style scoped>
.sub { color:var(--el-text-color-secondary); font-size:13px; margin-top:4px; }
.grid { display:grid; gap:16px; margin-top:16px; }
.cards { grid-template-columns: repeat(4, minmax(0,1fr)); }
.kpi span { color:var(--el-text-color-secondary); display:block; margin-bottom:8px; }
.kpi b { font-size:24px; }
.pager { display:flex; justify-content:flex-end; margin-top:14px; }
code { background:#f2f3f5; padding:1px 6px; border-radius:4px; }
@media (max-width: 960px) { .cards { grid-template-columns:1fr; } }
</style>
