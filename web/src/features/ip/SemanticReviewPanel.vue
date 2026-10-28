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
import QualificationPanel from "../skills/QualificationPanel.vue";
import ModelsPage from "../models/ModelsPage.vue";
import {
  reviewStateNames,
  reviewVerdictNames,
  type SemanticReview,
} from "./semanticReviews";

const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: string;
  targetKind: "ip_candidate" | "artifact_release";
  targetID: string;
  targetHash: string;
  required: boolean;
  blocked?: boolean;
}>();
const emit = defineEmits<{
  ready: [boolean];
  blocking: [boolean];
  expired: [];
  reviewRequired: [boolean];
}>();
type Binding = {
  id: string;
  task_kind: string;
  enabled: boolean;
  current: boolean;
  valid: boolean;
  qualification?: {
    frozen: {
      input: { role: string; max_output_tokens: number };
      config: { id: string; revision: number; name: string };
    };
  };
};
type Option = {
  role: string;
  name: string;
  price: {
    id: string;
    currency: string;
    connection_id: string;
    connection_revision: number;
  };
};
const root = computed(() => `/projects/${props.project.id}`);
const bindings = ref<Binding[]>([]),
  options = ref<Option[]>([]),
  runs = ref<SemanticReview[]>([]);
const bindingID = ref(""),
  selectedID = ref(""),
  selected = ref<SemanticReview | null>(null);
const operation = ref(newOperationID()),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const showQualification = ref(false),
  showModels = ref(false),
  setupDirty = ref(false);
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const canSelect = computed(
  () => props.actor.role !== "reader" && props.project.role !== "reader",
);
const binding = computed(() =>
  bindings.value.find((b) => b.id === bindingID.value),
);
const option = computed(() =>
  options.value.find(
    (o) =>
      o.role === "critic" &&
      o.price.connection_id ===
        binding.value?.qualification?.frozen.config.id &&
      o.price.connection_revision ===
        binding.value?.qualification?.frozen.config.revision,
  ),
);
const tokens = computed(
  () => binding.value?.qualification?.frozen.input.max_output_tokens ?? 1024,
);
let disposed = false,
  epoch = 0,
  timer: ReturnType<typeof setTimeout> | undefined;
