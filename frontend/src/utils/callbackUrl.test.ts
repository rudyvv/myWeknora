import assert from 'node:assert/strict'
import test from 'node:test'
import { buildApiCallbackUrl } from './callbackUrl.ts'

test('callback URL keeps the API mount prefix and current browser origin', () => {
  assert.equal(
    buildApiCallbackUrl('/api/v1/gitlab/webhooks/push', '/app/weknora/', 'https://weknora.example'),
    'https://weknora.example/app/weknora/api/v1/gitlab/webhooks/push',
  )
})

test('callback URL supports an absolute configured API base', () => {
  assert.equal(
    buildApiCallbackUrl('api/v1/gitlab/webhooks/push', 'https://api.example/proxy/prefix', 'https://ui.example'),
    'https://api.example/proxy/prefix/api/v1/gitlab/webhooks/push',
  )
})
