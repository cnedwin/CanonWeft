<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { api, APIError, message } from "../../api";
import { useModelReads } from "./modelReads";
import ModelSetupChecklist from "./ModelSetupChecklist.vue";
import type { ModelSetupReport, SetupTarget } from "./modelSetup";
import BudgetPanel from "./BudgetPanel.vue";
import PricePanel from "./PricePanel.vue";
import ReconciliationPanel from "./ReconciliationPanel.vue";
const budgetPanel = ref<InstanceType<typeof BudgetPanel> | null>(null);
const accountingBlocking = ref(false);
const accountingAttention = ref({ count: 0, loaded: 0, hasMore: false });
const activeSection = ref<SetupTarget | "checklist">("connections");
const sections = [
  { id: "connections", label: "模型与 Key" },
  { id: "bindings", label: "专业用途" },
  { id: "checklist", label: "开写检查" },
  { id: "budget", label: "预算与账单" },
  { id: "prices", label: "模型价格" },
] as const;
const keyInput = ref<HTMLInputElement | null>(null);

interface Preset {
  id: string;
  name: string;
  model: string;
  base_url: string;
  source: string;
  checked_at: string;
}
interface Connection {
  image_capabilities?: {
    image_input: boolean;
    image_generate: boolean;
    image_edit: boolean;
    region_edit: boolean;
  };
  id: string;
  provider: string;
  name: string;
  model: string;
  base_url: string;
  provider_workspace: string;
  api_version: string;
  enabled: boolean;
  revision: number;
  has_key: boolean;
}
interface Binding {
  role: string;
  connection_id: string;
  connection_revision: number;
  revision: number;
}
interface Settings {
  setup?: ModelSetupReport;
  catalog: Preset[];
  connections: Connection[];
  bindings: Binding[];
  roles: string[];
  paid_enabled: boolean;
}
const emit = defineEmits<{ close: []; expired: [] }>();
function emptySettings(): Settings {
  return {
    catalog: [],
    connections: [],
    bindings: [],
    roles: [],
    paid_enabled: false,
  };
}
const settings = ref<Settings>(emptySettings());
const { read, active } = useModelReads();
const form = ref<Connection | null>(null);
const secret = ref("");
const clearKey = ref(false);
const busy = ref(false);
const dirty = ref(false);
const error = ref("");
const notice = ref("");
const role = ref("writer");
const bindingConnection = ref("");
const available = computed(() =>
  settings.value.connections.filter((c) => c.enabled && c.has_key),
);
const names: Record<string, string> = {
  architect: "世界与故事设计",
  planner: "大纲与细纲规划",
  writer: "正文创作",
  bookkeeper: "事实与伏笔记账",
  critic: "独立审查",
  researcher: "资料研究",
  character_designer: "角色设计",
  continuity_editor: "连续性修订",
  style_editor: "文风润色",
  asset_designer: "图片需求设计",
  image_generator: "图片生成",
  import_reader: "导入理解",
};
async function perform(work: () => Promise<void>) {
  if (busy.value || !active()) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    if (active()) {
      error.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (active()) busy.value = false;
  }
}
async function refresh() {
  try {
    const value = await read<Settings>("/models");
    if (active()) settings.value = value;
  } catch (e) {
    if (active() && e instanceof APIError && [401, 403].includes(e.status)) {
      settings.value = emptySettings();
      discard();
      bindingConnection.value = "";
    }
    throw e;
  }
}
function preset(p: Preset) {
  if (dirty.value || busy.value) return;
  form.value = {
    id: "",
    provider: p.id,
    name: p.name,
    model: p.model,
    base_url: p.base_url,
    provider_workspace: "",
    api_version: p.id === "anthropic" ? "2026-10-21" : "",
    enabled: true,
    revision: 0,
    has_key: false,
    image_capabilities: {
      image_input: false,
      image_generate: false,
      image_edit: false,
      region_edit: false,
    },
  };
  secret.value = "";
  clearKey.value = false;
  void nextTick(() => keyInput.value?.focus());
}
function edit(c: Connection) {
  if (dirty.value || busy.value) return;
  form.value = {
    ...c,
    image_capabilities: {
      image_input: false,
      image_generate: false,
      image_edit: false,
      region_edit: false,
      ...c.image_capabilities,
    },
  };
  secret.value = "";
  clearKey.value = false;
  void nextTick(() => keyInput.value?.focus());
}
function discard() {
  form.value = null;
  secret.value = "";
  clearKey.value = false;
  dirty.value = false;
}
async function save() {
  const c = form.value;
  if (!c) return;
  await perform(async () => {
    const { id, has_key, ...fields } = c;
    void has_key;
    const saved = await api<Connection>(
      id ? `/models/${id}` : "/models",
      id ? "PUT" : "POST",
      { ...fields, api_key: secret.value, clear_key: clearKey.value },
    );
    if (!active()) return;
    secret.value = "";
    clearKey.value = false;
    dirty.value = false;
    form.value = saved;
    await refresh();
    if (active()) notice.value = "配置已保存；未发起模型调用。";
  });
}
async function bind() {
  const c = available.value.find((c) => c.id === bindingConnection.value);
  if (!c) return;
  await perform(async () => {
    const previous = settings.value.bindings.find((b) => b.role === role.value);
    await api(`/model-bindings/${role.value}`, "PUT", {
      connection_id: c.id,
      connection_revision: c.revision,
      revision: previous?.revision ?? 0,
    });
    if (!active()) return;
    await refresh();
    if (active()) notice.value = "用途绑定已保存；不影响已冻结的任务版本。";
  });
}
function bindingName(b: Binding) {
  const c = settings.value.connections.find((c) => c.id === b.connection_id);
  return `${c?.name ?? "配置已不存在"} · 版本 ${b.connection_revision}${!c?.enabled ? " · 已停用" : c.revision !== b.connection_revision ? " · 固定旧版，请参照配置检查核对" : ""}`;
}
async function locateSetup(target: SetupTarget) {
  activeSection.value = target;
  await nextTick();
  const element = document.getElementById(`model-setup-${target}`);
  element?.focus({ preventScroll: true });
  element?.scrollIntoView({ block: "start" });
}
async function locateAccounting() {
  activeSection.value = "budget";
  await nextTick();
  const element = document.getElementById("model-setup-accounting");
  element?.focus({ preventScroll: true });
  element?.scrollIntoView({ block: "start" });
}
onMounted(() => perform(refresh));
onBeforeUnmount(() => {
  secret.value = "";
  form.value = null;
});
</script>