watch([busy, setupDirty], () =>
  emit("blocking", busy.value || setupDirty.value),
);
watch(bindingID, () => (operation.value = newOperationID()));
watch(
  () => option.value?.price.id,
  () => (operation.value = newOperationID()),
);
watch(
  () => [props.targetID, props.targetHash, props.branch, props.project.id],
  () => {
    epoch++;
    selected.value = null;
    selectedID.value = "";
    runs.value = [];
    operation.value = newOperationID();
    emit("ready", false);
    void refresh();
  },
);
function handle(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
  if (e instanceof APIError && e.status === 410) {
    selected.value = null;
    emit("ready", false);
  }
}
async function readSelected(expected = epoch) {
  if (!selectedID.value) {
    selected.value = null;
    emit("ready", false);
    return;
  }
  const id = selectedID.value;
  const r = await api<SemanticReview>(`${root.value}/semantic-reviews/${id}`);
  if (disposed || expected !== epoch || selectedID.value !== id) return;
  selected.value = r;
  emit("ready", r.selected && r.selection_ready && !r.restored_disabled);
}
async function refresh() {
  const current = epoch;
  try {
    const [b, o, list] = await Promise.all([
      api<{ items: Binding[] }>(`${root.value}/qualification/bindings`),
      api<{ items: Option[] }>(`${root.value}/generation-options`),
      api<{ items: SemanticReview[] }>(
        `${root.value}/semantic-reviews?branch_id=${encodeURIComponent(props.branch)}&target_kind=${props.targetKind}&target_id=${encodeURIComponent(props.targetID)}`,
      ),
    ]);
    if (disposed || current !== epoch) return;
    bindings.value = b.items.filter(
      (v) =>
        v.task_kind === "shared.semantic-review" &&
        v.enabled &&
        v.current &&
        v.valid &&
        v.qualification?.frozen.input.role === "critic",
    );
    options.value = o.items;
    if (!bindingID.value && bindings.value[0])
      bindingID.value = bindings.value[0].id;
    runs.value = list.items;
    if (list.items.some((r) => r.id === selectedID.value && r.retention)) {
      selectedID.value = "";
      selected.value = null;
      emit("ready", false);
    }
    emit(
      "reviewRequired",
      props.required ||
        list.items.some(
          (r) =>
            r.state === "COMPLETED" &&
            ["FAIL", "UNKNOWN"].includes(r.verdict) &&
            !r.restored_disabled &&
            !r.cancel_requested,
        ),
    );
    if (!selectedID.value)
      selectedID.value =
        (
          list.items.find((r) => !r.retention && r.selected) ??
          list.items.find((r) => !r.retention)
        )?.id ?? "";
    await readSelected(current);
  } catch (e) {
    if (!disposed && current === epoch) handle(e);
  }
}
async function act(fn: () => Promise<void>) {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await fn();
  } catch (e) {
    handle(e);
  } finally {
    busy.value = false;
  }
}
async function prepare() {
  await act(async () => {
    const r = await api<SemanticReview>(
      `${root.value}/semantic-reviews`,
      "POST",
      {
        operation_id: operation.value,
        target_kind: props.targetKind,
        target_id: props.targetID,
        target_hash: props.targetHash,
        branch_id: props.branch,
        reviewer_binding_id: binding.value?.id ?? "",
        price_id: option.value?.price.id ?? "",
        max_output_tokens: tokens.value,
      },
    );
    selectedID.value = r.id;
    selected.value = r;
    operation.value = newOperationID();
    notice.value = "待审内容已保存。准备没有发起模型调用。";
    await refresh();
  });
}
async function control(action: string) {
  const id = selectedID.value;
  if (!id) return;
  await act(async () => {
    if (action === "start")
      await api(`${root.value}/semantic-reviews/${id}/start`, "POST", {});
    else if (action === "select") {
      await api(`${root.value}/semantic-reviews/${id}/select`, "POST", {
        expected_revision: selected.value?.selection_revision ?? 0,
      });
      notice.value = "已选择此报告。仍需明确采用与发布正文。";
    } else
      await api(`${root.value}/semantic-reviews/${id}/control`, "POST", {
        action,
      });
    await refresh();
  });
}
async function installMethod() {
  await act(async () => {
    await api(`${root.value}/qualification/baseline-presets`, "POST", {
      suite_id: "shared-semantic-review-v1",
      kind: "agent",
      role: "critic",
    });
    showQualification.value = true;
    notice.value =
      "独立审查方法已安装。请选择独立共享语义审查评测集和 critic 用途，经实际评测后才可启用。";
  });
}
function poll() {
  timer = setTimeout(async () => {
    if (!busy.value) await refresh();
    if (!disposed) poll();
  }, 4000);
}
onMounted(async () => {
  await refresh();
  poll();
});
onBeforeUnmount(() => {
  disposed = true;
  epoch++;
  clearTimeout(timer);
  emit("blocking", false);
});
</script>

