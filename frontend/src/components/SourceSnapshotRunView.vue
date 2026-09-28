<script setup lang="ts">
import { computed, ref } from 'vue'
import type { SyncResultDetail } from '@/api/datasource'
import SourceCodeView from './SourceCodeView.vue'

const props = defineProps<{ result: NonNullable<SyncResultDetail['source']> }>()
const filter = ref('')
const page = ref(0)
const fileID = ref('')
const versionID = ref('')
const pageSize = 50
const labels: Record<string, string> = { fetching: '正在获取固定提交', parsing: '正在解析源码', indexing: '正在建立双索引', ready: '准备发布', published: '已发布', failed: '发布失败' }
const filtered = computed(() => props.result.members.filter(member => member.path.toLowerCase().includes(filter.value.toLowerCase())))
const members = computed(() => filtered.value.slice(page.value * pageSize, (page.value + 1) * pageSize))
</script>

<template>
  <section class="source-run" aria-label="源码同步详情">
    <strong role="status">{{ labels[result.snapshot.state] || result.snapshot.state }}</strong>
    <p class="commit">提交：{{ result.snapshot.commit_sha || '正在解析分支' }}</p>
    <p>{{ result.snapshot.manifest_complete ? '成员清单完整' : '正在扫描成员清单' }} · {{ result.snapshot.member_count }} 个成员</p>
    <p>解析文件 {{ result.snapshot.file_count }} · 源码块 {{ result.snapshot.chunk_count }}</p>
    <p v-if="result.snapshot.error" role="alert">{{ result.snapshot.error }}</p>
    <input v-model="filter" aria-label="筛选源码成员" placeholder="筛选文件路径" @input="page = 0" />
    <ul>
      <li v-for="member in members" :key="member.path">
        <button v-if="result.snapshot.state === 'published' && member.status === 'parsed'" type="button" @click="fileID = member.source_file_id; versionID = member.file_version_id">{{ member.path }}</button>
        <span v-else>{{ member.path }}</span>
        <span> · {{ member.status }}{{ member.reason ? ` · ${member.reason}` : '' }}</span>
        <small v-if="member.file_version_id">版本 {{ member.file_version_id }}</small>
      </li>
    </ul>
    <nav v-if="filtered.length > pageSize" aria-label="源码清单分页">
      <button type="button" :disabled="page === 0" @click="page--">上一页</button>
      <span>{{ page + 1 }} / {{ Math.ceil(filtered.length / pageSize) }}</span>
      <button type="button" :disabled="(page + 1) * pageSize >= filtered.length" @click="page++">下一页</button>
    </nav>
    <SourceCodeView v-if="fileID && result.snapshot.state === 'published'" :knowledge-id="fileID" :file-version-id="versionID" />
  </section>
</template>

<style scoped>
.source-run { margin: 12px 0; padding: 12px; border: 1px solid var(--td-component-border); border-radius: 6px; overflow-wrap: anywhere; }
p { margin: 6px 0; }
.commit, small { font-family: monospace; }
ul { padding-left: 16px; max-height: 260px; overflow: auto; }
li { margin: 6px 0; }
small { display: block; color: var(--td-text-color-placeholder); }
button, input { padding: 4px 8px; border: 1px solid var(--td-component-border); border-radius: 4px; color: inherit; background: var(--td-bg-color-container); }
button { cursor: pointer; }
nav { display: flex; justify-content: space-between; margin: 8px 0; }
</style>
