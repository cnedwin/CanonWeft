<script setup lang="ts">
import { onUnmounted, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";

interface CleanupPreview {
  automatic?: boolean;
  id: string;
  project_id: string;
  state: string;
  reason?: string;
  plan_hash: string;
  inventory_revision: number;
  checkpoint_current: boolean;
  restored_disabled: boolean;
  expiring: number;
  removable_objects: number;
  blocked: number;
  overdue: boolean;
  cutoff: string;
}
interface PreviewPage {
  items: CleanupPreview[];
  next_cursor: string;
}

const props = defineProps<{ actor: Actor; projects: Project[] }>();
const emit = defineEmits<{ expired: [] }>();
const selected = ref("");
const items = ref<CleanupPreview[]>([]);
const next = ref("");
const pending = ref("");
const busy = ref(false);
const error = ref("");
const storageReady = ref(false);
const executionEnabled = ref(false);
const automaticEnabled = ref(false);
const automaticConfigured = ref(false);
const confirming = ref("");
let active = true;
let epoch = 0;

function pendingKey(project: string) {
  return `cleanup-preview-v1:${props.actor.workspace_id}:${props.actor.id}:${project}`;
}
function fail(cause: unknown) {
  error.value = message(cause);
  if (cause instanceof APIError && cause.status === 401) emit("expired");
}
function validResponse(token: number, project: string) {
  return active && token === epoch && project === selected.value;
}
function title(item: CleanupPreview) {
  const labels: Record<string, string> = {
    PLANNED: "已保存预览",
    DETACHING: "正在清理到期材料",
    DELETING: "正在清理文件",
    VERIFYING: "正在核对结果",
    RETRY_PENDING: "暂未完成，后台将重试",
    BLOCKED: "已暂缓，需要检查",
    OVERDUE: "已超过期限，需要处理",
    COMPLETE: "清理已完成",
    CANCELLED: "已取消",
  };
  return labels[item.state] || "需要查看执行状态";
}
function running(item: CleanupPreview) {
  return (
    !item.restored_disabled &&
    ["DETACHING", "DELETING", "VERIFYING", "RETRY_PENDING"].includes(item.state)
  );
}
function canStart(item: CleanupPreview) {
  return (
    executionEnabled.value &&
    !item.restored_disabled &&
    ((item.state === "PLANNED" && item.checkpoint_current) ||
      ["BLOCKED", "OVERDUE", "RETRY_PENDING"].includes(item.state))
  );
}
async function start(item: CleanupPreview) {
  if (busy.value || !canStart(item) || confirming.value !== item.id) return;
  const project = selected.value,
    token = epoch;
  busy.value = true;
  error.value = "";
  try {
    const result = await api<CleanupPreview>(
      `/projects/${project}/retention-jobs/${item.id}/start`,
      "POST",
      { plan_hash: item.plan_hash },
    );
    if (!validResponse(token, project)) return;
    if (result.project_id !== project || result.id !== item.id)
      throw new Error("任务归属不一致，请重新读取");
    items.value = items.value.map((old) =>
      old.id === result.id ? result : old,
    );
    confirming.value = "";
  } catch (cause) {
    if (validResponse(token, project)) fail(cause);
  } finally {
    if (validResponse(token, project)) busy.value = false;
  }
}
async function load(append = false, quiet = false) {
  if (!selected.value || busy.value) return;
  const project = selected.value,
    token = epoch;
  busy.value = true;
  error.value = "";
  if (!append && !quiet) {
    items.value = [];
    next.value = "";
  }
  try {
    const cursor =
      append && next.value ? `?after=${encodeURIComponent(next.value)}` : "";
    const page = await api<PreviewPage>(
      `/projects/${project}/retention-jobs${cursor}`,
    );
    if (!validResponse(token, project)) return;
    if (page.items.some((item) => item.project_id !== project))
      throw new Error("预览归属不一致，请重新读取");
    items.value = append ? [...items.value, ...page.items] : page.items;
    next.value = page.next_cursor;
  } catch (cause) {
    if (validResponse(token, project)) fail(cause);
  } finally {
    if (validResponse(token, project)) busy.value = false;
  }
}
async function create() {
  if (!selected.value || busy.value || !storageReady.value) return;
  const project = selected.value,
    token = epoch;
  busy.value = true;
  error.value = "";
  try {
    const operation = pending.value || newOperationID();
    // Persist before sending: a lost response or reload must retry the same
    // durable request, never create another preview under a fresh identifier.
    sessionStorage.setItem(pendingKey(project), operation);
    pending.value = operation;
    const item = await api<CleanupPreview>(
      `/projects/${project}/retention-jobs`,
      "POST",
      { operation_id: operation },
    );
    if (!validResponse(token, project)) return;
    if (item.project_id !== project)
      throw new Error("预览归属不一致，请重新读取");
    items.value = [item, ...items.value.filter((old) => old.id !== item.id)];
    sessionStorage.removeItem(pendingKey(project));
    pending.value = "";
  } catch (cause) {
    if (validResponse(token, project)) fail(cause);
  } finally {
    if (validResponse(token, project)) busy.value = false;
  }
}
async function cancel(item: CleanupPreview) {
  if (busy.value || item.state !== "PLANNED" || item.restored_disabled) return;
  const project = selected.value,
    token = epoch;
  busy.value = true;
  error.value = "";
  try {
    const result = await api<CleanupPreview>(
      `/projects/${project}/retention-jobs/${item.id}/cancel`,
      "POST",
      { plan_hash: item.plan_hash },
    );
    if (!validResponse(token, project)) return;
    if (result.project_id !== project || result.id !== item.id)
      throw new Error("预览归属不一致，请重新读取");
    items.value = items.value.map((old) =>
      old.id === result.id ? result : old,
    );
  } catch (cause) {
    if (validResponse(token, project)) fail(cause);
  } finally {
    if (validResponse(token, project)) busy.value = false;
  }
}
watch(selected, () => {
  epoch++;
  busy.value = false;
  items.value = [];
  next.value = "";
  pending.value = "";
  storageReady.value = false;
  error.value = "";
  confirming.value = "";
  executionEnabled.value = false;
  automaticEnabled.value = false;
  automaticConfigured.value = false;
  if (!selected.value) return;
  try {
    const stored = sessionStorage.getItem(pendingKey(selected.value)) || "";
    if (stored && !/^request-v2:[a-f0-9]{32}$/.test(stored))
      throw new Error("本机未完成请求记录损坏，已停止创建新预览");
    pending.value = stored;
    storageReady.value = true;
  } catch (cause) {
    fail(cause);
    return;
  }
  void load();
  const project = selected.value,
    token = epoch;
  void api<{
    enabled: boolean;
    automatic_enabled?: boolean;
    automatic_configured?: boolean;
  }>("/operations/retention-execution")
    .then((result) => {
      if (validResponse(token, project)) {
        executionEnabled.value = result.enabled;
        automaticEnabled.value = result.automatic_enabled === true;
        automaticConfigured.value = result.automatic_configured === true;
      }
    })
    .catch((cause) => {
      if (validResponse(token, project)) fail(cause);
    });
});
const refreshTimer = setInterval(() => {
  if (active && selected.value && !busy.value && items.value.some(running))
    void load(false, true);
}, 5000);
onUnmounted(() => {
  clearInterval(refreshTimer);
  active = false;
  epoch++;
});
</script>

<template>
  <section class="panel">
    <h2>材料清理</h2>
    <p class="muted">
      先查看到期材料，再确认清理。保存预览不会删除资料；确认后由后台分步执行，关闭页面不会丢失进度。
    </p>
    <label
      >清理预览的作品
      <select v-model="selected" :disabled="busy">
        <option value="">选择作品</option>
        <option
          v-for="project in projects"
          :key="project.id"
          :value="project.id"
        >
          {{ project.name }}
        </option>
      </select>
    </label>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="selected && !executionEnabled" class="notice">
      清理执行尚未启用，当前可保存和查看预览。
    </p>
    <p v-if="selected" class="muted">
      自动清理：{{
        automaticEnabled
          ? "已启用，每天生成一批到期材料计划"
          : automaticConfigured
            ? "等待执行环境或维护身份恢复"
            : "未启用"
      }}。 手工预览和受阻任务保留，后台不会自动接管。
    </p>
    <p v-if="pending" class="notice">
      上次保存尚未收到确认。重试会找回同一份预览。
    </p>
    <div v-if="selected" class="actions">
      <button :disabled="busy || !storageReady" @click="create">
        {{ pending ? "重试保存原预览" : "生成并保存清理预览" }}
      </button>
      <button class="quiet" :disabled="busy" @click="load(false)">
        刷新预览历史
      </button>
    </div>
    <p v-if="busy" role="status">正在处理清理预览…</p>
    <article v-for="item in items" :key="item.id" class="panel">
      <h3>{{ title(item) }}</h3>
      <p v-if="item.automatic" class="muted">来自周期自动清理</p>
      <p>
        扫描时间：{{
          new Date(item.cutoff).toLocaleString("zh-CN", { hour12: false })
        }}
      </p>
      <p>
        待清理引用：{{ item.expiring }}；文件：{{
          item.removable_objects
        }}；暂缓项：{{ item.blocked }}
      </p>
      <p
        v-if="item.state === 'PLANNED' && !item.checkpoint_current"
        class="notice"
      >
        资料已变化，需要重新生成预览
      </p>
      <p v-if="item.restored_disabled" class="notice">
        恢复副本中的历史计划，仅供查看
      </p>
      <p v-if="item.overdue" class="error">
        部分资料已超过清理期限，需要处理。
      </p>
      <p v-if="running(item)" role="status">
        已交给后台处理，页面会自动刷新进度。
      </p>
      <p v-if="item.reason" class="notice">{{ item.reason }}</p>
      <p v-else-if="item.state === 'BLOCKED'" class="notice">
        依赖、权限或材料状态已变化。处理原因后可重试原任务，或重新生成预览；已完成步骤不会重复删除。
      </p>
      <div v-if="canStart(item)" class="actions">
        <button
          v-if="confirming !== item.id"
          :disabled="busy"
          @click="confirming = item.id"
        >
          {{ item.state === "PLANNED" ? "执行此清理计划" : "重试此清理任务" }}
        </button>
        <div v-else>
          <p>
            将永久清理本预览列出的到期材料和文件。后台会再次核对使用状态及恢复期限。确认继续？
          </p>
          <button :disabled="busy" @click="start(item)">确认清理</button>
          <button class="quiet" :disabled="busy" @click="confirming = ''">
            返回
          </button>
        </div>
      </div>
      <button
        v-if="item.state === 'PLANNED' && !item.restored_disabled"
        class="quiet"
        :disabled="busy"
        @click="cancel(item)"
      >
        取消此预览
      </button>
    </article>
    <button v-if="next" class="quiet" :disabled="busy" @click="load(true)">
      查看更早的预览
    </button>
  </section>
</template>
