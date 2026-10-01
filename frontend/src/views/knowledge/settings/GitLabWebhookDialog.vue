<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import {
  absoluteGitLabWebhookUrl,
  getGitLabWebhook,
  testGitLabWebhook,
  updateGitLabWebhook,
  type DataSource,
  type GitLabWebhookStatus,
  type GitLabWebhookTestResult,
} from '@/api/datasource'

const props = defineProps<{ dataSource: DataSource | null }>()
const visible = defineModel<boolean>('visible', { default: false })
const { t } = useI18n()

const status = ref<GitLabWebhookStatus | null>(null)
const enabled = ref(false)
const secret = ref('')
const clearSecret = ref(false)
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const testResult = ref<GitLabWebhookTestResult | null>(null)

const callbackUrl = computed(() => status.value?.callback_path
  ? absoluteGitLabWebhookUrl(status.value.callback_path)
  : '')

function unwrap<T>(response: any): T {
  return (response?.data ?? response) as T
}

async function load() {
  if (!props.dataSource) return
  loading.value = true
  testResult.value = null
  clearSecret.value = false
  try {
    status.value = unwrap<GitLabWebhookStatus>(await getGitLabWebhook(props.dataSource.id))
    enabled.value = status.value.enabled
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('datasource.gitlabWebhook.loadFailed'))
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!props.dataSource) return
  if (enabled.value && !status.value?.configured && !secret.value) {
    MessagePlugin.warning(t('datasource.gitlabWebhook.secretRequired'))
    return
  }
  saving.value = true
  try {
    const update: { enabled: boolean; secret?: string; clear_secret?: boolean } = { enabled: clearSecret.value ? false : enabled.value }
    if (secret.value) update.secret = secret.value
    if (clearSecret.value) update.clear_secret = true
    status.value = unwrap<GitLabWebhookStatus>(await updateGitLabWebhook(props.dataSource.id, update))
    enabled.value = status.value.enabled
    secret.value = ''
    clearSecret.value = false
    MessagePlugin.success(t('datasource.gitlabWebhook.saved'))
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('datasource.gitlabWebhook.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function runTest() {
  if (!props.dataSource) return
  testing.value = true
  try {
    testResult.value = unwrap<GitLabWebhookTestResult>(await testGitLabWebhook(props.dataSource.id))
    status.value = testResult.value.webhook
    enabled.value = status.value.enabled
  } catch (error: any) {
    MessagePlugin.error(error?.message || t('datasource.gitlabWebhook.testFailed'))
  } finally {
    testing.value = false
  }
}

async function copyCallbackUrl() {
  if (!callbackUrl.value) return
  try {
    await navigator.clipboard.writeText(callbackUrl.value)
    MessagePlugin.success(t('datasource.gitlabWebhook.copied'))
  } catch {
    MessagePlugin.error(t('datasource.gitlabWebhook.copyFailed'))
  }
}

function clearSavedSecret() {
  clearSecret.value = !clearSecret.value
  if (clearSecret.value) {
    enabled.value = false
    secret.value = ''
  }
}

watch([visible, () => props.dataSource?.id], ([open]) => {
  if (open) void load()
})
</script>

<template>
  <t-dialog
    v-model:visible="visible"
    :header="t('datasource.gitlabWebhook.title')"
    :footer="false"
    width="680px"
    destroy-on-close
  >
    <t-loading :loading="loading" size="small">
      <div v-if="dataSource" class="gitlab-webhook">
        <p class="gitlab-webhook__intro">{{ t('datasource.gitlabWebhook.intro') }}</p>

        <div class="gitlab-webhook__field">
          <label>{{ t('datasource.gitlabWebhook.callbackUrl') }}</label>
          <div class="gitlab-webhook__url-row">
            <code>{{ callbackUrl }}</code>
            <t-button variant="outline" size="small" :disabled="!callbackUrl" @click="copyCallbackUrl">
              {{ t('datasource.gitlabWebhook.copy') }}
            </t-button>
          </div>
        </div>

        <div class="gitlab-webhook__field">
          <label>{{ t('datasource.gitlabWebhook.secret') }}</label>
          <t-input
            v-model="secret"
            type="password"
            autocomplete="new-password"
            :disabled="clearSecret"
            :placeholder="status?.configured ? t('datasource.gitlabWebhook.secretKeep') : t('datasource.gitlabWebhook.secretPlaceholder')"
          />
          <small>{{ t('datasource.gitlabWebhook.secretHint') }}</small>
          <t-button
            v-if="status?.configured"
            variant="text"
            theme="danger"
            size="small"
            @click="clearSavedSecret"
          >
            {{ clearSecret ? t('datasource.gitlabWebhook.cancelClear') : t('datasource.gitlabWebhook.clearSecret') }}
          </t-button>
        </div>

        <label class="gitlab-webhook__toggle">
          <t-switch v-model="enabled" :disabled="clearSecret" />
          <span>{{ t('datasource.gitlabWebhook.enabled') }}</span>
        </label>

        <div class="gitlab-webhook__instructions">
          <strong>{{ t('datasource.gitlabWebhook.manualSetupTitle') }}</strong>
          <p>{{ t('datasource.gitlabWebhook.manualSetup') }}</p>
        </div>

        <div class="gitlab-webhook__receipt">
          <strong>{{ t('datasource.gitlabWebhook.lastReceipt') }}</strong>
          <span v-if="status?.last_received_at">
            {{ new Date(status.last_received_at).toLocaleString() }}
            <template v-if="status.last_event_id"> · {{ status.last_event_id }}</template>
          </span>
          <span v-else>{{ t('datasource.gitlabWebhook.unverified') }}</span>
        </div>

        <div v-if="testResult" class="gitlab-webhook__test" :class="`gitlab-webhook__test--${testResult.gitlab_access_status}`">
          <div>
            <strong>{{ t('datasource.gitlabWebhook.gitlabAccess') }}:</strong>
            {{ testResult.gitlab_access_status === 'connected' ? t('datasource.gitlabWebhook.connected') : t('datasource.gitlabWebhook.failed') }}
            <span> · {{ testResult.project_id }} / {{ testResult.branch }}</span>
            <code v-if="testResult.current_commit_sha">{{ testResult.current_commit_sha }}</code>
            <p v-if="testResult.gitlab_access_error">{{ testResult.gitlab_access_error }}</p>
          </div>
          <div>
            <strong>{{ t('datasource.gitlabWebhook.inbound') }}:</strong>
            {{ testResult.inbound_status === 'verified' ? t('datasource.gitlabWebhook.verified') : t('datasource.gitlabWebhook.unverified') }}
          </div>
          <div>{{ t('datasource.gitlabWebhook.reconciliation') }}: {{ testResult.sync_schedule }}</div>
        </div>

        <div class="gitlab-webhook__actions">
          <t-button variant="outline" :loading="testing" @click="runTest">
            {{ t('datasource.gitlabWebhook.test') }}
          </t-button>
          <span class="gitlab-webhook__spacer" />
          <t-button variant="outline" @click="visible = false">{{ t('common.cancel') }}</t-button>
          <t-button theme="primary" :loading="saving" @click="save">{{ t('common.save') }}</t-button>
        </div>
      </div>
    </t-loading>
  </t-dialog>
</template>

<style scoped lang="less">
.gitlab-webhook {
  display: grid;
  gap: 16px;
  color: var(--td-text-color-primary);
  font-size: 13px;

  &__intro,
  &__instructions p,
  &__test p {
    margin: 0;
    line-height: 1.55;
    color: var(--td-text-color-secondary);
  }

  &__field {
    display: grid;
    gap: 6px;

    label { font-weight: 600; }
    small { color: var(--td-text-color-secondary); line-height: 1.45; }
  }

  &__url-row {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;

    code {
      flex: 1;
      min-width: 0;
      overflow-wrap: anywhere;
      padding: 8px 10px;
      border-radius: 6px;
      background: var(--td-bg-color-secondarycontainer);
    }
  }

  &__toggle { display: flex; align-items: center; gap: 8px; }

  &__instructions,
  &__receipt,
  &__test {
    display: grid;
    gap: 6px;
    padding: 12px;
    border-radius: 8px;
    background: var(--td-bg-color-secondarycontainer);
    line-height: 1.5;
  }

  &__receipt span { color: var(--td-text-color-secondary); overflow-wrap: anywhere; }
  &__test code { display: block; margin-top: 4px; overflow-wrap: anywhere; }
  &__test--connected { border-inline-start: 3px solid var(--td-success-color); }
  &__test--failed { border-inline-start: 3px solid var(--td-error-color); }

  &__actions { display: flex; align-items: center; gap: 8px; margin-top: 4px; }
  &__spacer { flex: 1; }
}
</style>
