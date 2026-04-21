<script setup lang="ts">
import { useRouter } from 'vue-router'
const router = useRouter()
</script>
<template>
  <div class="err-page">
    <el-result icon="error" title="403" sub-title="你当前的角色没有访问此页面的权限。">
      <template #extra>
        <el-button @click="router.replace('/personal/dashboard')">回到个人中心</el-button>
      </template>
    </el-result>
  </div>
</template>
<style scoped>
.err-page{min-height:100vh;display:flex;align-items:center;justify-content:center;}
</style>
