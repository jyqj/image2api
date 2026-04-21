<script setup lang="ts">
const base = typeof window === 'undefined' ? 'http://localhost:8080' : window.location.origin
const chatCurl = `curl ${base}/v1/chat/completions \\
  -H 'Content-Type: application/json' \\
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream":true}'`
const imageCurl = `curl ${base}/v1/images/generations \\
  -H 'Content-Type: application/json' \\
  -d '{"model":"gpt-image-2","prompt":"a cat reading a book","n":1,"size":"1024x1024"}'`
</script>

<template>
  <div class="page-container">
    <div class="card-block">
      <h2 class="page-title">接口文档</h2>
      <p class="sub">本版本是本地自用中转，默认不需要下游身份凭证。把 OpenAI SDK 的 baseURL 指向 <code>{{ base }}/v1</code> 即可。</p>
    </div>

    <div class="card-block">
      <h3>对话接口</h3>
      <p><code>POST /v1/chat/completions</code>，兼容 OpenAI Chat Completions，支持流式输出。</p>
      <pre>{{ chatCurl }}</pre>
    </div>

    <div class="card-block">
      <h3>图片接口</h3>
      <p><code>POST /v1/images/generations</code>，同步返回图片 URL；图片代理地址由本服务签名生成。</p>
      <pre>{{ imageCurl }}</pre>
    </div>

    <div class="card-block">
      <h3>模型列表</h3>
      <p><code>GET /v1/models</code> 会返回已开放的本地模型映射。</p>
      <pre>curl {{ base }}/v1/models</pre>
    </div>
  </div>
</template>

<style scoped>
.sub { color:var(--el-text-color-secondary); }
.card-block { margin-bottom:16px; }
code { background:#f2f3f5; padding:1px 6px; border-radius:4px; }
pre { background:#111827; color:#e5e7eb; border-radius:8px; padding:14px; overflow:auto; line-height:1.5; }
</style>
