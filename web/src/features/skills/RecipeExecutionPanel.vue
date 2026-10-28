<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, toRaw, watch } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import type { CapabilityManifest, CapabilityVersion } from "./capabilityTypes";
import CapabilityInputForm from "./CapabilityInputForm.vue";
import ExpiredTaskHistory from "../../components/ExpiredTaskHistory.vue";
import {
  recipeStates,
  type RecipeRun,
  type RecipeStage,
  type RecipeStageInput,
} from "./recipeTypes";
const props = defineProps<{
  actor: Actor;
  project: Project;
  capability: CapabilityVersion;
  manifest: CapabilityManifest;
}>();
const emit = defineEmits<{
  expired: [];
  blocking: [boolean];
  busy: [boolean];
}>();
type Option = {
  role: string;
  name: string;
  model: string;
  price: { id: string; currency: string };
};
type Binding = {
  id: string;
  task_kind: string;
  current: boolean;
  valid: boolean;
  enabled: boolean;
  qualification?: {
    frozen: {
      candidate: CapabilityVersion;
      input: { max_output_tokens: number };
    };
  };
};
type StageForm = RecipeStageInput & { input_text: string };
const root = `/projects/${props.project.id}/recipe-executions`;
const storageKey = `recipe-draft:${props.actor.id}:${props.project.id}:${props.capability.id}`;
const branches = ref<{ id: string; name: string; head: string }[]>([]);
const branchID = ref("");
const options = ref<Option[]>([]);
const bindings = ref<Binding[]>([]);
const trial = ref(false);
const rootInput = ref("{}");
const currency = ref("USD");
const budget = ref("");
const operation = ref(newOperationID());
const stages = ref<StageForm[]>([]);
const runs = ref<RecipeRun[]>([]);
const expiredRuns = ref<RecipeRun[]>([]);
const selected = ref<RecipeRun | null>(null);
const busy = ref(false),
  error = ref(""),
  notice = ref("");
const retryStage = ref<RecipeStage | null>(null),
  retryText = ref(""),
  retryOperation = ref<string>(newOperationID());
const retryRevision = ref(0);
const retrySettings = ref<StageForm | null>(null);
const baseline = ref("");
const formText = computed(() =>
  JSON.stringify({
    branchID: branchID.value,
    trial: trial.value,
    rootInput: rootInput.value,
    currency: currency.value,
    budget: budget.value,
    stages: stages.value,
  }),
);
const dirty = computed(
  () => formText.value !== baseline.value || retryStage.value !== null,
);
const editable = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const ready = computed(() =>
  props.manifest.findings.every((f) => f.code === "QUALIFICATION_REQUIRED"),
);
const activeSteps = computed(
  () => selected.value?.steps?.filter((s) => s.active) ?? [],
);
const oldSteps = computed(
  () => selected.value?.steps?.filter((s) => !s.active) ?? [],
);
let disposed = false,
  restoring = true,
  reading = false,
  sequence = 0;
