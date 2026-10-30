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
import ToolTrialsPanel from "./ToolTrialsPanel.vue";
import RecipeExecutionPanel from "./RecipeExecutionPanel.vue";
import {
  capabilityKinds,
  capabilityRoles,
  type CapabilityDefinition,
  type CapabilityManifest,
  type CapabilityVersion,
} from "./capabilityTypes";
const props = defineProps<{ actor: Actor; project: Project }>();
type TaskAttention = { unknown: number; review: number; active: number };
const emit = defineEmits<{
  expired: [];
  blocking: [boolean];
  installed: [];
  attention: [value: TaskAttention];
}>();
const root = `/projects/${props.project.id}/capabilities`;
const items = ref<CapabilityVersion[]>([]);
const cursor = ref("");
const packages = ref<
  { id: string; available: boolean; report: { name: string; hash: string } }[]
>([]);
const selected = ref<CapabilityVersion | null>(null);
const manifest = ref<CapabilityManifest | null>(null);
const draft = ref<CapabilityDefinition | null>(null);
const schemaIn = ref("");
const schemaOut = ref("");
const baseline = ref("");
const busy = ref(false);
const toolBlocking = ref(false);
const toolBusy = ref(false);
const error = ref("");
const notice = ref("");
const reason = ref("");
const op = ref(newOperationID());
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const canRevoke = computed(
  () => props.actor.role === "owner" && props.project.role === "owner",
);
const currentText = computed(() =>
  JSON.stringify([draft.value, schemaIn.value, schemaOut.value]),
);
const dirty = computed(
  () => draft.value !== null && currentText.value !== baseline.value,
);
const dependencies = computed(() =>
  items.value.filter((v) => {
    if (v.retention) return false;
    const kind = draft.value?.kind;
    return kind === "agent"
      ? ["skill", "tool"].includes(v.definition.kind)
      : kind === "skill"
        ? v.definition.kind === "tool"
        : kind === "recipe" && v.definition.kind === "agent";
  }),
);
const ordered = computed(() =>
  [...items.value].sort(
    (a, b) =>
      b.created_at.localeCompare(a.created_at) || b.id.localeCompare(a.id),
  ),
);
const query = ref("");
const kindFilter = ref("");
const filtered = computed(() => {
  const text = query.value.trim().toLocaleLowerCase();
  return ordered.value.filter(
    (version) =>
      (!kindFilter.value ||
        (!version.retention && version.definition.kind === kindFilter.value)) &&
      (!text ||
        (version.retention
          ? [version.id, "已过期的能力版本"]
          : [
              version.id,
              version.definition.name,
              version.definition.description,
            ]
        ).some((value) => value.toLocaleLowerCase().includes(text))),
  );
});
const executionAttention = ref<
  Record<string, TaskAttention & { name: string }>
