<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import * as meApi from '@/api/me'
import * as accountApi from '@/api/accounts'
import * as proxyApi from '@/api/proxies'

const router = useRouter()
const loading = ref(false)
const stats = ref<meApi.MyStatsResp | null>(null)
const models = ref<meApi.SimpleModel[]>([])
const tasks = ref<meApi.ImageTask[]>([])
const accounts = ref<accountApi.Account[]>([])
const accountTotal = ref(0)
const proxies = ref<proxyApi.Proxy[]>([])
const proxyTotal = ref(0)

const overall = computed(() => stats.value?.overall)
const okRate = computed(() => {
  const o = overall.value
  if (!o?.requests) return '100%'
  return `${Math.max(0, Math.round(((o.requests - o.failures) / o.requests) * 100))}%`
})
const modelSummary = computed(() => ({
  chat: models.value.filter((m) => m.type === 'chat').length,
  image: models.value.filter((m) => m.type === 'image').length,
}))
const accountSummary = computed(() => {
  const rows = accounts.value
  return {
    total: accountTotal.value || rows.length,
    healthy: rows.filter((a) => a.status === 'healthy').length,
    warned: rows.filter((a) => a.status === 'warned').length,
    throttled: rows.filter((a) => a.status === 'throttled').length,
    dead: rows.filter((a) => ['dead', 'suspicious'].includes(a.status)).length,
  }
})
const proxySummary = computed(() => {
  const rows = proxies.value
  return {
    total: proxyTotal.value || rows.length,
    enabled: rows.filter((p) => p.enabled).length,
    good: rows.filter((p) => p.enabled && p.health_score >= 80).length,
    risk: rows.filter((p) => p.enabled && p.health_score < 50).length,
  }
})
const dailyRows = computed(() => stats.value?.daily || [])
const dailyMax = computed(() => Math.max(1, ...dailyRows.value.map((x) => x.requests || 0)))

async function load() {
  loading.value = true
  try {
    const [s, m, t, a, p] = await Promise.all([
      meApi.getMyUsageStats({ days: 7, top_n: 8 }),
      meApi.listMyModels(),
      meApi.listMyImageTasks({ limit: 8 }),
      accountApi.listAccounts({ page: 1, page_size: 100 }).catch(() => null),
      proxyApi.listProxies({ page: 1, page_size: 100 }).catch(() => null),
    ])
    stats.value = s
    models.value = m.items || []
    tasks.value = t.items || []
    if (a) {
      accounts.value = a.list || []
      accountTotal.value = a.total || 0
    }
    if (p) {
      proxies.value = p.list || []
      proxyTotal.value = p.total || 0
    }
  } finally { loading.value = false }
}

function go(path: string) { router.push(path) }
function fmtTime(s: string) {
  if (!s) return '-'
  return s.replace('T', ' ').replace(/\+.*/, '').slice(0, 19)
}
function barWidth(v: number) {
  return `${Math.max(6, Math.round((v / dailyMax.value) * 100))}%`
}
function dayLabel(s: string) {
  return s?.slice(5) || '-'
}
function taskType(status: string) {
  return status === 'success' ? 'success' : status === 'failed' ? 'danger' : 'info'
}
onMounted(load)
</script>

