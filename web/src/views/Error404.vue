<script setup lang="ts">
import { useRouter } from 'vue-router'
const router = useRouter()
</script>
<template>
  <div class="err-page">
    <el-result icon="warning" title="404" sub-title="页面不存在,或你没有访问它的权限。">
      <template #extra>
        <el-button type="primary" @click="router.replace('/')">返回首页</el-button>
      </template>
    </el-result>
  </div>
</template>
<style scoped>
.err-page{min-height:100vh;display:flex;align-items:center;justify-content:center;}
</style>
