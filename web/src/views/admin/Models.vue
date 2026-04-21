<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { ElMessage, ElMessageBox, type FormInstance, type FormRules } from 'element-plus'
import { Plus } from '@element-plus/icons-vue'
import * as statsApi from '@/api/stats'
import { ENABLE_CHAT_MODEL } from '@/config/feature'

const loading = ref(false)
const rows = ref<statsApi.Model[]>([])
const filterType = ref<string>(ENABLE_CHAT_MODEL ? '' : 'image')

async function load() {
  loading.value = true
  try {
    const d = await statsApi.listModels()
    rows.value = d.items || []
  } finally { loading.value = false }
}

const filteredRows = computed(() => filterType.value ? rows.value.filter((r) => r.type === filterType.value) : rows.value)

const dlgVisible = ref(false)
const dlgState = ref<'create' | 'edit'>('create')
const dlgLoading = ref(false)
const formRef = ref<FormInstance>()

const emptyForm = (): statsApi.ModelUpsert & { id: number } => ({
  id: 0,
  slug: '',
  type: ENABLE_CHAT_MODEL ? 'chat' : 'image',
  upstream_model_slug: '',
  description: '',
  enabled: true,
})
const form = reactive(emptyForm())

const rules: FormRules = {
  slug: [
    { required: true, message: '请输入 slug', trigger: 'blur' },
    { pattern: /^[A-Za-z][A-Za-z0-9._\-]{1,63}$/, message: '字母开头,2-64 位字母/数字/点/下划线/短横', trigger: 'blur' },
  ],
  type: [{ required: true, message: '请选择类型', trigger: 'change' }],
  upstream_model_slug: [{ required: true, message: '上游 slug 必填', trigger: 'blur' }],
}

function openCreate() {
  Object.assign(form, emptyForm())
  dlgState.value = 'create'
  dlgVisible.value = true
}
function openEdit(row: statsApi.Model) {
  Object.assign(form, {
    id: row.id,
    slug: row.slug,
    type: row.type as 'chat' | 'image',
    upstream_model_slug: row.upstream_model_slug,
    description: row.description,
    enabled: row.enabled,
  })
  dlgState.value = 'edit'
  dlgVisible.value = true
}

async function submit() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  const payload: statsApi.ModelUpsert = {
    slug: form.slug,
    type: form.type,
    upstream_model_slug: form.upstream_model_slug,
    description: form.description,
    enabled: form.enabled,
  }
  dlgLoading.value = true
  try {
    if (dlgState.value === 'create') {
      await statsApi.createModel(payload)
      ElMessage.success('新增成功')
    } else {
      await statsApi.updateModel(form.id, payload)
      ElMessage.success('保存成功')
    }
    dlgVisible.value = false
    await load()
  } finally { dlgLoading.value = false }
}

async function onToggleEnabled(row: statsApi.Model) {
  await statsApi.setModelEnabled(row.id, !row.enabled)
  row.enabled = !row.enabled
  ElMessage.success(row.enabled ? '已开放调用' : '已暂停调用')
}

async function onDelete(row: statsApi.Model) {
  const ok = await ElMessageBox.confirm(`确定删除模型映射 "${row.slug}" 吗?已发生的用量日志不会被清除。`, '删除确认', {
    type: 'warning', confirmButtonText: '删除', cancelButtonText: '取消',
  }).catch(() => null)
  if (!ok) return
  await statsApi.deleteModel(row.id)
  ElMessage.success('已删除')
  await load()
}

onMounted(load)
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <div class="flex-between" style="margin-bottom:12px">
        <div>
          <h2 class="page-title" style="margin:0">模型映射</h2>
          <div class="page-sub">定义对外 model slug 与 chatgpt.com 上游模型名的映射。这里不配置计量金额，只控制是否开放调用。</div>
        </div>
        <div class="flex-wrap-gap">
          <el-radio-group v-model="filterType" size="small">
            <el-radio-button v-if="ENABLE_CHAT_MODEL" label="">全部</el-radio-button>
            <el-radio-button v-if="ENABLE_CHAT_MODEL" label="chat">对话</el-radio-button>
            <el-radio-button label="image">生图</el-radio-button>
          </el-radio-group>
          <el-button type="primary" :icon="Plus" @click="openCreate">新增映射</el-button>
        </div>
      </div>

      <el-table v-loading="loading" :data="filteredRows" stripe>
        <el-table-column prop="id" label="ID" width="70" />
        <el-table-column prop="slug" label="对外 slug" min-width="170"><template #default="{ row }"><code>{{ row.slug }}</code></template></el-table-column>
        <el-table-column label="类型" width="90"><template #default="{ row }"><el-tag :type="row.type === 'image' ? 'warning' : 'primary'" size="small">{{ row.type === 'image' ? '生图' : '对话' }}</el-tag></template></el-table-column>
        <el-table-column prop="upstream_model_slug" label="上游 slug" min-width="180" show-overflow-tooltip><template #default="{ row }"><code>{{ row.upstream_model_slug }}</code></template></el-table-column>
        <el-table-column prop="description" label="说明" min-width="220" show-overflow-tooltip />
        <el-table-column label="状态" width="110"><template #default="{ row }"><el-switch :model-value="row.enabled" inline-prompt active-text="开" inactive-text="停" @change="onToggleEnabled(row)" /></template></el-table-column>
        <el-table-column label="操作" width="140" fixed="right"><template #default="{ row }"><el-button size="small" link type="primary" @click="openEdit(row)">编辑</el-button><el-button size="small" link type="danger" @click="onDelete(row)">删除</el-button></template></el-table-column>
      </el-table>
    </div>

    <el-dialog v-model="dlgVisible" :title="dlgState === 'create' ? '新增模型映射' : `编辑映射 · ${form.slug}`" width="640px" destroy-on-close>
      <el-form ref="formRef" :model="form" :rules="rules" label-width="130px">
        <el-form-item label="对外 slug" prop="slug">
          <el-input v-model="form.slug" :disabled="dlgState === 'edit'" placeholder="例如 gpt-5 / gpt-image-2" />
          <div class="hint">创建后不可修改；客户端请求中的 model 字段使用该值。</div>
        </el-form-item>
        <el-form-item label="类型" prop="type">
          <el-radio-group v-model="form.type">
            <el-radio-button v-if="ENABLE_CHAT_MODEL" label="chat">对话 chat</el-radio-button>
            <el-radio-button label="image">生图 image</el-radio-button>
          </el-radio-group>
        </el-form-item>
        <el-form-item label="上游 slug" prop="upstream_model_slug"><el-input v-model="form.upstream_model_slug" placeholder="chatgpt.com 实际模型名" /></el-form-item>
        <el-form-item label="描述"><el-input v-model="form.description" maxlength="255" show-word-limit /></el-form-item>
        <el-form-item label="开放调用"><el-switch v-model="form.enabled" /></el-form-item>
      </el-form>
      <template #footer><el-button @click="dlgVisible = false">取消</el-button><el-button type="primary" :loading="dlgLoading" @click="submit">保存</el-button></template>
    </el-dialog>
  </div>
</template>

<style scoped>
code { background: #f2f3f5; padding: 1px 6px; border-radius: 4px; font-family: ui-monospace, Menlo, Consolas, monospace; font-size: 12px; }
:global(html.dark) code { background: #1d2026; }
.hint, .page-sub { font-size: 12px; color: var(--el-text-color-secondary); margin-top: 4px; line-height: 1.4; }
</style>