<template>
  <div class="dash" v-loading="loading">
    <!-- Hero -->
    <div class="hero">
      <div class="hero-bg"></div>
      <div class="hero-inner">
        <div class="hero-text">
          <h1>本地中转控制台</h1>
          <p>客户端直接调用 <code>/v1</code> 接口，控制台负责账号池、代理池、模型映射和运行观察。</p>
        </div>
        <div class="hero-actions">
          <el-button type="primary" size="large" round @click="go('/personal/play')">
            <el-icon><VideoPlay /></el-icon>&nbsp;在线体验
          </el-button>
          <el-button size="large" round @click="go('/personal/docs')">
            <el-icon><Document /></el-icon>&nbsp;接口文档
          </el-button>
        </div>
      </div>
    </div>

    <!-- KPI -->
    <div class="kpi-row">
      <div class="kpi-card kpi-blue">
        <div class="kpi-top">
          <span class="kpi-label">7 日请求</span>
          <div class="kpi-dot"><el-icon :size="18"><Connection /></el-icon></div>
        </div>
        <div class="kpi-val">{{ overall?.requests || 0 }}</div>
      </div>
      <div class="kpi-card kpi-green">
        <div class="kpi-top">
          <span class="kpi-label">成功率</span>
          <div class="kpi-dot"><el-icon :size="18"><CircleCheck /></el-icon></div>
        </div>
        <div class="kpi-val">{{ okRate }}</div>
      </div>
      <div class="kpi-card kpi-orange">
        <div class="kpi-top">
          <span class="kpi-label">输入 Token</span>
          <div class="kpi-dot"><el-icon :size="18"><Tickets /></el-icon></div>
        </div>
        <div class="kpi-val">{{ overall?.input_tokens || 0 }}</div>
      </div>
      <div class="kpi-card kpi-purple">
        <div class="kpi-top">
          <span class="kpi-label">图片数量</span>
          <div class="kpi-dot"><el-icon :size="18"><PictureFilled /></el-icon></div>
        </div>
        <div class="kpi-val">{{ overall?.image_images || 0 }}</div>
      </div>
    </div>

    <!-- Ops overview -->
    <div class="ops-grid">
      <div class="section-card ops-card">
        <div class="section-head compact">
          <h2>账号池状态</h2>
          <el-button link type="primary" @click="go('/admin/accounts')">维护 →</el-button>
        </div>
        <div class="ops-main">
          <div>
            <div class="ops-num">{{ accountSummary.healthy }}/{{ accountSummary.total }}</div>
            <div class="ops-label">健康账号 / 总账号</div>
          </div>
          <el-tag type="warning" effect="plain">warned {{ accountSummary.warned }}</el-tag>
          <el-tag type="danger" effect="plain">异常 {{ accountSummary.dead + accountSummary.throttled }}</el-tag>
        </div>
      </div>

      <div class="section-card ops-card">
        <div class="section-head compact">
          <h2>代理池状态</h2>
          <el-button link type="primary" @click="go('/admin/proxies')">维护 →</el-button>
        </div>
        <div class="ops-main">
          <div>
            <div class="ops-num">{{ proxySummary.good }}/{{ proxySummary.enabled }}</div>
            <div class="ops-label">高健康代理 / 启用代理</div>
          </div>
          <el-tag :type="proxySummary.risk ? 'danger' : 'success'" effect="plain">风险 {{ proxySummary.risk }}</el-tag>
          <el-tag type="info" effect="plain">总 {{ proxySummary.total }}</el-tag>
        </div>
      </div>

      <div class="section-card trend-card">
        <div class="section-head compact">
          <h2>近 7 日请求</h2>
          <span class="model-pill">chat {{ modelSummary.chat }} · image {{ modelSummary.image }}</span>
        </div>
        <div class="bars">
          <div v-for="d in dailyRows" :key="d.day" class="bar-row">
            <span>{{ dayLabel(d.day) }}</span>
            <div class="bar-track"><i :style="{ width: barWidth(d.requests) }" /></div>
            <b>{{ d.requests }}</b>
          </div>
        </div>
      </div>
    </div>

    <!-- Bottom grid -->
    <div class="bottom-grid">
      <div class="section-card">
        <div class="section-head">
          <h2>开放模型</h2>
          <el-button link type="primary" @click="go('/admin/models')">管理 →</el-button>
        </div>
        <el-table :data="models" size="small" stripe empty-text="暂无模型" class="dash-table">
          <el-table-column prop="slug" label="model" min-width="160">
            <template #default="{ row }"><code>{{ row.slug }}</code></template>
          </el-table-column>
          <el-table-column label="类型" width="90" align="center">
            <template #default="{ row }">
              <el-tag size="small" :type="row.type === 'image' ? 'warning' : 'primary'" disable-transitions round>
                {{ row.type === 'image' ? '生图' : '对话' }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column prop="description" label="说明" show-overflow-tooltip />
        </el-table>
      </div>
      <div class="section-card">
        <div class="section-head">
          <h2>最近图片任务</h2>
          <el-button link type="primary" @click="go('/personal/usage')">更多 →</el-button>
        </div>
        <el-table :data="tasks" size="small" stripe empty-text="暂无任务" class="dash-table">
          <el-table-column prop="task_id" label="任务 ID" min-width="180" show-overflow-tooltip>
            <template #default="{ row }"><code class="mono-sm">{{ row.task_id }}</code></template>
          </el-table-column>
          <el-table-column prop="status" label="状态" width="90" align="center">
            <template #default="{ row }">
              <el-tag size="small" round disable-transitions
                :type="taskType(row.status)">
                {{ row.status === 'success' ? '成功' : row.status === 'failed' ? '失败' : row.status }}
              </el-tag>
            </template>
          </el-table-column>
          <el-table-column label="时间" width="170">
            <template #default="{ row }"><span class="time-text">{{ fmtTime(row.created_at) }}</span></template>
          </el-table-column>
        </el-table>
      </div>
    </div>
  </div>
</template>

<style scoped>
.dash { padding: 0; }

/* ---- Hero ---- */
.hero {
  position: relative;
  overflow: hidden;
  border-radius: 0 0 16px 16px;
  margin-bottom: 20px;
}
.hero-bg {
  position: absolute; inset: 0;
  background: linear-gradient(135deg, #409EFF 0%, #2b7de9 50%, #67C23A 100%);
  opacity: 1;
}
.hero-inner {
  position: relative;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 24px;
  padding: 36px 32px;
}
.hero h1 {
  margin: 0 0 10px;
  font-size: 26px;
  font-weight: 700;
  color: #fff;
}
.hero p {
  margin: 0;
  color: rgba(255,255,255,0.82);
  font-size: 14px;
  line-height: 1.6;
  max-width: 600px;
}
.hero code {
  background: rgba(255,255,255,0.18);
  color: #fff;
  padding: 1px 6px;
  border-radius: 4px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 13px;
}
.hero-actions { display: flex; gap: 10px; flex-shrink: 0; }
.hero-actions .el-button--primary {
  --el-button-bg-color: #fff;
  --el-button-text-color: #409EFF;
  --el-button-border-color: #fff;
  --el-button-hover-bg-color: rgba(255,255,255,0.9);
  --el-button-hover-text-color: #409EFF;
  --el-button-hover-border-color: rgba(255,255,255,0.9);
  font-weight: 600;
}
.hero-actions .el-button:not(.el-button--primary) {
  --el-button-bg-color: transparent;
  --el-button-text-color: #fff;
  --el-button-border-color: rgba(255,255,255,0.5);
  --el-button-hover-bg-color: rgba(255,255,255,0.12);
  --el-button-hover-text-color: #fff;
  --el-button-hover-border-color: rgba(255,255,255,0.8);
}

/* ---- KPI Row ---- */
.kpi-row {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 16px;
  padding: 0 20px;
  margin-top: -28px;
  position: relative;
  z-index: 1;
}
.kpi-card {
  background: var(--el-bg-color);
  border-radius: 12px;
  padding: 18px 20px;
  box-shadow: 0 2px 12px rgba(0,0,0,0.06);
  border-top: 3px solid transparent;
  transition: transform .2s, box-shadow .2s;
}
.kpi-card:hover {
  transform: translateY(-2px);
  box-shadow: 0 6px 20px rgba(0,0,0,0.08);
}
.kpi-blue  { border-top-color: #409EFF; }
.kpi-green { border-top-color: #67C23A; }
.kpi-orange{ border-top-color: #E6A23C; }
.kpi-purple{ border-top-color: #9B59B6; }

.kpi-top {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 10px;
}
.kpi-label {
  font-size: 13px;
  color: var(--el-text-color-secondary);
}
.kpi-dot {
  width: 36px; height: 36px;
  border-radius: 10px;
  display: flex;
  align-items: center;
  justify-content: center;
}
.kpi-blue  .kpi-dot { background: rgba(64,158,255,0.1); color: #409EFF; }
.kpi-green .kpi-dot { background: rgba(103,194,58,0.1); color: #67C23A; }
.kpi-orange .kpi-dot { background: rgba(230,162,60,0.1); color: #E6A23C; }
.kpi-purple .kpi-dot { background: rgba(155,89,182,0.1); color: #9B59B6; }

.kpi-val {
  font-size: 30px;
  font-weight: 700;
  line-height: 1;
  letter-spacing: -1px;
  color: var(--el-text-color-primary);
}

/* ---- Ops Overview ---- */
.ops-grid {
  display: grid;
  grid-template-columns: 1fr 1fr 1.4fr;
  gap: 16px;
  padding: 16px 20px 0;
}
.ops-card,
.trend-card {
  min-height: 142px;
}
.section-head.compact { margin-bottom: 10px; }
.ops-main {
  display: flex;
  align-items: flex-end;
  gap: 10px;
  flex-wrap: wrap;
}
.ops-num {
  font-size: 28px;
  font-weight: 700;
  color: var(--el-text-color-primary);
  line-height: 1.1;
}
.ops-label,
.model-pill {
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.bars {
  display: grid;
  gap: 8px;
}
.bar-row {
  display: grid;
  grid-template-columns: 42px minmax(0, 1fr) 36px;
  gap: 8px;
  align-items: center;
  font-size: 12px;
  color: var(--el-text-color-secondary);
}
.bar-track {
  height: 8px;
  border-radius: 999px;
  background: var(--el-fill-color-light);
  overflow: hidden;
}
.bar-track i {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--el-color-primary-light-5);
}
.bar-row b {
  text-align: right;
  color: var(--el-text-color-primary);
  font-weight: 600;
}

/* ---- Bottom Grid ---- */
.bottom-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  padding: 16px 20px 20px;
}
.section-card {
  background: var(--el-bg-color);
  border-radius: 12px;
  padding: 20px 22px;
  box-shadow: 0 1px 4px rgba(0,21,41,0.05);
}
.section-head {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 14px;
}
.section-head h2 {
  font-size: 16px;
  font-weight: 600;
  margin: 0;
  color: var(--el-text-color-primary);
}

code {
  background: var(--el-fill-color-light, #f2f3f5);
  padding: 2px 7px;
  border-radius: 4px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
}
.mono-sm { font-size: 11px; }
.time-text { font-size: 13px; color: var(--el-text-color-secondary); }

/* dark mode adjustments */
:root.dark .hero-bg,
html.dark .hero-bg {
  opacity: 0.85;
}
html.dark .kpi-card {
  box-shadow: 0 2px 12px rgba(0,0,0,0.25);
}
html.dark .kpi-card:hover {
  box-shadow: 0 6px 20px rgba(0,0,0,0.35);
}

@media (max-width: 960px) {
  .hero-inner { flex-direction: column; align-items: flex-start; padding: 28px 20px; }
  .kpi-row { grid-template-columns: repeat(2, minmax(0, 1fr)); margin-top: -20px; }
  .ops-grid, .bottom-grid { grid-template-columns: 1fr; }
}
@media (max-width: 560px) {
  .kpi-row { grid-template-columns: 1fr; }
}
</style>