>({});
const attention = computed(() =>
  Object.values(executionAttention.value).reduce(
    (total, item) => ({
      unknown: total.unknown + item.unknown,
      review: total.review + item.review,
      active: total.active + item.active,
    }),
    { unknown: 0, review: 0, active: 0 },
  ),
);
watch(attention, (value) => emit("attention", value), { immediate: true });
function rememberExecution(
  name: string,
  value: TaskAttention & { scope: string },
) {
  executionAttention.value = {
    ...executionAttention.value,
    [value.scope]: { name, ...value },
  };
}
let disposed = false;
watch(currentText, () => {
  op.value = newOperationID();
});
watch(
  [dirty, busy, toolBlocking],
  () => emit("blocking", dirty.value || busy.value || toolBlocking.value),
  {
    immediate: true,
  },
);
function leave() {
  return (
    !busy.value &&
    !toolBusy.value &&
    (!(dirty.value || toolBlocking.value) ||
      window.confirm("放弃尚未保存的能力或工具输入修改？"))
  );
}
async function perform(work: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
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
  const result = await api<{ items: CapabilityVersion[]; next_cursor: string }>(
    `${root}?after=${more ? cursor.value : ""}`,
  );
  if (disposed) return;
  items.value = more ? [...items.value, ...result.items] : result.items;
  cursor.value = result.next_cursor;
}
function edit(v: CapabilityVersion) {
  if (v.retention) return;
  selected.value = v;
  draft.value = JSON.parse(
    JSON.stringify(v.definition),
  ) as CapabilityDefinition;
  schemaIn.value = v.input_schema_text;
  schemaOut.value = v.output_schema_text;
  baseline.value = currentText.value;
  reason.value = "";
}
async function inspect(v: CapabilityVersion) {
  if (v.retention) return;
  if (!leave()) return;
  await perform(async () => {
    const result = await api<CapabilityManifest>(`${root}/${v.id}/manifest`);
    if (disposed) return;
    edit(v);
    manifest.value = result;
  });
}
function create(kind: string) {
  if (!leave()) return;
  const input = {
    type: "object",
    properties: {
      objective: { type: "string", title: "本次目标" },
      materials: { type: "string", title: "资料与来源" },
    },
    required: ["objective", "materials"],
    additionalProperties: false,
  };
  const output = {
    type: "object",
    properties: {
      summary: { type: "string" },
      issues: { type: "array", items: { type: "string" } },
    },
    required: ["summary", "issues"],
    additionalProperties: false,
  };
  selected.value = null;
  manifest.value = null;
  draft.value = {
    kind,
    key: "",
    name: "",
    description: "",
    instructions: "",
    input_schema: input,
    output_schema: output,
    dependencies: [],
  };
  if (kind === "agent") draft.value.role = "researcher";
  if (["skill", "tool"].includes(kind)) {
    draft.value.package_id = "";
    draft.value.package_hash = "";
  }
  if (kind === "tool")
    draft.value.tool = {
      runtime: "linux-python",
      entrypoint: "",
      timeout_seconds: 15,
      memory_mib: 256,
      network: false,
    };
  if (kind === "recipe") {
    draft.value.steps = [];
    draft.value.max_automatic_revisions = 2;
  }
  schemaIn.value = JSON.stringify(input, null, 2);
  schemaOut.value = JSON.stringify(output, null, 2);
  baseline.value = "";
}
function choosePackage() {
  if (!draft.value) return;
  const p = packages.value.find((v) => v.id === draft.value?.package_id);
  draft.value.package_hash = p?.report.hash ?? "";
}
function toggleDependency(v: CapabilityVersion, event: Event) {
  if (!draft.value) return;
  if ((event.target as HTMLInputElement).checked)
    draft.value.dependencies.push({
      id: v.id,
      hash: v.hash,
      kind: v.definition.kind,
    });
  else
    draft.value.dependencies = draft.value.dependencies.filter(
      (ref) => ref.id !== v.id,
    );
}
function addStep() {
  const steps = draft.value?.steps;
  if (!steps) return;
  steps.push({
    key: `step-${steps.length + 1}`,
    agent_id: "",
    after: steps.length ? [steps[steps.length - 1]!.key] : [],
    wait_for_confirmation: true,
  });
}
async function save() {
  if (toolBlocking.value) return;
  await perform(async () => {
    if (!draft.value) return;
    const definition = {
      ...draft.value,
      input_schema: {},
      output_schema: {},
    };
    const v = await api<CapabilityVersion>(root, "POST", {
      operation_id: op.value,
      expected_head: selected.value?.id ?? "",
      definition,
      input_schema_text: schemaIn.value,
      output_schema_text: schemaOut.value,
    });
    if (disposed) return;
    edit(v);
    manifest.value = await api<CapabilityManifest>(`${root}/${v.id}/manifest`);
    await refresh();
    notice.value =
      "新能力版本已保存，旧版本及其依赖保持固定。尚未授予生产资格。";
  });
}
async function install(preset: "research" | "character-visual") {
  if (!leave()) return;
  await perform(async () => {
    const result = await api<{ root_id: string }>(
      `${root}/presets/${preset}`,
      "POST",
      preset === "research" ? { operation_id: newOperationID() } : {},
    );
    const v = await api<CapabilityVersion>(`${root}/${result.root_id}`);
    const m = await api<CapabilityManifest>(
      `${root}/${result.root_id}/manifest`,
    );
    if (disposed) return;
    edit(v);
    manifest.value = m;
    packages.value = (
      await api<{ items: typeof packages.value }>(
        `/projects/${props.project.id}/skills`,
      )
    ).items;
    await refresh();
    emit("installed");
    notice.value =
      preset === "research"
        ? "四个研究角色、四份完整方法和工作流已就绪，均保留独立评测门。重复准备不会覆盖你的编辑。"
        : "三视图视觉方案助手、方法和候选工作流已准备，可编辑后试用；此步骤不生成图片、不调用模型，也不自动采用造型。";
  });
}
async function revoke() {
  await perform(async () => {
    if (!selected.value) return;
    await api(`${root}/${selected.value.id}/revoke`, "POST", {
      reason: reason.value,
    });
    manifest.value = await api<CapabilityManifest>(
      `${root}/${selected.value.id}/manifest`,
    );
    notice.value =
      "已撤销此版本；使用它的角色、工作流及派生版本会重新检查资格。历史内容保留。";
  });
}
function discard() {
  if (selected.value) edit(selected.value);
  else {
    draft.value = null;
    baseline.value = "";
  }
}
onMounted(
  () =>
    void perform(async () => {
      await refresh();
      const result = await api<{ items: typeof packages.value }>(
        `/projects/${props.project.id}/skills`,
      );
      if (!disposed) packages.value = result.items;
    }),
);
onBeforeUnmount(() => {
  disposed = true;
  emit("blocking", false);
});
</script>
<template>
  <section class="panel capability-panel">
    <header class="section-heading">
      <div>
        <p class="eyebrow">选择版本 · 编辑 · 检查与试用</p>
        <h2>专业角色与工作流</h2>
      </div>
    </header>
    <p class="author-page-description">
      角色定职责，方法定步骤，工具单独受控，工作流组织顺序。编辑形成新版本；旧任务不会悄悄改用新方法。
    </p>
    <div v-if="canEdit" class="actions">
      <button :disabled="busy || dirty" @click="install('research')">
        准备四个研究角色
      </button>
      <button :disabled="busy || dirty" @click="install('character-visual')">
        准备三视图视觉方案助手
      </button>
      <button
        v-for="(label, kind) in capabilityKinds"
        :key="kind"
        :disabled="busy || dirty"
        class="quiet"
        @click="create(kind)"
      >
        新增{{ label }}
      </button>
    </div>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <aside
      v-if="attention.unknown || attention.review || attention.active"
      class="capability-attention"
    >
      <strong
        >已读取的执行记录：结果未知 {{ attention.unknown }} 项 · 等待确认
        {{ attention.review }} 项 · 进行中 {{ attention.active }} 项</strong
      >
      <p>
        不同能力或分支保留上次读取的状态。请选择对应能力核对任务；结果未知不会因切换版本而自动重发。
      </p>
      <p
        v-for="(item, key) in executionAttention"
        :key="key"
        v-show="item.unknown || item.review || item.active"
      >
        {{ item.name }}：结果未知 {{ item.unknown }} · 等待确认
        {{ item.review }} · 进行中 {{ item.active }}
      </p>
    </aside>
    <div class="capability-workspace">
      <aside class="capability-library" aria-label="能力版本库">
        <label
          >搜索能力<input
            v-model="query"
            type="search"
            aria-label="搜索能力版本"
            placeholder="名称、任务或版本编号"
        /></label>
        <label
          >能力类型<select v-model="kindFilter" aria-label="筛选能力类型">
            <option value="">全部类型</option>
            <option
              v-for="(name, kind) in capabilityKinds"
              :key="kind"
              :value="kind"
            >
              {{ name }}
            </option>
          </select></label
        >
        <p class="muted">
          显示 {{ filtered.length }} / {{ items.length }} 个已读取版本{{
            cursor ? "；还有未加载版本" : ""
          }}
        </p>
        <div class="capability-list">
          <button
            v-for="v in filtered"
            :key="v.id"
            class="quiet capability-version"
            :aria-pressed="selected?.id === v.id"
            :disabled="busy || dirty || !!v.retention"
            @click="inspect(v)"
          >
            {{
              v.retention
                ? "已过期的能力版本 · 原定义已清理"
                : `${v.definition.name} · ${capabilityKinds[v.definition.kind]}`
            }}
            ·
            {{ v.id.slice(0, 8) }}
          </button>
          <button
            v-if="cursor"
            :disabled="busy"
            @click="perform(() => refresh(true))"
          >
            加载更多能力版本
          </button>
        </div>
      </aside>
      <div class="capability-detail">
        <p v-if="!draft" class="capability-empty">
          选择一个已保存版本核对职责与完整清单，或从上方新增候选。编辑会另存版本，试用与正式用途资格分别核对。
        </p>
        <template v-if="draft">
          <p>
            {{ capabilityKinds[draft.kind] }} ·
            {{ selected ? `版本 ${selected.id.slice(0, 8)}` : "新候选" }}
          </p>
          <form @submit.prevent="save">
            <fieldset :disabled="busy || !canEdit">
              <label
                >能力名称<input v-model="draft.name" required maxlength="80"
              /></label>
              <label
                >稳定标识<input
                  v-model="draft.key"
                  :disabled="!!selected"
                  required
                  pattern="[a-z][a-z0-9-]{1,79}"
                  placeholder="例如 story-review"
              /></label>
              <label
                >适用任务<textarea
                  v-model="draft.description"
                  required
                  rows="2"
                />
              </label>
              <label
                >职责与执行方法<textarea
                  v-model="draft.instructions"
                  required
                  rows="7"
                />
              </label>
              <label v-if="draft.kind === 'agent'"
                >模型用途<select v-model="draft.role" aria-label="模型用途">
                  <option
                    v-for="(label, role) in capabilityRoles"
                    :key="role"
                    :value="role"
                  >
                    {{ label }}
                  </option>
                </select></label
              >
              <label v-if="['skill', 'tool'].includes(draft.kind)"
                >固定方法包<select
                  v-model="draft.package_id"
                  required
                  @change="choosePackage"
                >
                  <option value="" disabled>选择已导入的完整包</option>
                  <option
                    v-for="p in packages"
                    :key="p.id"
                    :value="p.id"
                    :disabled="!p.available"
                  >
                    {{ p.report.name }} · {{ p.id.slice(0, 8) }}
                  </option>
                </select></label
              >
              <fieldset v-if="draft.tool">
                <legend>工具请求范围（保存不等于执行授权）</legend>
                <label
                  >解释器<select v-model="draft.tool.runtime">
                    <option value="linux-python">Python</option>
                    <option value="linux-posix">Linux 命令脚本</option>
                  </select></label
                >
                <label
                  >包内入口文件<input
                    v-model="draft.tool.entrypoint"
                    placeholder="scripts/check.py"
                    required
                /></label>
                <label
                  >最长执行秒数<input
                    v-model.number="draft.tool.timeout_seconds"
                    type="number"
                    min="1"
                    max="120"
                    required
                /></label>
                <label
                  >请求内存 MB<input
                    v-model.number="draft.tool.memory_mib"
                    type="number"
                    min="64"
                    max="2048"
                    required
                /></label>
                <p>默认不联网；实际运行还须满足隔离环境更严格的上限。</p>
              </fieldset>
              <fieldset v-if="draft.kind !== 'tool'">
                <legend>固定依赖版本</legend>
                <label v-for="dep in dependencies" :key="dep.id"
                  ><input
                    type="checkbox"
                    :checked="
                      draft.dependencies.some((ref) => ref.id === dep.id)
                    "
                    @change="toggleDependency(dep, $event)"
                  />{{ dep.definition.name }} · {{ dep.id.slice(0, 8) }}</label
                >
                <p v-if="!dependencies.length">
                  暂无可选依赖，先创建方法或角色。
                </p>
                <p
                  v-for="dep in draft.dependencies.filter(
                    (ref) => !dependencies.some((v) => v.id === ref.id),
                  )"
                  :key="dep.id"
                >
                  已固定未加载的版本 {{ dep.id }}；加载更多后可修改。
                </p>
              </fieldset>
              <fieldset v-if="draft.steps">
                <legend>按顺序交接，各角色使用独立输入</legend>
                <div
                  v-for="(step, index) in draft.steps"
                  :key="index"
                  class="recipe-step"
                >
                  <label>步骤标识<input v-model="step.key" required /></label>
                  <label
                    >执行角色<select v-model="step.agent_id" required>
                      <option value="" disabled>从已固定依赖选择</option>
                      <option
                        v-for="dep in draft.dependencies"
                        :key="dep.id"
                        :value="dep.id"
                      >
                        {{
                          items.find((v) => v.id === dep.id)?.definition.name ??
                          dep.id
                        }}
                      </option>
                    </select></label
                  >
                  <label
                    >前置步骤（逗号分隔）<input
                      :value="step.after.join(',')"
                      @input="
                        step.after = ($event.target as HTMLInputElement).value
                          .split(',')
                          .map((s) => s.trim())
                          .filter(Boolean)
                      "
                  /></label>
                  <label
                    ><input
                      v-model="step.wait_for_confirmation"
                      type="checkbox"
                    />交接前等待用户确认</label
                  >
                  <button
                    type="button"
                    class="quiet"
                    @click="draft.steps?.splice(index, 1)"
                  >
                    移除此步骤
                  </button>
                </div>
                <button type="button" @click="addStep">增加交接步骤</button>
                <label
                  >自动修订上限<input
                    v-model.number="draft.max_automatic_revisions"
                    type="number"
                    min="0"
                    max="2"
                /></label>
              </fieldset>
              <details>
                <summary>高级输入输出契约</summary>
                <p>
                  预设已填好；修改须使用有效的对象
                  Schema，不能引用网上文件。契约变更也产生新版本。
                </p>
                <label
                  >输入契约<textarea
                    v-model="schemaIn"
                    rows="8"
                    required
                  /></label
                ><label
                  >输出契约<textarea v-model="schemaOut" rows="8" required />
                </label>
              </details>
              <div class="actions">
                <button :disabled="!dirty || toolBlocking">
                  保存为新能力版本</button
                ><button type="button" class="quiet" @click="discard">
                  放弃能力修改
                </button>
              </div>
            </fieldset>
          </form>
          <details v-if="selected && dirty">
            <summary>比较原版本职责</summary>
            <pre>{{ selected.definition.instructions }}</pre>
          </details>
          <section v-if="manifest" class="notice">
            <h3>完整清单与检查结果</h3>
            <p>
              固定 {{ manifest.versions.length }} 个能力版本。{{
                selected?.definition.kind === "recipe"
                  ? "工作流可在下方检查执行条件并逐步运行。"
                  : manifest.trial_ready
                    ? "可进入受控文本试评。"
                    : "当前不能直接执行。"
              }}具体任务用途的资格由独立评测和绑定记录决定。
            </p>
            <ul>
              <li v-for="(f, index) in manifest.findings" :key="index">
                {{ f.message }}
              </li>
            </ul>
            <details>
              <summary>查看固定版本清单</summary>
              <ul>
                <li v-for="v in manifest.versions" :key="v.id">
                  {{ v.definition.name }} · {{ v.id }} · {{ v.hash }}
                </li>
              </ul>
            </details>
          </section>
          <details v-if="canRevoke && selected">
            <summary>撤销能力版本</summary>
            <label
              >能力撤销原因<input v-model="reason" maxlength="1000" /></label
            ><button
              :disabled="busy || dirty || !reason.trim()"
              @click="revoke"
            >
              撤销此能力版本
            </button>
          </details>
          <ToolTrialsPanel
            v-if="selected?.definition.kind === 'tool'"
            :key="selected.id"
            :actor="actor"
            :project="project"
            :capability="selected"
            :revoked="
              manifest?.findings.some((f) => f.code === 'REVOKED') ?? false
            "
            @expired="emit('expired')"
            @blocking="toolBlocking = $event"
            @busy="toolBusy = $event"
            @attention="rememberExecution(selected.definition.name, $event)"
          />
          <RecipeExecutionPanel
            v-if="selected?.definition.kind === 'recipe' && manifest && !dirty"
            :key="selected.id"
            :actor="actor"
            :project="project"
            :capability="selected"
            :manifest="manifest"
            @expired="emit('expired')"
            @blocking="toolBlocking = $event"
            @busy="toolBusy = $event"
            @attention="rememberExecution(selected.definition.name, $event)"
          />
        </template>
      </div>
    </div>
  </section>
