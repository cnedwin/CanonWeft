<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import type { Branch, Candidate } from "./types";
import ChangeScopePanel from "./ChangeScopePanel.vue";
import ChangeInspectionPanel from "./ChangeInspectionPanel.vue";
import { describePolicy } from "./knowledgePolicy";
import {
  dependencyNames,
  describeDependencies,
  type DependencyEntry,
  type DependencyPolicy,
  type DependencyRef,
} from "./dependencies";
interface Resource {
  id: string;
  name: string;
  head: string;
  text: string;
  dependencies?: DependencyPolicy;
}
interface Plan {
  additional_review?: { node: DependencyRef; reason: string }[];
  recovery?: {
    project_id: string;
    plan_id: string;
    content_hash: string;
    source_event_id?: string;
    source_event_kind?: string;
  }[];
  source_event_id?: string;
  source_state?: string;
  release_id?: string;
  differences: {
    ref: DependencyRef;
    name: string;
    fields: { name: string; before: string; after: string }[];
  }[];
  id: string;
  stale: boolean;
  semantic_review: string;
  changes: { before: DependencyRef; after: DependencyRef }[];
  items: {
    node: DependencyRef;
    action: string;
    via: { source: DependencyRef; kind: string; reason: string }[];
  }[];
  untracked: DependencyRef[];
  outside_recorded_closure: DependencyRef[];
}
const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: Branch;
  candidates: Candidate[];
  resources: Resource[];
  releases: {
    id: string;
    status: string;
    manifest: {
      expected_ip_head: string;
      artifacts: { resource_id: string }[];
    };
  }[];
  blocked: boolean;
}>();
const emit = defineEmits<{
  use: [{ id: string; branch: string }];
  prepared: [string];
  blocking: [boolean];
  expired: [];
}>();
const root = `/projects/${props.project.id}`;
const catalog = ref<DependencyEntry[]>([]);
const resourceID = ref("");
const editing = ref<Resource | null>(null);
const policy = ref<DependencyPolicy>({ frozen: false, inputs: [] });
const sourceKey = ref("");
const kind = ref("semantic");
const reason = ref("");
const pinned = ref(false);
const frozenReason = ref("");
const operation = ref(newOperationID());
const candidateID = ref("");
const releaseID = ref("");
const history = ref<
  {
    id: string;
    release_id: string;
    changes: number;
    impacts: number;
    created_at: string;
    source_event_id?: string;
    retention?: { state: "EXPIRED"; expired_at: string };
  }[]
