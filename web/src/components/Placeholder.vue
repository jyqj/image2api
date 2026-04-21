<script setup lang="ts">
defineProps<{
  title: string
  subtitle?: string
  api?: string      // 提示后端 API 路径
}>()
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <el-empty :description="subtitle || '该模块的前端还在施工中'">
        <template #image>
          <el-icon :size="64" color="#c0c4cc"><Coffee /></el-icon>
        </template>
        <div class="title">{{ title }}</div>
        <div v-if="api" class="hint">
          后端接口:<code>{{ api }}</code>
        </div>
        <div class="hint">你可以通过 Postman / curl 直接调用,或在下个版本里等待 UI 完工。</div>
      </el-empty>
    </div>
  </div>
</template>

<style scoped lang="scss">
.title { font-size: 18px; font-weight: 600; margin-top: 4px; }
.hint { color: var(--el-text-color-secondary); margin-top: 6px; font-size: 13px; }
code {
  background: #f2f3f5;
  padding: 2px 6px;
  border-radius: 4px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
}
</style>