<template>
  <main class="workspace author-subpage">
    <section class="intro author-page-header">
      <div>
        <p class="eyebrow">工作室设置</p>
        <h1>模型与用途</h1>
        <p class="muted author-page-description">
          保存 Key 只做本地加密，不验证额度、不调用生成。付费调用默认关闭。
        </p>
      </div>
      <button
        class="quiet"
        :disabled="busy || dirty || accountingBlocking"
        @click="emit('close')"
      >
        返回项目
      </button>
    </section>
    <nav class="author-page-nav" aria-label="模型设置分区">
      <button
        v-for="section in sections"
        :key="section.id"
        type="button"
        class="quiet"
        :aria-pressed="activeSection === section.id"
        @click="activeSection = section.id"
      >
        {{ section.label }}
      </button>
    </nav>
    <button
      type="button"
      class="quiet"
      :disabled="busy"
      @click="perform(refresh)"
    >
      刷新模型配置
    </button>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <p v-if="dirty" class="author-status">
      模型配置有未保存的修改，切换分区会保留输入。保存或放弃后才能切换服务商或返回项目。
      <button
        v-if="activeSection !== 'connections'"
        type="button"
        class="quiet"
        @click="locateSetup('connections')"
      >
        返回配置草稿
      </button>
    </p>
    <p v-if="accountingBlocking" class="author-status">
      账单核对中有未完成的输入或操作。
      <button
        v-if="activeSection !== 'budget'"
        type="button"
        class="quiet"
        @click="locateAccounting"
      >
        查看账单核对
      </button>
    </p>
    <p v-if="accountingAttention.count" class="author-status" role="status">
      已读取 {{ accountingAttention.loaded }} 条请求，其中
      {{ accountingAttention.count }} 项结果未知，费用占用保留，不自动重发。
      <span v-if="accountingAttention.hasMore">还有未读取的请求记录。</span>
      <button type="button" class="quiet" @click="locateAccounting">
        查看待核对账目
      </button>
    </p>
    <div v-show="activeSection === 'checklist'" class="author-work-area">
      <ModelSetupChecklist
        v-if="settings.setup"
        :report="settings.setup"
        :names="names"
        :connections="settings.connections"
        @locate="locateSetup"
      />
      <section v-else class="panel">
        <h2>开写前配置检查</h2>
        <p class="muted">
          当前尚无配置检查快照。可先录入模型与
          Key，再刷新模型配置；旧版服务未提供检查时，其他设置仍可使用。
        </p>
      </section>
    </div>
    <div
      v-show="activeSection === 'connections'"
      class="layout author-work-area"
    >
      <section id="model-setup-connections" class="panel" tabindex="-1">
        <h2>选择服务商</h2>
        <p class="muted">
          预置型号来自官方目录，实际账户权限和生成质量仍需单独验证。可填写其他正式型号。
        </p>
        <div class="actions">
          <button
            v-for="p in settings.catalog"
            :key="p.id"
            class="quiet"
            :disabled="busy || dirty"
            @click="preset(p)"
          >
            添加 {{ p.name }}
          </button>
        </div>
        <h3>已保存的配置</h3>
        <p v-if="!settings.connections.length">尚未保存模型配置。</p>
        <ul class="project-list">
          <li v-for="c in settings.connections" :key="c.id">
            <button
              class="quiet"
              :aria-pressed="form?.id === c.id"
              :disabled="busy || dirty"
              @click="edit(c)"
            >
              {{ c.name }} · {{ c.enabled ? "启用" : "停用" }} ·
              {{ c.has_key ? "已保存 Key" : "未填 Key" }}
            </button>
          </li>
        </ul>
      </section>
      <section class="panel">
        <h2>{{ form?.id ? "编辑模型配置" : "添加模型配置" }}</h2>
        <p v-if="!form" class="empty">
          选择服务商或已保存的配置，录入 Key 后保存。保存不会发起模型调用。
        </p>
        <form
          v-else
          @submit.prevent="save"
          @input="dirty = true"
          @change="dirty = true"
        >
          <fieldset :disabled="busy">
            <label
              >配置名称<input v-model="form.name" required maxlength="120"
            /></label>
            <label
              >API Key<input
                ref="keyInput"
                v-model="secret"
                type="password"
                autocomplete="new-password"
                :placeholder="
                  form.has_key
                    ? '已加密保存；留空保留原值'
                    : '填写服务商 API Key'
                "
                maxlength="8192"
            /></label>
            <label v-if="form.has_key"
              ><input v-model="clearKey" type="checkbox" />清除已保存的
              Key</label
            >
            <label
              >模型 ID<input v-model="form.model" required maxlength="160"
            /></label>
            <label
              >服务地址<input v-model="form.base_url" type="url" required
            /></label>
            <p v-if="form.provider === 'qwen'" class="muted">
              Qwen 的 Key
              与区域绑定。支持官方区域域名和工作空间专属域名；切换区域时请替换对应
              Key。
            </p>
            <template v-if="form.provider === 'anthropic'"
              ><label
                >Claude API 版本<select v-model="form.api_version">
                  <option>2026-10-21</option>
                  <option>2023-06-01</option>
                </select></label
              ><label
                >Claude 工作空间 ID（按账户要求填写）<input
                  v-model="form.provider_workspace"
                  maxlength="100" /></label
            ></template>
            <label
              ><input v-model="form.enabled" type="checkbox" />启用配置</label
            >
            <p class="muted">
              凭据与业务数据分开保存，不向浏览器回显。这里保存的配置尚未通过真实模型验收。
            </p>
            <fieldset v-if="form.image_capabilities" class="image-capabilities">
              <legend>已核对的图片能力</legend>
              <p>
                按这个具体型号及账户资格填写，不能仅凭提供商名称推断。勾选不代表已通过付费效果测试；当前生图、改图通道支持
                OpenAI 图片接口。
              </p>
              <label
                ><input
                  v-model="form.image_capabilities.image_input"
                  type="checkbox"
                />可接收参考图片</label
              >
              <label
                ><input
                  v-model="form.image_capabilities.image_generate"
                  type="checkbox"
                  :disabled="form.provider !== 'openai'"
                />可生成图片</label
              >
              <label
                ><input
                  v-model="form.image_capabilities.image_edit"
                  type="checkbox"
                  :disabled="form.provider !== 'openai'"
                />可编辑已有图片（需接收参考图）</label
              >
              <label
                ><input
                  v-model="form.image_capabilities.region_edit"
                  type="checkbox"
                  :disabled="form.provider !== 'openai'"
                />可按区域编辑（需图片编辑能力）</label
              >
            </fieldset>
            <div class="actions">
              <button>保存模型配置</button
              ><button type="button" class="quiet" @click="discard">
                放弃修改
              </button>
            </div>
          </fieldset>
        </form>
      </section>
    </div>
    <section
      v-show="activeSection === 'bindings'"
      id="model-setup-bindings"
      class="panel author-work-area"
      tabindex="-1"
    >
      <h2>专业用途绑定</h2>
      <p class="muted">
        创作、记账、审查分别绑定；这里只选择模型，不代表对应 Agent
        已通过验收。图片生成须使用具备图片能力的独立型号。
      </p>
      <form @submit.prevent="bind">
        <fieldset :disabled="busy">
          <label
            >专业用途<select v-model="role">
              <option v-for="r in settings.roles" :key="r" :value="r">
                {{ names[r] ?? r }}
              </option>
            </select></label
          ><label
            >使用的模型<select
              v-model="bindingConnection"
              aria-label="使用的模型"
              required
            >
              <option value="">请选择已启用且已填 Key 的配置</option>
              <option v-for="c in available" :key="c.id" :value="c.id">
                {{ c.name }} · {{ c.model }}
              </option>
            </select></label
          ><button :disabled="!bindingConnection">保存用途绑定</button>
        </fieldset>
      </form>
      <ul>
        <li v-for="b in settings.bindings" :key="b.role">
          {{ names[b.role] ?? b.role }}：{{ bindingName(b) }}
        </li>
      </ul>
    </section>
    <div v-show="activeSection === 'budget'" class="author-work-area">
      <BudgetPanel
        id="model-setup-budget"
        ref="budgetPanel"
        tabindex="-1"
        @expired="emit('expired')"
      />
      <ReconciliationPanel
        id="model-setup-accounting"
        tabindex="-1"
        @expired="emit('expired')"
        @blocking="accountingBlocking = $event"
        @attention="accountingAttention = $event"
        @recorded="budgetPanel?.refreshTotals()"
      />
    </div>
    <div v-show="activeSection === 'prices'" class="author-work-area">
      <PricePanel
        id="model-setup-prices"
        tabindex="-1"
        :connections="settings.connections"
        @expired="emit('expired')"
      />
    </div>
  </main>
</template>
<style scoped>
.image-capabilities label {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  margin: 0.5rem 0;
}
.image-capabilities input {
  width: auto;
  margin: 0;
}
</style>
