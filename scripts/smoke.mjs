#!/usr/bin/env node
const base = (process.argv[2] || 'http://localhost:8080').replace(/\/$/, '')

async function call(path, init) {
  const res = await fetch(base + path, init)
  const text = await res.text()
  let body = null
  try { body = text ? JSON.parse(text) : null } catch { body = text }
  return { status: res.status, body }
}

function ok(msg) { console.log('✓', msg) }
function fail(msg) { console.error('✗', msg); process.exitCode = 1 }

const models = await call('/v1/models')
if (models.status >= 200 && models.status < 300) ok('/v1/models reachable')
else fail(`/v1/models failed: ${models.status}`)

const site = await call('/api/public/site-info')
if (site.status >= 200 && site.status < 300) ok('/api/public/site-info reachable')
else fail(`/api/public/site-info failed: ${site.status}`)

const adminModels = await call('/api/admin/models')
if (adminModels.status >= 200 && adminModels.status < 300) ok('/api/admin/models reachable')
else fail(`/api/admin/models failed: ${adminModels.status}`)
