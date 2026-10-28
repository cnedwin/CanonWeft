<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import type { CapabilityVersion } from "./capabilityTypes";
import ExpiredTaskHistory from "../../components/ExpiredTaskHistory.vue";
const props = defineProps<{
  actor: Actor;
  project: Project;
  capability: CapabilityVersion;
  revoked: boolean;
}>();
const emit = defineEmits<{
  expired: [];
  blocking: [boolean];
  busy: [boolean];
}>();
type Trial = {
  id: string;
  retention?: { state: "EXPIRED"; expired_at: string };
  state: string;
  reason: string;
  stale: boolean;
  can_cancel: boolean;
  elapsed_ms: number;
  frozen: {
    version_id: string;
    package_hash: string;
    image_hash: string;
    input_hash: string;
  };
  output?: {
    valid: boolean;
    stdout: string;
    stderr: string;
    receipt: { reason: string; exit_code: number };
  };
};
const root = `/projects/${props.project.id}/tool-trials`;
const input = ref("{}");
const baseline = ref("{}");
const operation = ref(newOperationID());
const jobs = ref<Trial[]>([]);
const selected = ref<Trial | null>(null);
const cursor = ref("");
const busy = ref(false);
const configured = ref(false);
const error = ref("");
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const dirty = computed(() => input.value !== baseline.value);
const currentJobs = computed(() =>
  jobs.value.filter(
    (j) => !j.retention && j.frozen.version_id === props.capability.id,
  ),
);
let disposed = false;
let polling = false;
let readSequence = 0;
let timer: ReturnType<typeof setInterval> | undefined;
watch(input, () => {
  operation.value = newOperationID();
});
watch([dirty, busy], () => emit("blocking", dirty.value || busy.value), {
  immediate: true,
});
watch(busy, () => emit("busy", busy.value), { immediate: true });
watch(
  () => props.revoked,
  (revoked) => {
    if (!revoked) return;
    readSequence++;
    if (selected.value)
      selected.value = { ...selected.value, stale: true, output: undefined };
  },
);
const labels: Record<string, string> = {
  QUEUED: "等待执行",
  RUNNING: "隔离执行中",
  SUCCEEDED: "试运行通过",
  FAILED: "试运行未通过",
  BLOCKED: "已阻止",
  CANCELLED: "已取消",
};
async function perform(work: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await work();
  } catch (e) {
    if (!disposed) {
      if (e instanceof APIError && e.status === 401) emit("expired");
      error.value = message(e);
    }
  } finally {
    busy.value = false;
  }
}
async function refresh(more = false) {
  const selectedID = selected.value?.id;
  const sequence = readSequence;
  const result = await api<{
    items: Trial[];
    next_cursor: string;
    runtime_configured: boolean;
  }>(`${root}?after=${more ? cursor.value : ""}`);
  if (disposed) return;
  jobs.value = more ? [...jobs.value, ...result.items] : result.items;
  if (
    result.items.some((job) => job.id === selected.value?.id && job.retention)
  ) {
    ++readSequence;
    selected.value = null;
  }
  cursor.value = result.next_cursor;
  configured.value = result.runtime_configured;
  if (
    selectedID &&
    selected.value?.id === selectedID &&
    sequence === readSequence
  )
    await read(selectedID);
}
async function read(id: string) {
  const sequence = ++readSequence;
  try {
    const result = await api<Trial>(`${root}/${id}`);
    if (disposed || sequence !== readSequence) return;
    selected.value = props.revoked
      ? { ...result, stale: true, output: undefined }
      : result;
    jobs.value = jobs.value.map((j) => (j.id === result.id ? result : j));
  } catch (e) {
    if (!disposed && sequence === readSequence && selected.value) {
      if (e instanceof APIError && e.status === 410) selected.value = null;
      else selected.value = { ...selected.value, output: undefined };
    }
    throw e;
  }
}
async function submit() {
  if (props.revoked) return;
  await perform(async () => {
    const job = await api<Trial>(root, "POST", {
      operation_id: operation.value,
      version_id: props.capability.id,
      input_text: input.value,
    });
    if (disposed) return;
    baseline.value = input.value;
    operation.value = newOperationID();
    selected.value = job;
    readSequence++;
    await refresh();
  });
}
async function cancel() {
  if (!selected.value) return;
  const id = selected.value.id;
  readSequence++;
  await perform(async () => {
    await api(`${root}/${id}/cancel`, "POST", {});
    await read(id);
  });
}
onMounted(() => {
  void perform(() => refresh());
  timer = setInterval(() => {
    if (disposed || polling || busy.value || !selected.value || props.revoked)
      return;
    polling = true;
    void read(selected.value.id)
      .catch((e) => {
        if (!disposed) {
          error.value = message(e);
          if (e instanceof APIError && e.status === 401) emit("expired");
        }
      })
      .finally(() => {
        polling = false;
      });
  }, 2000);
});
onBeforeUnmount(() => {
  disposed = true;
  if (timer) clearInterval(timer);
  emit("blocking", false);
  emit("busy", false);
});
</script>
<template>
  <section class="tool-trials" aria-label="工具隔离试运行">
    <h3>工具隔离试运行</h3>
    <p>
      使用已保存的工具版本和完整方法包，在本机隔离环境中运行。不能联网或改写项目资料。通过只记录试运行证据，正式使用仍须独立评测。
    </p>
    <p v-if="!configured" class="notice">
      任务服务或隔离环境尚未配置，暂不能开始试运行。
    </p>
    <p v-if="error" role="alert">{{ error }}</p>
    <form @submit.prevent="submit">
      <fieldset :disabled="busy || !canEdit || !configured || revoked">
        <label
          >工具输入（JSON）<textarea
            v-model="input"
            rows="7"
            required
            spellcheck="false"
          />
        </label>
        <details>
          <summary>查看此版本输入要求</summary>
          <pre>{{ capability.input_schema_text }}</pre>
        </details>
        <button>开始隔离试运行</button>
        <button
          type="button"
          class="quiet"
          :disabled="!dirty"
          @click="input = baseline"
        >
          放弃试运行输入修改
        </button>
      </fieldset>
    </form>
    <ExpiredTaskHistory scope="当前项目工具" :items="jobs" />
    <button class="quiet" :disabled="busy" @click="perform(() => refresh())">
      刷新试运行记录
    </button>
    <ul>
      <li v-for="job in currentJobs" :key="job.id">
        <button
          class="quiet"
          :disabled="busy"
          @click="perform(() => read(job.id))"
        >
          {{ labels[job.state] || job.state }} · {{ job.id }}
        </button>
      </li>
    </ul>
    <button
      v-if="cursor"
      :disabled="busy"
      @click="perform(() => refresh(true))"
    >
      加载更多试运行
    </button>
    <article v-if="selected" class="tool-result">
      <h4>{{ labels[selected.state] || selected.state }}</h4>
      <p>{{ selected.reason }}</p>
      <p v-if="selected.stale" role="alert">
        能力或来源已撤回或变更，此结果不可继续使用。
      </p>
      <button
        v-if="selected.can_cancel && canEdit"
        :disabled="busy"
        @click="cancel"
      >
        取消此次试运行
      </button>
      <template v-if="selected.output">
        <p>
          {{
            selected.output.valid
              ? "输出符合本版本契约"
              : "输出或执行未通过检查"
          }}
          · {{ selected.elapsed_ms }} 毫秒
        </p>
        <h4>工具输出</h4>
        <pre>{{ selected.output.stdout }}</pre>
        <details v-if="selected.output.stderr">
          <summary>执行诊断</summary>
          <pre>{{ selected.output.stderr }}</pre>
        </details>
      </template>
      <details>
        <summary>固定版本与输入凭证</summary>
        <pre>{{ selected.frozen }}</pre>
      </details>
    </article>
  </section>
</template>
<style scoped>
.tool-trials {
  margin-top: 1.5rem;
  border-top: 1px solid #ddd;
  padding-top: 1rem;
}
pre,
li {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
fieldset {
  min-width: 0;
}
.tool-result {
  margin-top: 1rem;
}
</style>