<template>
  <section class="panel semantic-review" data-testid="semantic-review-panel">
    <h4>独立语义审查</h4>
    <p v-if="required" class="notice">
      当前内容需要独立复核。旧资格对应旧内容，需要复核当前修改；已发现的冲突或不确定问题也须解决，草稿和原来源都会保留。
    </p>
    <p v-else class="muted">
      纯人工内容可按人工流程保存、采用和发布，不强制调用模型。未运行审查时，状态如实保持未审。
    </p>
    <p class="muted">
      审查仅覆盖固定输入；不代表全书远端依赖或文学质量已经验收。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <div v-if="canEdit" class="actions">
      <label
        >独立审查用途<select v-model="bindingID" :disabled="busy || blocked">
          <option value="">尚未选择可用资格</option>
          <option v-for="b in bindings" :key="b.id" :value="b.id">
            {{ b.qualification?.frozen.config.name }} ·
            {{ b.qualification?.frozen.input.max_output_tokens }} 输出额度
          </option>
        </select></label
      >
      <button :disabled="busy || blocked" @click="prepare">
        保存并准备当前内容审查
      </button>
      <button class="quiet" :disabled="busy" @click="refresh">
        刷新条件与报告
      </button>
    </div>
    <div v-if="canEdit && (!binding || !option)" class="notice">
      <p>尚缺可用的独立审查资格或模型价格，仍可保存待审请求。</p>
      <button :disabled="busy || blocked" @click="installMethod">
        安装独立审查方法
      </button>
      <button :disabled="busy" @click="showQualification = !showQualification">
        {{ showQualification ? "收起资格设置" : "打开审查资格设置" }}
      </button>
      <button
        v-if="actor.role === 'owner'"
        :disabled="busy"
        @click="showModels = !showModels"
      >
        打开模型与费用设置
      </button>
    </div>
    <ModelsPage
      v-if="showModels"
      @close="
        showModels = false;
        refresh();
      "
      @expired="emit('expired')"
    />
    <QualificationPanel
      v-if="showQualification"
      initial-suite="shared-semantic-review-v1"
      :actor="actor"
      :project="project"
      @blocking="setupDirty = $event"
      @expired="emit('expired')"
    />
    <label v-if="runs.length"
      >审查历史<select
        v-model="selectedID"
        :disabled="busy"
        @change="readSelected().catch(handle)"
      >
        <option
          v-for="r in runs"
          :key="r.id"
          :value="r.id"
          :disabled="!!r.retention"
        >
          {{ r.retention ? "材料已过期 · " : "" }}
          {{ reviewStateNames[r.state] ?? r.state }} ·
          {{ reviewVerdictNames[r.verdict] ?? r.verdict }}
          {{ r.selected ? "（已选择）" : "" }}
        </option>
      </select></label
    >
    <article v-if="selected">
      <p>
        {{ reviewStateNames[selected.state] ?? selected.state }} ·
        {{ reviewVerdictNames[selected.verdict] ?? selected.verdict
        }}<span v-if="selected.paused"> · 已暂停</span>
      </p>
      <p v-if="selected.reason" class="notice">{{ selected.reason }}</p>
      <p v-if="selected.restored_disabled" class="notice">
        这是恢复的历史报告，仅供查看。请针对当前内容新建审查。
      </p>
      <p v-if="selected.generation_id">
        预计费用 {{ (selected.estimate_micros / 1000000).toFixed(6) }}
        {{ selected.estimate_currency }}；实际启动仍须通过当前预算门禁。
      </p>
      <p v-if="selected.selected && !selected.selection_ready" class="notice">
        历史选择已保留，但当前目标、权限或资格不再满足采用条件，请重新准备。
      </p>
      <div v-if="canEdit && !selected.restored_disabled" class="actions">
        <button
          v-if="
            ['PREPARED', 'BLOCKED'].includes(selected.state) &&
            selected.generation_id
          "
          :disabled="busy || blocked"
          @click="control('start')"
        >
          按估算启动或继续审查
        </button>
        <button
          v-if="['QUEUED', 'RUNNING'].includes(selected.state)"
          :disabled="busy || blocked"
          @click="control(selected.paused ? 'resume' : 'pause')"
        >
          {{ selected.paused ? "继续审查" : "暂停审查" }}
        </button>
        <button
          v-if="!['COMPLETED', 'FAILED', 'CANCELLED'].includes(selected.state)"
          :disabled="busy || blocked"
          @click="control('cancel')"
        >
          取消此次审查
        </button>
      </div>
      <div v-if="canSelect && !selected.restored_disabled" class="actions">
        <button
          v-if="selected.state === 'COMPLETED' && selected.verdict === 'PASS'"
          :disabled="busy || blocked || selected.selection_ready"
          @click="control('select')"
        >
          确认选择此审查报告
        </button>
      </div>
      <div v-if="selected.report">
        <p>{{ selected.report.summary }}</p>
        <p>已检查 {{ selected.report.coverage.length }} 个固定对象。</p>
        <ul v-if="selected.report.issues.length">
          <li v-for="(issue, index) in selected.report.issues" :key="index">
            {{ issue.explanation }}
            <blockquote
              v-for="anchor in selected.anchors?.filter((a) =>
                issue.anchor_keys.includes(a.key),
              )"
              :key="anchor.key"
            >
              {{ anchor.text }}
            </blockquote>
          </li>
        </ul>
      </div>
    </article>
  </section>
</template>
