<script setup lang="ts">
import { onUnmounted, ref } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";

const props = defineProps<{ actor: Actor }>();
const emit = defineEmits<{ expired: []; recovered: [project: Project] }>();
interface RecycleProject {
  id: string;
  name: string;
  revision: number;
  deleted_at?: string;
  recover_until?: string;
  checked_at: string;
  window: string;
}
const items = ref<RecycleProject[]>([]);
const next = ref("");
const busy = ref(false);
const loaded = ref(false);
const error = ref("");
const recovering = ref(false);
const selection = ref<RecycleProject | null>(null);
const copyName = ref("");
const recovered = ref<Project | null>(null);
let request: { name: string; revision: number; operation: string } | null =
  null;
function requestKey(item: RecycleProject) {
  return `project-recovery:${props.actor.workspace_id}:${props.actor.id}:${item.id}`;
}
function select(item: RecycleProject) {
  if (busy.value || recovering.value) return;
  selection.value = item;
  recovered.value = null;
  error.value = "";
  copyName.value = `${item.name}（恢复副本）`.slice(0, 120);
  request = null;
  try {
    const saved = JSON.parse(
      sessionStorage.getItem(requestKey(item)) || "null",
    );
    if (
      saved &&
      saved.revision === item.revision &&
      typeof saved.name === "string" &&
      typeof saved.operation === "string"
    ) {
      request = saved;
      copyName.value = saved.name;
    }
  } catch {
    /* The server still enforces idempotency if browser storage is unavailable. */
  }
}
async function recover() {
  const item = selection.value;
  const name = copyName.value.trim();
  if (!item || !name || recovering.value || busy.value) return;
  if (!request || request.name !== name || request.revision !== item.revision) {
    request = { name, revision: item.revision, operation: newOperationID() };
  }
  try {
    sessionStorage.setItem(requestKey(item), JSON.stringify(request));
  } catch {
    /* Keep the in-memory receipt for retry. */
  }
  recovering.value = true;
  error.value = "";
  try {
    const project = await api<Project>(`/projects/${item.id}/recover`, "POST", {
      name,
      confirm_name: item.name,
      revision: item.revision,
      operation_id: request.operation,
    });
    if (!active) return;
    recovered.value = project;
    selection.value = null;
    try {
      sessionStorage.removeItem(requestKey(item));
    } catch {
      /* A retained receipt is safe to retry. */
    }
  } catch (cause) {
    if (!active) return;
    error.value = message(cause);
    if (cause instanceof APIError && cause.status === 401) emit("expired");
  } finally {
    if (active) recovering.value = false;
  }
}
let active = true;
onUnmounted(() => {
  active = false;
});
function date(value?: string) {
  return value ? new Date(value).toLocaleString() : "无法确认";
}
function label(value: string) {
  return (
    (
      {
        WITHIN_WINDOW: "仍在七天期限内",
        EXPIRED: "已超过恢复期限",
        UNVERIFIED_CLOCK: "历史删除时间未经确认，无法确定恢复期限",
      } as Record<string, string>
    )[value] ?? "暂时无法确认期限"
  );
}
async function load(more = false) {
  if (busy.value || recovering.value) return;
  busy.value = true;
  error.value = "";
  try {
    const result = await api<{ items: RecycleProject[]; next_cursor: string }>(
      `/operations/recycle-bin${more ? `?after=${encodeURIComponent(next.value)}` : ""}`,
    );
    if (!active) return;
    items.value = more ? [...items.value, ...result.items] : result.items;
    next.value = result.next_cursor;
    loaded.value = true;
  } catch (cause) {
    if (!active) return;
    // A failed authority check must not leave old eligibility visible.
    items.value = [];
    next.value = "";
    loaded.value = false;
    selection.value = null;
    error.value = message(cause);
    if (cause instanceof APIError && cause.status === 401) emit("expired");
  } finally {
    if (active) busy.value = false;
  }
}
</script>

<template>
  <section v-if="actor.role === 'owner'" class="panel" aria-label="回收站">
    <h2>回收站</h2>
    <p class="muted">
      将期限内的作品恢复为独立副本，保留设定、正文、保存的草稿、记忆和方法版本。旧执行任务不会重启，知识索引会重新建立。含已清理材料的历史迁移仍在完善，完整性检查失败时不会创建副本。
    </p>
    <button :disabled="busy || recovering" @click="load()">
      {{ busy ? "正在核对…" : loaded ? "刷新回收站" : "查看回收站" }}
    </button>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div v-if="recovered" role="status">
      <p>已恢复为“{{ recovered.name }}”，可以继续创作；知识索引在后台重建。</p>
      <button @click="emit('recovered', recovered)">返回工作室使用副本</button>
    </div>
    <form v-if="selection" @submit.prevent="recover">
      <p>确认从“{{ selection.name }}”建立独立副本。</p>
      <label
        >恢复副本名称<input
          v-model="copyName"
          required
          maxlength="120"
          :disabled="recovering"
      /></label>
      <button :disabled="recovering || busy || !copyName.trim()">
        {{ recovering ? "正在恢复…" : "确认恢复为新副本" }}
      </button>
      <button type="button" :disabled="recovering" @click="selection = null">
        取消
      </button>
    </form>
    <p v-if="loaded && !items.length">没有已删除的作品。</p>
    <ul v-if="items.length">
      <li v-for="item in items" :key="item.id">
        <strong>{{ item.name }}</strong> — {{ label(item.window) }}
        <p>
          删除时间：{{ date(item.deleted_at) }}；恢复截止：{{
            date(item.recover_until)
          }}
        </p>
        <p class="muted">
          核对时间：{{ date(item.checked_at) }}，以最近一次核对结果为准。
        </p>
        <button
          v-if="item.window === 'WITHIN_WINDOW'"
          :disabled="busy || recovering"
          @click="select(item)"
        >
          恢复为新副本
        </button>
      </li>
    </ul>
    <button v-if="next" :disabled="busy || recovering" @click="load(true)">
      查看更多已删除作品
    </button>
  </section>
</template>
