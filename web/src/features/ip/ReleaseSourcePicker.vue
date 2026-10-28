<script setup lang="ts">
import { onBeforeUnmount, ref } from "vue";
import { api, APIError, message } from "../../api";

interface SourceGeneration {
  id: string;
  state: string;
  cancelled: boolean;
  frozen: { role: string; capability?: { mode: string; task_kind?: string } };
  output?: { text: string };
}
const props = defineProps<{
  project: string;
  branch: string;
  disabled: boolean;
}>();
const emit = defineEmits<{
  select: [{ id: string; text: string }];
  blocking: [boolean];
  expired: [];
}>();
const choices = ref<SourceGeneration[]>([]);
const selection = ref("");
const preview = ref<SourceGeneration | null>(null);
const busy = ref(false);
const error = ref("");
let disposed = false;
let sequence = 0;
function fail(e: unknown) {
  if (e instanceof APIError && e.status === 401) emit("expired");
  error.value = message(e);
}
async function load() {
  if (props.disabled || busy.value) return;
  busy.value = true;
  emit("blocking", true);
  error.value = "";
  try {
    const result = await api<{ items: SourceGeneration[] }>(
      `/projects/${props.project}/generations?branch=${props.branch}`,
    );
    if (!disposed)
      choices.value = result.items.filter(
        (g) =>
          g.state === "SUCCEEDED" &&
          !g.cancelled &&
          g.frozen.capability?.mode !== "EVALUATION",
      );
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    busy.value = false;
    emit("blocking", false);
  }
}
async function inspect() {
  const seq = ++sequence;
  preview.value = null;
  if (!selection.value || props.disabled || busy.value) return;
  busy.value = true;
  emit("blocking", true);
  error.value = "";
  try {
    const result = await api<SourceGeneration>(
      `/projects/${props.project}/generations/${selection.value}`,
    );
    if (
      !disposed &&
      seq === sequence &&
      result.state === "SUCCEEDED" &&
      result.output
    )
      preview.value = result;
  } catch (e) {
    if (!disposed && seq === sequence) fail(e);
  } finally {
    busy.value = false;
    emit("blocking", false);
  }
}
function select() {
  if (props.disabled || busy.value || !preview.value?.output) return;
  emit("select", { id: preview.value.id, text: preview.value.output.text });
}
onBeforeUnmount(() => {
  disposed = true;
  sequence++;
  emit("blocking", false);
});
</script>

<template>
  <details class="source-picker">
    <summary>从已有 AI 结果建立资料来源</summary>
    <p>
      选择会先保存绑定当前资料的来源候选，再放入编辑区。之后手改或刷新都保留生成血缘；这里不会发起模型请求。
    </p>
    <button
      type="button"
      class="quiet"
      :disabled="disabled || busy"
      @click="load"
    >
      加载已完成的生成结果
    </button>
    <label
      >生成来源<select
        v-model="selection"
        aria-label="生成来源"
        :disabled="disabled || busy"
        @change="inspect"
      >
        <option value="">选择一个已完成任务</option>
        <option v-for="g in choices" :key="g.id" :value="g.id">
          {{ g.frozen.capability?.task_kind || g.frozen.role }} ·
          {{ g.id.slice(0, 8) }} ·
          {{
            g.frozen.capability?.mode === "QUALIFIED"
              ? "有固定资格"
              : "未取得生产资格"
          }}
        </option>
      </select></label
    >
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <template v-if="preview?.output">
      <pre>{{ preview.output.text }}</pre>
      <p v-if="preview.frozen.capability?.mode !== 'QUALIFIED'">
        该结果可以保存为待复核候选，目前不能作为获准结果发布。
      </p>
      <button type="button" :disabled="disabled || busy" @click="select">
        保存来源候选并载入编辑区
      </button>
    </template>
  </details>
</template>

<style scoped>
.source-picker {
  margin: 1rem 0;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  max-height: 18rem;
  overflow: auto;
}
</style>