let timer: ReturnType<typeof setInterval> | undefined;
function agent(key: string) {
  const stage = props.capability.definition.steps?.find((s) => s.key === key);
  return props.manifest.versions.find((v) => v.id === stage?.agent_id);
}
function availableBindings(key: string) {
  return bindings.value.filter(
    (b) =>
      b.current &&
      b.enabled &&
      b.valid &&
      b.qualification?.frozen.candidate.id === agent(key)?.id,
  );
}
function selectedBinding(stage: StageForm) {
  const b = bindings.value.find((b) => b.id === stage.binding_id);
  stage.task_kind = b?.task_kind ?? "";
  if (b?.qualification)
    stage.max_output_tokens = b.qualification.frozen.input.max_output_tokens;
}
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function perform(work: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  ++sequence;
  try {
    await work();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    busy.value = false;
  }
}
function persist() {
  if (restoring || disposed) return;
  try {
    sessionStorage.setItem(
      storageKey,
      JSON.stringify({
        form: formText.value,
        operation: operation.value,
        selected: selected.value?.id ?? "",
        retry:
          retryStage.value && retrySettings.value
            ? {
                run: selected.value?.id,
                step: retryStage.value.id,
                revision: retryRevision.value,
                text: retryText.value,
                settings: retrySettings.value,
                operation: retryOperation.value,
              }
            : null,
      }),
    );
  } catch {
    notice.value = "此浏览器暂不能保存输入，离开前请先准备任务。";
  }
}
watch(
  formText,
  () => {
    if (!restoring) operation.value = newOperationID();
    persist();
  },
  { flush: "sync" },
);
watch(selected, persist);
watch([dirty, busy], () => emit("blocking", dirty.value || busy.value));
watch(busy, () => emit("busy", busy.value));
watch(
  [retryText, retrySettings],
  () => {
    if (!restoring) retryOperation.value = newOperationID();
    persist();
  },
  { deep: true, flush: "sync" },
);
watch(retryStage, persist, { flush: "sync" });
function money(
  amount: number,
  unit = selected.value?.currency ?? currency.value,
) {
  return `${(amount / 1000000).toFixed(6)} ${unit}`;
}
function limitMicros() {
  const match = /^(0|[1-9]\d{0,6})(?:\.(\d{1,6}))?$/.exec(budget.value.trim());
  if (!match) throw new Error("填写最多六位小数的任务费用上限。");
  const amount =
    Number(match[1]) * 1000000 + Number((match[2] ?? "").padEnd(6, "0"));
  if (amount < 1 || amount > 1000000000000)
    throw new Error("任务费用上限超出允许范围。");
  return amount;
}
async function read(id: string) {
  const token = ++sequence;
  try {
    const result = await api<RecipeRun>(`${root}/${id}`);
    if (!disposed && token === sequence) selected.value = result;
  } catch (e) {
    if (
      !disposed &&
      token === sequence &&
      e instanceof APIError &&
      e.status === 410
    ) {
      selected.value = null;
      retryStage.value = null;
    }
    throw e;
  }
}
async function refresh() {
  if (!branchID.value) return;
  const branch = branchID.value;
  const result = await api<{ items: RecipeRun[] }>(
    `${root}?branch_id=${branch}`,
  );
  if (!disposed && branchID.value === branch) {
    expiredRuns.value = result.items.filter((r) => r.retention);
    if (expiredRuns.value.some((r) => r.id === selected.value?.id)) {
      ++sequence;
      selected.value = null;
      retryStage.value = null;
    }
    runs.value = result.items.filter(
      (r) => !r.retention && r.frozen.definition.id === props.capability.id,
    );
  }
}
async function changeBranch() {
  ++sequence;
  selected.value = null;
  retryStage.value = null;
  await perform(refresh);
}
async function prepare() {
  await perform(async () => {
    const branch = branches.value.find((b) => b.id === branchID.value);
    if (!branch) throw new Error("先选择作品分支。");
    const result = await api<RecipeRun>(root, "POST", {
      recipe_version_id: props.capability.id,
      branch_id: branch.id,
      expected_ip_head: branch.head,
      operation_id: operation.value,
      trial: trial.value,
      input_text: rootInput.value,
      currency: currency.value,
      budget_limit_micros: limitMicros(),
      steps: stages.value.map((s) => ({
        ...s,
        input: undefined,
        binding_id: trial.value ? "" : s.binding_id,
        task_kind: trial.value ? "" : s.task_kind,
      })),
    });
    if (disposed) return;
    selected.value = result;
    baseline.value = formText.value;
    notice.value = "任务和输入已保存，尚未调用模型。";
    await refresh();
  });
}
async function command(action: string, payload: Record<string, unknown> = {}) {
  const snapshot = selected.value;
  if (!snapshot) return;
  await perform(async () => {
    const result = await api<RecipeRun>(
      `${root}/${snapshot.id}/${action}`,
      "POST",
      { expected_revision: snapshot.revision, ...payload },
    );
    if (disposed || selected.value?.id !== snapshot.id) return;
    selected.value = result;
    await refresh();
  });
}
async function rework(stage: RecipeStage) {
  const runID = selected.value?.id;
  if (!runID) return;
  await perform(async () => {
    await read(runID);
    const current = selected.value?.steps?.find(
      (s) => s.id === stage.id && s.active,
    );
    if (!current || current.input_text === undefined)
      throw new Error("步骤已更新，请刷新记录后重新选择。");
    retryRevision.value = selected.value!.revision;
    retryText.value = current.input_text;
    retrySettings.value = {
      ...structuredClone(toRaw(current.frozen.input)),
      bindings: structuredClone(toRaw(current.frozen.input.bindings ?? [])),
      input: undefined,
      input_text: current.input_text,
    };
    retryOperation.value = newOperationID();
    retryStage.value = current;
  });
}
async function submitRework() {
  const target = retryStage.value,
    run = selected.value;
  if (!target || !run || !retrySettings.value) return;
  await perform(async () => {
    const result = await api<RecipeRun>(`${root}/${run.id}/retry`, "POST", {
      expected_revision: retryRevision.value,
      operation_id: retryOperation.value,
      step: {
        ...retrySettings.value,
        input: undefined,
        input_text: retryText.value,
      },
    });
    if (disposed || selected.value?.id !== run.id) return;
    retryStage.value = null;
    selected.value = result;
    await refresh();
  });
}
function addRoute(stage: StageForm) {
  const definition = props.capability.definition.steps?.find(
    (s) => s.key === stage.key,
  );
  stage.bindings.push({
    source_step: definition?.after[0] ?? "",
    source_pointer: "/summary",
    target_pointer: "/source",
  });
}
onMounted(
  () =>
    void perform(async () => {
      const [b, o, q] = await Promise.all([
        api<{ items: typeof branches.value }>(
          `/projects/${props.project.id}/branches`,
        ),
        api<{ items: Option[] }>(
          `/projects/${props.project.id}/generation-options`,
        ),
        api<{ items: Binding[] }>(
          `/projects/${props.project.id}/qualification/bindings`,
        ),
      ]);
      if (disposed) return;
      branches.value = b.items;
      options.value = o.items;
      bindings.value = q.items;
      branchID.value = b.items[0]?.id ?? "";
      stages.value = (props.capability.definition.steps ?? []).map((step) => {
        const option = options.value.find(
          (o) => o.role === agent(step.key)?.definition.role,
        );
        return {
          key: step.key,
          price_id: option?.price.id ?? "",
          max_output_tokens: 1024,
          context_id: "",
          binding_id: "",
          task_kind: "",
          input_text: "{}",
          bindings: [],
        };
      });
      let priorID = "";
      let priorRetry: {
        run: string;
        step: string;
        revision: number;
        text: string;
        settings: StageForm;
        operation: string;
      } | null = null;
      try {
        const raw = sessionStorage.getItem(storageKey);
        if (raw) {
          const prior = JSON.parse(raw),
            form = JSON.parse(prior.form);
          if (
            Array.isArray(form.stages) &&
            form.stages.length === stages.value.length &&
            form.stages.every(
              (s: StageForm, i: number) => s.key === stages.value[i]?.key,
            )
          ) {
            branchID.value = form.branchID;
            trial.value = form.trial;
            rootInput.value = form.rootInput;
            currency.value = form.currency;
            budget.value = form.budget;
            stages.value = form.stages;
            operation.value = prior.operation;
            priorID = prior.selected;
            priorRetry = prior.retry ?? null;
          }
        }
      } catch {
        notice.value = "暂存内容不可读取，请重新填写。";
      }
      baseline.value = formText.value;
      restoring = false;
      await refresh();
      if (priorID) await read(priorID);
      if (priorRetry && priorRetry.run === selected.value?.id) {
        const stage = selected.value?.steps?.find(
          (s) => s.id === priorRetry!.step,
        );
        if (stage) {
          restoring = true;
          retryRevision.value = priorRetry.revision;
          retryText.value = priorRetry.text;
          retrySettings.value = priorRetry.settings;
          retryOperation.value = priorRetry.operation;
          retryStage.value = stage;
          restoring = false;
          persist();
        }
      }
      timer = setInterval(() => {
        if (
          disposed ||
          reading ||
          busy.value ||
          !selected.value ||
          retryStage.value
        )
          return;
        reading = true;
        void read(selected.value.id)
          .catch((e) => {
            if (!disposed) fail(e);
          })
          .finally(() => {
            reading = false;
          });
      }, 2000);
    }),
);
onBeforeUnmount(() => {
  persist();
  disposed = true;
  ++sequence;
  if (timer) clearInterval(timer);
  emit("blocking", false);
  emit("busy", false);
});
</script>
<template>
  <section
    class="recipe-execution"
    aria-label="工作流执行"
    data-testid="recipe-execution"
  >
    <h3>执行此工作流</h3>
    <p>
      各步骤只读取你指定的资料与交接内容。结果先供检查，确认不会直接发布设定或正文。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <p v-if="!ready" role="alert">
      当前能力清单有待解决的问题，暂不能准备新任务。
    </p>
    <form @submit.prevent="prepare">
      <fieldset :disabled="busy || !editable || !ready">
        <label
          >执行分支<select v-model="branchID" required @change="changeBranch">
            <option v-for="b in branches" :key="b.id" :value="b.id">
              {{ b.name }}
            </option>
          </select></label
        >
        <label
          ><input
            v-model="trial"
            type="checkbox"
          />本次为显式试运行，不作为正式质量资格</label
        >
        <CapabilityInputForm
          v-model="rootInput"
          :schema="capability.definition.input_schema"
          label="本次工作流资料"
        />
        <label
          >任务费用币种<select v-model="currency">
            <option>USD</option>
            <option>CNY</option>
          </select></label
        >
        <label
          >整个任务的费用上限<input
            v-model="budget"
            inputmode="decimal"
            placeholder="包含重做和未知占用"
            required
        /></label>
        <fieldset
          v-for="stage in stages"
          :key="stage.key"
          :aria-label="`执行步骤 ${stage.key}`"
        >
          <legend>
            {{ agent(stage.key)?.definition.name }} · {{ stage.key }}
          </legend>
          <label
            >阶段模型<select v-model="stage.price_id" required>
              <option value="" disabled>先在模型设置配置此角色及价格</option>
              <option
                v-for="o in options.filter(
                  (o) => o.role === agent(stage.key)?.definition.role,
                )"
                :key="o.price.id"
                :value="o.price.id"
              >
                {{ o.name }} · {{ o.model }} · {{ o.price.currency }}
              </option>
            </select></label
          >
          <label v-if="!trial"
            >阶段用途资格<select
              v-model="stage.binding_id"
              required
              @change="selectedBinding(stage)"
            >
              <option value="" disabled>选择有效的正式用途绑定</option>
              <option
                v-for="b in availableBindings(stage.key)"
                :key="b.id"
                :value="b.id"
              >
                {{ b.task_kind }}
              </option>
            </select></label
          >
          <label
            >阶段输出上限<input
              v-model.number="stage.max_output_tokens"
              type="number"
              min="1"
              max="131072"
              required
          /></label>
          <CapabilityInputForm
            v-model="stage.input_text"
            :schema="agent(stage.key)?.definition.input_schema ?? {}"
            :label="`步骤 ${stage.key} 资料`"
          />
          <details>
            <summary>指定交接内容与资料包</summary>
            <p>
              只允许从此步骤声明的前序读取内容。路径使用
              /字段名；目标字段须先在资料中填写或留空。
            </p>
            <label>固定资料包编号<input v-model="stage.context_id" /></label>
            <div
              v-for="(route, index) in stage.bindings"
              :key="index"
              class="route"
            >
              <label
                >前序步骤<select v-model="route.source_step" required>
                  <option
                    v-for="key in capability.definition.steps?.find(
                      (s) => s.key === stage.key,
                    )?.after ?? []"
                    :key="key"
                  >
                    {{ key }}
                  </option>
                </select></label
              >
              <label
                >读取内容路径<input
                  v-model="route.source_pointer"
                  required /></label
              ><label
                >交给此步骤的字段路径<input
                  v-model="route.target_pointer"
                  required
              /></label>
              <button
                type="button"
                class="quiet"
                @click="stage.bindings.splice(index, 1)"
              >
                移除此交接
              </button>
            </div>
            <button
              v-if="
                capability.definition.steps?.find((s) => s.key === stage.key)
                  ?.after.length
              "
              type="button"
              @click="addRoute(stage)"
            >
              增加交接内容
            </button>
          </details>
        </fieldset>
        <button>保存并准备工作流</button>
      </fieldset>
    </form>
    <div class="actions">
      <ExpiredTaskHistory scope="当前分支流程" :items="expiredRuns" />
      <button class="quiet" :disabled="busy" @click="perform(refresh)">
        刷新工作流记录</button
      ><button
        v-for="run in runs"
        :key="run.id"
        class="quiet"
        :disabled="busy || !!retryStage"
        @click="perform(() => read(run.id))"
      >
        {{ recipeStates[run.state] ?? run.state }} · {{ run.id.slice(0, 8) }}
      </button>
    </div>
    <article v-if="selected" data-testid="recipe-run">
      <h4>
        {{
          selected.paused
            ? "已暂停"
            : (recipeStates[selected.state] ?? selected.state)
        }}
      </h4>
      <p>
        已发生 {{ money(selected.spent_micros) }} · 待结算占用
        {{ money(selected.held_micros) }} · 本任务上限
        {{ money(selected.budget_limit_micros) }}
      </p>
      <p v-if="selected.reason">{{ selected.reason }}</p>
      <p v-if="selected.uncertain_calls > 0">
        有
        {{ selected.uncertain_calls }}
        次调用尚未核清，当前不能重做。请先在费用记录中核对账单。
      </p>
      <p v-if="selected.frozen.trial">试运行记录，未取得正式质量资格。</p>
      <div
        v-if="editable && !selected.restored_disabled && !retryStage"
        class="actions"
      >
        <button
          v-if="selected.state === 'PREPARED'"
          :disabled="busy"
          @click="command('start')"
        >
          按任务上限启动
        </button>
        <template v-if="!['COMPLETED', 'CANCELLED'].includes(selected.state)">
          <button
            :disabled="busy"
            @click="
              command('control', {
                action: selected.paused ? 'resume' : 'pause',
              })
            "
          >
            {{ selected.paused ? "继续工作流" : "暂停工作流" }}
          </button>
          <button
            :disabled="busy"
            @click="command('control', { action: 'cancel' })"
          >
            取消工作流
          </button>
        </template>
      </div>
      <article
        v-for="step in activeSteps"
        :key="step.id"
        class="stage-result"
        :data-step="step.key"
      >
        <h4>
          {{ step.frozen.agent.definition.name }} · {{ step.key }} · 第
          {{ step.attempt }} 次
        </h4>
        <p>
          {{ recipeStates[step.state] ?? step.state }} ·
          {{ step.frozen.config.name }}
        </p>
        <pre v-if="step.output_text">{{ step.output_text }}</pre>
        <button
          v-if="
            editable &&
            step.state === 'WAITING_CONFIRMATION' &&
            !selected.restored_disabled
          "
          :disabled="busy || selected.paused || !!retryStage"
          @click="
            command('confirm', {
              step_key: step.key,
              output_hash: step.output_hash,
            })
          "
        >
          确认此步骤并继续
        </button>
        <button
          v-if="
            editable &&
            !selected.restored_disabled &&
            !selected.cancel_requested &&
            step.state !== 'WAITING' &&
            (selected.state === 'BLOCKED' ||
              !['PREPARING', 'RUNNING'].includes(step.state))
          "
          class="quiet"
          :disabled="
            busy ||
            selected.paused ||
            !!retryStage ||
            selected.uncertain_calls > 0
          "
          @click="rework(step)"
        >
          修改资料并重做 {{ step.key }}
        </button>
      </article>
      <form v-if="retryStage && retrySettings" @submit.prevent="submitRework">
        <h4>重做 {{ retryStage.key }}</h4>
        <p>旧稿与费用保留；依赖此步骤的后续结果需要重新生成和确认。</p>
        <fieldset :disabled="busy">
          <label
            >重做模型<select v-model="retrySettings.price_id" required>
              <option :value="retryStage.frozen.input.price_id">
                沿用原模型 · {{ retryStage.frozen.config.name }} ·
                {{ retryStage.frozen.config.model }}
              </option>
              <option
                v-for="o in options.filter(
                  (o) =>
                    o.role === retryStage!.frozen.agent.definition.role &&
                    o.price.id !== retryStage!.frozen.input.price_id,
                )"
                :key="o.price.id"
                :value="o.price.id"
              >
                {{ o.name }} · {{ o.model }} · {{ o.price.currency }}
              </option>
            </select></label
          >
          <label v-if="!selected.frozen.trial"
            >重做用途资格<select
              v-model="retrySettings.binding_id"
              required
              @change="selectedBinding(retrySettings)"
            >
              <option
                v-for="b in availableBindings(retryStage.key)"
                :key="b.id"
                :value="b.id"
              >
                {{ b.task_kind }}
              </option>
            </select></label
          >
          <label
            >重做输出上限<input
              v-model.number="retrySettings.max_output_tokens"
              type="number"
              min="1"
              max="131072"
              required
          /></label>
          <label
            >重做资料包编号<input v-model="retrySettings.context_id"
          /></label>
          <fieldset
            v-if="
              capability.definition.steps?.find(
                (s) => s.key === retryStage!.key,
              )?.after.length
            "
            aria-label="重做交接内容"
          >
            <legend>重做交接内容</legend>
            <div
              v-for="(route, index) in retrySettings.bindings"
              :key="index"
              class="route"
            >
              <label
                >重做前序步骤<select v-model="route.source_step" required>
                  <option
                    v-for="key in capability.definition.steps?.find(
                      (s) => s.key === retryStage!.key,
                    )?.after ?? []"
                    :key="key"
                  >
                    {{ key }}
                  </option>
                </select></label
              >
              <label
                >重做读取内容路径<input v-model="route.source_pointer" required
              /></label>
              <label
                >重做接收字段路径<input v-model="route.target_pointer" required
              /></label>
              <button
                type="button"
                class="quiet"
                @click="retrySettings.bindings.splice(index, 1)"
              >
                移除此重做交接
              </button>
            </div>
            <button type="button" @click="addRoute(retrySettings)">
              增加重做交接内容
            </button>
          </fieldset>
          <CapabilityInputForm
            v-model="retryText"
            :schema="retryStage.frozen.agent.definition.input_schema"
            label="重做资料"
          /><button>保存修改并重做此步骤</button
          ><button type="button" class="quiet" @click="retryStage = null">
            放弃重做修改
          </button>
        </fieldset>
      </form>
      <details v-if="oldSteps.length">
        <summary>查看 {{ oldSteps.length }} 条历史尝试</summary>
        <article v-for="step in oldSteps" :key="step.id">
          <h4>{{ step.key }} · 第 {{ step.attempt }} 次</h4>
          <pre>{{ step.output_text }}</pre>
        </article>
      </details>
    </article>
  </section>
</template>
<style scoped>
.recipe-execution {
  margin-top: 1.5rem;
  border-top: 1px solid #ddd;
  padding-top: 1rem;
}
fieldset {
  min-width: 0;
  margin: 0.75rem 0;
}
.stage-result,
.route {
  border-bottom: 1px solid #ddd;
  padding: 0.75rem 0;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
</style>
