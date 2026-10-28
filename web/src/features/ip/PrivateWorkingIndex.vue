<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, APIError, message, type Actor, type Project } from "../../api";
import type { Branch } from "./types";
import IndexJobsPanel from "./IndexJobsPanel.vue";
const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: Branch;
  blocked: boolean;
  revision: number;
}>();
const emit = defineEmits<{ expired: []; queued: [] }>();
interface Status {
  ready: boolean;
  chunks: number;
  manifest: {
    sources: unknown[];
    omissions: { ref: { kind: string; id: string }; reason: string }[];
  };
}
const status = ref<Status | null>(null);
const error = ref("");
let sequence = 0;
let disposed = false;
async function refresh() {
  const current = ++sequence;
  try {
    const value = await api<Status>(
      `/projects/${props.project.id}/branches/${props.branch.id}/knowledge/private-index`,
    );
    if (!disposed && current === sequence) {
      status.value = value;
      error.value = "";
    }
  } catch (e) {
    if (!disposed && current === sequence) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  }
}
watch(
  () => [props.branch.head, props.revision],
  () => void refresh(),
);
onMounted(() => void refresh());
onBeforeUnmount(() => {
  disposed = true;
  sequence++;
});
</script>
<template>
  <section data-testid="private-working-index">
    <p class="muted">
      只检索我的私人草稿及当前有权查看的待确认、已采用未发布候选。版本变化后须重建；这些片段不会成为正史。上方勾选的完整输入仍会完整保留。
    </p>
    <p v-if="status">
      {{
        status.ready
          ? `当前私人索引已就绪 · ${status.chunks} 个片段`
          : "当前版本尚未建立私人索引，请构建后再检索。"
      }}
      · {{ status.manifest.sources.length }} 个可用文字单元
    </p>
    <p
      v-for="item in status?.manifest.omissions ?? []"
      :key="item.ref.kind + item.ref.id"
      class="muted"
    >
      未纳入 {{ item.ref.kind }} · {{ item.ref.id }}：{{ item.reason }}
    </p>
    <button type="button" class="quiet" :disabled="blocked" @click="refresh">
      检查私人索引状态
    </button>
    <IndexJobsPanel
      :project="project"
      :actor="actor"
      :branch="branch"
      :blocked="blocked"
      :revision="revision"
      kind="working"
      label="构建 / 更新我的工作区索引"
      @expired="emit('expired')"
      @queued="
        emit('queued');
        refresh();
      "
      @ready="refresh"
    />
    <p v-if="error" role="alert" class="error">{{ error }}</p>
  </section>
</template>