>([]);
const plan = ref<Plan | null>(null);
const outcomes = ref<{
  next?: string;
  items: {
    resource_id: string;
    name: string;
    branch_id: string;
    release_id: string;
    status: string;
    current: boolean;
    reason: string;
  }[];
  truncated: boolean;
  plan_stale: boolean;
  untracked: number;
  outside: number;
} | null>(null);
const refreshOperation = ref(newOperationID());
watch(
  () => plan.value?.id,
  () => {
    outcomes.value = null;
    refreshOperation.value = newOperationID();
  },
);
const planID = ref("");
const panel = ref<HTMLDetailsElement | null>(null);
const busy = ref(false);
const scopeEditBlocking = ref(false);
const inspectionBlocking = ref(false);
const scopeBlocking = computed(
  () => scopeEditBlocking.value || inspectionBlocking.value,
);
const dirty = ref(false);
const error = ref("");
const notice = ref("");
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const unavailable = computed(() => busy.value || props.blocked);
const candidates = computed(() =>
  props.candidates.filter(
    (c) => c.status !== "PUBLISHED" && c.base_head === props.branch.head,
  ),
);
const releaseChoices = computed(() =>
  props.releases.filter(
    (r) =>
      r.status !== "PUBLISHED" &&
      r.manifest.expected_ip_head === props.branch.head,
  ),
);
const sourceChoices = computed(() =>
  catalog.value.filter(
    (c) => !(c.ref.kind === "resource" && c.ref.id === editing.value?.id),
  ),
);
const labels: Record<string, string> = {
  DECIDE: "需要裁决：冻结内容",
  REVIEW: "需要复核",
  REBUILD: "需要重建候选",
};
const fieldNames: Record<string, string> = {
  text: "正文",
  policy: "资料用途",
  dependencies: "版本依赖",
  sources: "来源实体",
  target: "目标实体",
  reason: "理由",
  id: "身份编号",
  name: "名称",
  description: "简介",
  kind: "类型",
  aliases: "别名",
  status: "状态",
  profile: "能力档案",
  locked: "锁定状态",
  subject: "主体",
  predicate: "属性",
  value: "内容",
  object_id: "关联对象",
  effective_from: "生效时间",
  effective_until: "失效时间",
  known_from: "知情时间",
  known_by: "知情角色",
  visibility: "可见范围",
  recorded_at: "记录时间",
  source: "来源",
  holder_id: "持有者",
  context: "发生情境",
  confidence: "置信度",
  when: "适用条件",
  exceptions: "例外",
  operator: "判断方式",
  operand: "规则值",
  time: "发生时间",
  participants: "参与者",
  causes: "前因",
};
function key(r: DependencyRef) {
  return `${r.kind}:${r.branch_id}:${r.id}`;
}
function title(r: DependencyRef) {
  const found = catalog.value.find((e) => key(e.ref) === key(r));
  return found
    ? `${found.branch_name} / ${found.name}`
    : `${r.kind} · ${r.id.slice(0, 12)}`;
}
function readableField(name: string, value: string): string {
  if (name === "locked")
    return value === "true" ? "是" : value === "false" ? "否" : value;
  if (
    ![
      "policy",
      "dependencies",
      "aliases",
      "known_by",
      "sources",
      "participants",
      "causes",
    ].includes(name)
  )
    return value;
  try {
    const parsed = JSON.parse(value);
    if (name === "policy") return describePolicy(parsed ?? undefined);
    if (name === "dependencies") {
      const policy = parsed as DependencyPolicy | null;
      return [
        describeDependencies(policy ?? undefined),
        ...(policy?.inputs ?? []).map(
          (d) =>
            `${title(d.source)} · ${dependencyNames[d.kind]} · ${d.pinned ? "固定历史版本" : "跟随最新版本"}\n${d.reason}`,
        ),
      ].join("\n");
    }
    if (Array.isArray(parsed))
      return (
        parsed
          .map(
            (id) =>
              catalog.value.find(
                (e) => e.ref.id === id && e.ref.branch_id === props.branch.id,
              )?.name ?? String(id),
          )
          .join("、") || "无"
      );
  } catch {
    /* Old versions may omit a field entirely. */
  }
  return value;
}
watch(
  [busy, dirty, scopeBlocking],
  () => emit("blocking", busy.value || dirty.value || scopeBlocking.value),
  {
    immediate: true,
  },
);
watch(
  [policy, frozenReason],
  () => {
    if (editing.value) {
      dirty.value = true;
      operation.value = newOperationID();
    }
  },
  { deep: true, flush: "sync" },
);
async function run(work: () => Promise<void>) {
  if (unavailable.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  } finally {
    busy.value = false;
  }
}
async function refreshCatalog() {
  catalog.value = (
    await api<{ items: DependencyEntry[] }>(`${root}/dependencies/catalog`)
  ).items;
}
async function load() {
  if (scopeBlocking.value) return;
  await run(async () => {
    const r = await api<Resource>(`${root}/resources/${resourceID.value}`);
    await refreshCatalog();
    editing.value = null;
    policy.value = structuredClone(
      r.dependencies ?? { frozen: false, inputs: [] },
    );
    frozenReason.value = "";
    editing.value = r;
    dirty.value = false;
    operation.value = newOperationID();
  });
}
function add() {
  const entry = catalog.value.find((e) => key(e.ref) === sourceKey.value);
  if (!entry || !reason.value.trim()) return;
  if (
    policy.value.inputs.some(
      (d) => key(d.source) === key(entry.ref) && d.kind === kind.value,
    )
  ) {
    error.value = "同一来源的这种关联已经存在。";
    return;
  }
  policy.value.inputs.push({
    source: JSON.parse(JSON.stringify(entry.ref)),
    kind: kind.value,
    reason: reason.value.trim(),
    pinned: pinned.value,
  });
  reason.value = "";
}
async function save() {
  await run(async () => {
    const r = editing.value;
    if (!r) return;
    const result = await api<{ id: string }>(`${root}/releases`, "POST", {
      operation_id: operation.value,
      branch_id: props.branch.id,
      expected_ip_head: props.branch.head,
      updates: [
        {
          resource_id: r.id,
          expected_head: r.head,
          text: r.text,
          dependencies: policy.value,
          frozen_change_reason: frozenReason.value,
        },
      ],
    });
    dirty.value = false;
    notice.value = "依赖候选已保存，请在下方检查对照、确认采用并发布。";
    emit("prepared", result.id);
  });
}
async function refreshHistory() {
  history.value = (
    await api<{ items: typeof history.value }>(
      `${root}/changes?branch=${props.branch.id}`,
    )
  ).items;
}
async function analyze(target: "ip" | "release" = "ip") {
  if (scopeBlocking.value) return;
  await run(async () => {
    await refreshCatalog();
    plan.value = await api<Plan>(`${root}/changes`, "POST", {
      operation_id: newOperationID(),
      branch_id: props.branch.id,
      candidate_id: target === "ip" ? candidateID.value : "",
      release_id: target === "release" ? releaseID.value : undefined,
    });
    planID.value = plan.value.id;
    await refreshHistory();
  });
}
async function readPlan() {
  if (scopeBlocking.value) return;
  await run(async () => {
    await refreshCatalog();
    plan.value = await api<Plan>(`${root}/changes/${planID.value}`);
  });
}
async function refreshPlan() {
  if (scopeBlocking.value) return;
  const id = plan.value?.id;
  if (!id) return;
  await run(async () => {
    const result = await api<Plan>(`${root}/changes/${id}/refresh`, "POST", {
      operation_id: refreshOperation.value,
    });
    if (plan.value?.id !== id) return;
    plan.value = result;
    planID.value = result.id;
    await refreshHistory();
    await refreshCatalog();
    notice.value = "已按当前目标版本重新生成清单，旧清单和处理记录仍保留。";
  });
}
async function readOutcomes(more = false) {
  if (scopeBlocking.value) return;
  const id = plan.value?.id;
  if (!id) return;
  const previous = more ? outcomes.value : null;
  if (more && !previous?.next) return;
  await run(async () => {
    if (!more) outcomes.value = null;
    const result = await api<NonNullable<typeof outcomes.value>>(
      `${root}/changes/${id}/outcomes${previous?.next ? `?after=${encodeURIComponent(previous.next)}` : ""}`,
    );
    if (plan.value?.id === id)
      outcomes.value = {
        ...result,
        items: [
          ...new Map(
            [...(previous?.items ?? []), ...result.items].map((item) => [
              item.release_id + ":" + item.resource_id,
              item,
            ]),
          ).values(),
        ],
      };
  });
}
async function reloadHistory() {
  if (scopeBlocking.value) return;
  await run(refreshHistory);
}
async function scopeExpanded(id: string) {
  await run(async () => {
    const result = await api<Plan>(`${root}/changes/${id}`);
    plan.value = result;
    planID.value = result.id;
    await refreshHistory();
    notice.value = "补充对象已绑定具体版本纳入新清单，仍待审查；原清单保留。";
  });
}
async function previewCandidate(id: string) {
  if (
    scopeBlocking.value ||
    unavailable.value ||
    !candidates.value.some((c) => c.id === id)
  )
    return;
  candidateID.value = id;
  if (panel.value) panel.value.open = true;
  await analyze("ip");
  panel.value?.scrollIntoView({ behavior: "smooth", block: "start" });
}
defineExpose({ previewCandidate });
function usePlan(branchID: string) {
  if (!plan.value || unavailable.value || dirty.value || scopeBlocking.value)
    return;
  emit("use", { id: plan.value.id, branch: branchID });
}
function close() {
  editing.value = null;
  dirty.value = false;
  notice.value = "已放弃这次未保存的依赖编辑。";
}
function unload(e: BeforeUnloadEvent) {
  if (dirty.value || busy.value || scopeBlocking.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
onMounted(() => {
  window.addEventListener("beforeunload", unload);
  // Read-only initial loading must also run while the parent loads its lists.
  // A disabled editor is not a reason to permanently skip saved history.
  busy.value = true;
  void Promise.all([refreshCatalog(), refreshHistory()])
    .catch((e) => {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    })
    .finally(() => {
      busy.value = false;
    });
});
onUnmounted(() => {
  window.removeEventListener("beforeunload", unload);
  emit("blocking", false);
});
</script>

<template>
  <details ref="panel" class="dependency-panel" data-testid="dependency-panel">
    <summary>依赖、冻结与变更影响</summary>
    <p class="muted">
      记录这份资料使用了哪些版本的设定、证据或方法。分析只产生检查清单，正文须经候选确认后才会变化。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <form v-if="canEdit" @submit.prevent="load">
      <label
        >要维护依赖的资料<select
          v-model="resourceID"
          aria-label="要维护依赖的资料"
          :disabled="unavailable || dirty"
          required
        >
          <option value="">请选择</option>
          <option v-for="r in resources" :key="r.id" :value="r.id">
            {{ r.name }}
          </option>
        </select></label
      >
      <button :disabled="unavailable || dirty || !resourceID">
        载入资料依赖
      </button>
    </form>
    <div v-if="editing">
      <h3>{{ editing.name }} · 依赖编辑</h3>
      <label class="check-line"
        ><input
          v-model="policy.frozen"
          type="checkbox"
          :disabled="unavailable"
        />冻结此产物，后续修改须由所有者裁决</label
      >
      <ul>
        <li v-for="(d, index) in policy.inputs" :key="key(d.source) + d.kind">
          <strong>{{ title(d.source) }}</strong> ·
          {{ dependencyNames[d.kind] }} ·
          {{ d.pinned ? "固定此版本，变更仍通知" : "跟随最新版本" }}
          <p>{{ d.reason }} · 来源版本 {{ d.source.revision.slice(0, 12) }}</p>
          <button
            class="quiet"
            :disabled="unavailable"
            @click="policy.inputs.splice(index, 1)"
          >
            移除此依赖
          </button>
        </li>
      </ul>
      <form @submit.prevent="add">
        <label
          >依赖来源<select
            v-model="sourceKey"
            aria-label="依赖来源"
            :disabled="unavailable"
            required
          >
            <option value="">选择正式版本</option>
            <option
              v-for="c in sourceChoices"
              :key="key(c.ref)"
              :value="key(c.ref)"
            >
              {{ c.branch_name }} / {{ c.name }} ·
              {{ c.ref.revision.slice(0, 8) }}
            </option>
          </select></label
        >
        <label
          >关联类型<select
            v-model="kind"
            aria-label="关联类型"
            :disabled="unavailable"
          >
            <option
              v-for="(label, value) in dependencyNames"
              :key="value"
              :value="value"
            >
              {{ label }}
            </option>
          </select></label
        >
        <label
          >关联理由<input
            v-model="reason"
            maxlength="500"
            required
            :disabled="unavailable"
        /></label>
        <label class="check-line"
          ><input
            v-model="pinned"
            type="checkbox"
            :disabled="unavailable"
          />固定使用所选历史版本；上游变化时保留复核提醒</label
        >
        <button :disabled="unavailable || !sourceKey || !reason.trim()">
          添加依赖
        </button>
      </form>
      <label v-if="editing.dependencies?.frozen"
        >冻结内容修改原因<textarea
          v-model="frozenReason"
          maxlength="2000"
          :disabled="unavailable"
        />
      </label>
      <p class="muted">
        依赖修改在保存候选后持久保留；这里不会改写资料正文。网上导入的 Skill
        仅作方法参考，不因此获得执行权限。
      </p>
      <div class="actions">
        <button
          :disabled="
            unavailable ||
            !dirty ||
            (editing.dependencies?.frozen && !frozenReason.trim())
          "
          @click="save"
        >
          保存依赖候选</button
        ><button class="quiet" :disabled="unavailable" @click="close">
          放弃编辑并收起
        </button>
      </div>
    </div>
    <h3>检查设定改动的影响</h3>
    <form v-if="canEdit" @submit.prevent="analyze('ip')">
      <label
        >待分析的设定候选<select
          v-model="candidateID"
          aria-label="待分析的设定候选"
          :disabled="unavailable || dirty"
          required
        >
          <option value="">请选择</option>
          <option v-for="c in candidates" :key="c.id" :value="c.id">
            {{ c.id.slice(0, 12) }} ·
            {{ c.snapshot.entities.map((e) => e.name).join("、") }}
          </option>
        </select></label
      >
      <button :disabled="unavailable || dirty || !candidateID">
        生成变更影响清单
      </button>
    </form>
    <form v-if="canEdit" @submit.prevent="analyze('release')">
      <label
        >待分析的资料候选<select
          v-model="releaseID"
          aria-label="待分析的资料候选"
          :disabled="unavailable || dirty"
          required
        >
          <option value="">请选择尚未发布的资料组</option>
          <option v-for="r in releaseChoices" :key="r.id" :value="r.id">
            {{
              r.manifest.artifacts
                .map(
                  (a) =>
                    resources.find((x) => x.id === a.resource_id)?.name ??
                    "资料",
                )
                .join("、")
            }}
            · {{ r.id.slice(0, 8) }}
          </option>
        </select></label
      >
      <button :disabled="unavailable || dirty || !releaseID">
        生成资料变更清单
      </button>
    </form>
    <form @submit.prevent="readPlan">
      <label
        >已保存的影响清单<select
          v-model="planID"
          aria-label="已保存的影响清单"
          :disabled="unavailable || dirty"
          required
        >
          <option value="">选择最近的检查记录</option>
          <option
            v-for="item in history"
            :key="item.id"
            :value="item.id"
            :disabled="!!item.retention"
          >
            {{ item.retention ? "已过期清理 · " : "" }}
            {{ item.source_event_id ? "发布后自动登记 · " : "" }}
            {{ item.created_at }} · {{ item.release_id ? "资料" : "设定" }}变更
            {{ item.changes }} 项 · 影响 {{ item.impacts }} 项
          </option>
        </select></label
      ><button
        :disabled="
          unavailable ||
          dirty ||
          !planID ||
          !!history.find((item) => item.id === planID)?.retention
        "
      >
        打开并检查清单时效
      </button>
    </form>
    <button
      class="quiet"
      :disabled="unavailable || dirty"
      @click="reloadHistory"
    >
      刷新发布后的影响记录
    </button>
    <p class="muted">
      发布后会自动登记变更影响。后台处理完成后刷新可查看；登记不会自动改写正文或代表语义复核通过。
    </p>
    <div v-if="plan" data-testid="impact-plan">
      <p v-if="plan.recovery?.length" class="notice">
        这是从旧作品恢复的影响记录，保留当时的版本关系。旧任务和审核许可不会恢复，继续修改前须检查当前版本。
      </p>
      <p v-if="plan.source_event_id" class="notice">
        这是发布后自动保存的影响记录，保留发布时的原版本与改后版本。
      </p>
      <p v-if="plan.source_state === 'SUPERSEDED'" class="error">
        该发布已有后续版本。这份记录只供历史复核，继续修订前请按最新内容重新分析。
      </p>
      <h3>修改前后对照</h3>
      <details v-for="diff in plan.differences" :key="key(diff.ref)" open>
        <summary>{{ diff.name }}</summary>
        <table>
          <thead>
            <tr>
              <th>项目</th>
              <th>原版本</th>
              <th>候选版本</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="field in diff.fields" :key="field.name">
              <th>{{ fieldNames[field.name] ?? field.name }}</th>
              <td>{{ readableField(field.name, field.before) }}</td>
              <td>{{ readableField(field.name, field.after) }}</td>
            </tr>
          </tbody>
        </table>
      </details>
      <p>
        本次改变 {{ plan.changes.length }} 项内容，检查清单包含
        {{ plan.items.length }} 项内容（已登记关联与作者补充对象）。
      </p>
      <p v-if="plan.stale" class="error">
        相关版本已变化。这是历史清单，请重新分析最新候选。
      </p>
      <button
        v-if="canEdit && plan.source_event_id"
        :disabled="unavailable || dirty || plan.source_state === 'SUPERSEDED'"
        @click="refreshPlan"
      >
        按当前目标版本重新检查
      </button>
      <button
        :disabled="unavailable || dirty || scopeBlocking"
        @click="readOutcomes()"
      >
        查看处理记录
      </button>
      <section v-if="outcomes" aria-label="版本绑定的处理记录">
        <h4>处理记录</h4>
        <p>
          以下状态来自实际候选与发布记录，尚未采用的决定不算完成。没有记录不等于无需修改。
        </p>
        <p v-if="outcomes.plan_stale">
          清单已过期，记录仍可追溯；继续处理前请重新检查目标版本。
        </p>
        <p v-if="outcomes.truncated">
          还有未加载的处理记录，请继续加载；不能据此判断整体完成。
        </p>
        <p>
          未登记依赖 {{ outcomes.untracked }} 项；原记录范围外
          {{ outcomes.outside }} 项，仍需另行核对。
        </p>
        <p v-if="!outcomes.items.length">尚无关联的修订或引用处理候选。</p>
        <p class="muted">
          分页显示与这次变更关联的生成修订和引用决定。未关联的手工编辑不自动计为已处理；新产生的候选请重新查看第一页。已加载页面的状态以各次读取时为准。
        </p>
        <article
          v-for="item in outcomes.items"
          :key="item.release_id + item.resource_id"
        >
          <strong>{{ item.name }}</strong>
          <p>
            {{
              item.current
                ? "已发布，仍是当前版本"
                : item.status === "PUBLISHED"
                  ? "曾发布，之后已有修改"
                  : item.status === "READY"
                    ? "已采用，尚未发布"
                    : "候选已保存，尚待复核与采用"
            }}
          </p>
          <p>{{ item.reason }}</p>
        </article>
        <button
          v-if="outcomes.next"
          :disabled="unavailable || dirty || scopeBlocking"
          @click="readOutcomes(true)"
        >
          继续加载处理记录
        </button>
      </section>
      <p class="notice">
        独立审查按所选批次记录。未进入传播范围或未选入审查的内容不等于已经证明无关。
      </p>
      <ChangeScopePanel
        v-if="canEdit"
        :key="plan.id"
        :project-id="project.id"
        :branch-id="branch.id"
        :plan-id="plan.id"
        :stale="plan.stale || plan.source_state === 'SUPERSEDED'"
        :catalog="catalog"
        :included="plan.items.map((item) => item.node)"
        :blocked="unavailable || dirty || inspectionBlocking"
        @expanded="scopeExpanded"
        @blocking="scopeEditBlocking = $event"
        @expired="emit('expired')"
      />
      <p v-for="choice in plan.additional_review ?? []" :key="key(choice.node)">
        作者补充检查 · {{ title(choice.node) }}：{{ choice.reason }}
      </p>
      <ChangeInspectionPanel
        :key="`inspection-${plan.id}`"
        :project="project"
        :actor="actor"
        :plan-id="plan.id"
        :source-branch="branch.id"
        :included="plan.items.map((item) => item.node)"
        :catalog="catalog"
        :stale="plan.stale || plan.source_state === 'SUPERSEDED'"
        :blocked="unavailable || dirty || scopeEditBlocking"
        @blocking="inspectionBlocking = $event"
        @expired="emit('expired')"
      />
      <p class="muted">
        本次依赖分析在本地完成，没有模型调用费用。后续重新生成内容需要单独估算。
      </p>
      <button
        v-if="canEdit"
        :disabled="
          unavailable ||
          dirty ||
          plan.stale ||
          plan.source_state === 'SUPERSEDED'
        "
        @click="usePlan(branch.id)"
      >
        用于 AI 审查或连续性修订
      </button>
      <article
        v-for="item in plan.items"
        :key="key(item.node)"
        class="candidate-card"
      >
        <h4>{{ title(item.node) }} · {{ labels[item.action] }}</h4>
        <button
          v-if="
            canEdit &&
            plan.source_event_id &&
            item.node.kind === 'resource' &&
            item.node.branch_id !== branch.id
          "
          :disabled="
            unavailable ||
            dirty ||
            plan.stale ||
            plan.source_state === 'SUPERSEDED'
          "
          @click="usePlan(item.node.branch_id)"
        >
          到此内容所属分支处理
        </button>
        <p v-for="via in item.via" :key="key(via.source) + via.kind">
          {{ title(via.source) }} → {{ dependencyNames[via.kind] }}：{{
            via.reason
          }}
        </p>
      </article>
      <details>
        <summary>缺少依赖记录：{{ plan.untracked.length }} 项</summary>
        <p v-for="r in plan.untracked" :key="key(r)">{{ title(r) }}</p>
      </details>
      <details>
        <summary>
          未进入已记录传播范围：{{ plan.outside_recorded_closure.length }} 项
        </summary>
        <p v-for="r in plan.outside_recorded_closure" :key="key(r)">
          {{ title(r) }}
        </p>
      </details>
    </div>
  </details>
</template>

<style scoped>
.dependency-panel {
  margin: 1rem 0;
  padding: 1rem;
  border: 1px solid var(--border, #d7d6ce);
  border-radius: 12px;
}
.check-line {
  display: flex;
  align-items: center;
  gap: 0.6rem;
  margin: 0.8rem 0;
}
.check-line input {
  width: auto;
}
form {
  margin: 1rem 0;
}
table {
  width: 100%;
  border-collapse: collapse;
  margin: 0.8rem 0;
  table-layout: fixed;
}
th,
td {
  padding: 0.7rem;
  border: 1px solid #d7d6ce;
  text-align: left;
  vertical-align: top;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