</template>
<style scoped>
.capability-panel {
  margin-bottom: 1.5rem;
}
.capability-list {
  display: grid;
  gap: 0.5rem;
  margin: 1rem 0;
}
.capability-workspace {
  display: grid;
  grid-template-columns: minmax(240px, 0.8fr) minmax(0, 2fr);
  gap: 28px;
  margin-top: 24px;
}
.capability-library,
.capability-detail {
  min-width: 0;
}
.capability-library > label {
  margin-bottom: 14px;
}
.capability-detail {
  border-left: 1px solid #e1e5da;
  padding-left: 28px;
}
.capability-empty {
  padding: 24px;
  background: #f5f6ef;
  border-radius: 10px;
}
.capability-attention {
  margin: 20px 0;
  padding: 16px;
  background: #f6f2e7;
  border: 1px solid #d8d1b7;
  border-radius: 10px;
  font-size: 13px;
}
.capability-attention p {
  margin: 8px 0 0;
}
.capability-version[aria-pressed="true"] {
  border-color: #91ad98;
  background: #edf1e6;
}
.capability-detail > p:first-child {
  margin-top: 0;
}
@media (max-width: 800px) {
  .capability-workspace {
    grid-template-columns: minmax(0, 1fr);
  }
  .capability-detail {
    border-left: 0;
    padding-left: 0;
  }
}
.capability-version {
  text-align: left;
}
.recipe-step {
  border-bottom: 1px solid #ddd;
  padding: 0.8rem 0;
}
fieldset {
  min-width: 0;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
li {
  overflow-wrap: anywhere;
}
</style>
