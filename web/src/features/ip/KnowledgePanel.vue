<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, APIError, message, type Actor, type Project } from "../../api";
import type { Assertion, Branch } from "./types";
import IndexJobsPanel from "./IndexJobsPanel.vue";
import WorkingInputsPicker from "./WorkingInputsPicker.vue";
import PrivateWorkingIndex from "./PrivateWorkingIndex.vue";
import type { Dependency } from "./dependencies";
import { requiredSpaceNames, requiredText } from "./requiredContext";

const props = defineProps<{
  project: Project;
  actor: Actor;
  branch: Branch;
  blocked: boolean;
  publicationRevision: number;
}>();
const emit = defineEmits<{
  expired: [];
  use: [{ id: string; purpose: string; branch: string }];
}>();
interface Evidence {
  id: string;
  resource_id: string;
  name: string;
  revision: string;
  source_hash: string;
  start: number;
  end: number;
  text: string;
  semantic_score: number;
  lexical_score: number;
}
interface Context {
  structured_selection?: {
    version: string;
    selected_assertions: number;
    omitted_assertions: number;
    selected_entities: number;
    omitted_entities: number;
    required_assertions: number;
    required_entities: number;
  };
  private_index_id?: string;
  private_hits?: (Evidence & {
    ref: { kind: string; id: string; version: string };
    part: number;
    state: string;
  })[];
  unconfirmed_inputs?: {
    state: string;
    parts: { name: string; format: string; text: string }[];
  }[];
  required_inputs?: {
    dependency: Dependency;
    name: string;
    space: string;
    text: string;
    hidden?: boolean;
  }[];
  id: string;
  index_id: string;
  ip_head: string;
  input: { purpose: string };
  facts: Assertion[];
  hidden_constraints: Assertion[];
  evidence: Evidence[];
  working_materials?: Evidence[];
  methods?: Evidence[];
  research_candidates?: Evidence[];
  omissions: string[];
  estimated_tokens: number;
  token_budget: number;
  expanded_query?: string;
}
const root = `/projects/${props.project.id}/branches/${props.branch.id}/knowledge`;
const busy = ref(false);
const error = ref("");
const notice = ref("");
const query = ref("");
const purpose = ref("author");
const perspective = ref("");
const storyTime = ref(0);
const tokenBudget = ref(65536);
const includeWorking = ref(false);
const includePrivateWorking = ref(false);
const includeMethods = ref(false);
const workingRefs = ref<{ kind: string; id: string; version: string }[]>([]);
watch(includeWorking, (value) => {
  if (!value) {
    workingRefs.value = [];
    includePrivateWorking.value = false;
  }
});
const requiredIDs = ref<string[]>([]);
const focusEntityIDs = ref<string[]>([]);
const requiredAssertionIDs = ref<string[]>([]);
const resources = ref<{ id: string; name: string; head: string }[]>([]);
let loadVersion = 0;
async function loadResources() {
  const version = ++loadVersion;
  try {
    const list = await api<{
      items: { id: string; name: string; head: string }[];
    }>(`/projects/${props.project.id}/resources?branch=${props.branch.id}`);
    if (!disposed && version === loadVersion)
      resources.value = list.items.filter((item) => item.head);
  } catch (e) {
    if (!disposed && version === loadVersion) fail(e);
  }
}
onMounted(loadResources);
watch(purpose, () => {
  includeWorking.value = false;
  includeMethods.value = false;
});
const result = ref<Context | null>(null);
const referenceGroups = computed(() => [
  {
    name: "项目工作资料（不作为已发生事实）",
    items: result.value?.working_materials ?? [],
  },
  {
    name: "方法参考（不授予工具或执行权限）",
    items: result.value?.methods ?? [],
  },
  {
    name: "外部研究候选（未激活）",
    items: result.value?.research_candidates ?? [],
  },
]);
const excerpt = ref("");
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
let generation = 0;
let disposed = false;
watch(
  [
    query,
    purpose,
    perspective,
    storyTime,
    tokenBudget,
    focusEntityIDs,
    requiredAssertionIDs,
    includeWorking,
    includePrivateWorking,
    includeMethods,
    requiredIDs,
    workingRefs,
  ],
  () => {
    generation++;
    result.value = null;
    excerpt.value = "";
  },
  { deep: true },
);
onBeforeUnmount(() => {
  disposed = true;
  generation++;
});
watch(
  () => [props.branch.head, props.publicationRevision],
  () => {
    generation++;
    result.value = null;
    excerpt.value = "";
    notice.value = "正式内容已更新，发布索引已准备完成，请重新检索。";
    void loadResources();
  },
);
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
function indexQueued() {
  generation++;
  result.value = null;
  excerpt.value = "";
  notice.value = "后台索引任务已保存，可继续整理资料。";
}
function indexReady(built: { chunks: number }) {
  notice.value = `索引已就绪，共 ${built.chunks} 个原文片段。`;
}
async function search() {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  excerpt.value = "";
  result.value = null;
  const current = ++generation;
  try {
    const next = await api<Context>(`${root}/search`, "POST", {
      query: query.value,
      purpose: purpose.value,
      perspective: perspective.value,
      story_time: storyTime.value,
      limit: 6,
      token_budget: tokenBudget.value,
      include_working: includeWorking.value,
      include_private_working: includePrivateWorking.value,
      include_methods: includeMethods.value,
      required_resource_ids: requiredIDs.value,
      working_refs: workingRefs.value,
      structured_scope: {
        focus_entity_ids: focusEntityIDs.value,
        required_assertion_ids: requiredAssertionIDs.value,
      },
    });
    if (!disposed && current === generation) result.value = next;
  } catch (e) {
    if (!disposed && current === generation) fail(e);
  } finally {
    busy.value = false;
  }
}
async function source(item: Evidence) {
  if (busy.value || props.blocked) return;
  busy.value = true;
  error.value = "";
  const current = generation;
  try {
    const data = await api<{ head: string; hash: string; text: string }>(
      `/projects/${props.project.id}/resources/${item.resource_id}`,
    );
    if (data.head !== item.revision || data.hash !== item.source_hash)
      throw new Error("资料已更新，请重建索引后重新检索。");
    const exact = Array.from(data.text).slice(item.start, item.end).join("");
    if (exact !== item.text)
      throw new Error("原文定位校验失败，未展示不一致资料。");
    if (!disposed && current === generation) excerpt.value = data.text;
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    busy.value = false;
  }
}
async function privateSource(item: Evidence) {
  if (busy.value || props.blocked || !result.value) return;
  const current = generation;
  busy.value = true;
  error.value = "";
  try {
    const part = await api<{ name: string; format: string; text: string }>(
      `${root}/contexts/${result.value.id}/private-sources/${item.id}`,
    );
    if (!disposed && current === generation)
      excerpt.value =
        part.format === "text"
          ? part.text
          : requiredText("structured", part.text);
  } catch (e) {
    if (!disposed && current === generation) fail(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="panel knowledge-panel">
    <p class="eyebrow">知识与证据</p>
    <h2>从资料中查找线索</h2>
    <p class="muted">
      结合中文关键词和本地语义模型查找原文。设定约束独立保留，检索不会把参考资料自动改成正史。
    </p>
    <IndexJobsPanel
      :project="project"
      :actor="actor"
      :branch="branch"
      :blocked="busy || blocked"
      :revision="publicationRevision"
      @queued="indexQueued"
      @ready="indexReady"
      @expired="emit('expired')"
    />
    <p v-if="notice" role="status">{{ notice }}</p>
    <form @submit.prevent="search">
      <label
        >要查找的线索<input
          v-model="query"
          required
          maxlength="1000"
          placeholder="例如：与主角寻找失落兵器有关的记录"
      /></label>
      <div class="form-row">
        <label
          >上下文用途<select v-model="purpose">
            <option value="author">作者规划</option>
            <option value="critic">连续性审查</option>
            <option value="writer">正文表述与隐藏约束</option>
            <option value="npc">角色已知信息</option>
            <option value="research">外部方法研究</option>
          </select></label
        >
        <template v-if="purpose === 'writer' || purpose === 'npc'">
          <label
            >角色视角<select v-model="perspective" required>
              <option value="" disabled>选择角色</option>
              <option
                v-for="entity in branch.snapshot.entities"
                :key="entity.id"
                :value="entity.id"
              >
                {{ entity.name }}
              </option>
            </select></label
          >
          <label
            >故事时间<input v-model.number="storyTime" type="number" required
          /></label>
        </template>
      </div>
      <div v-if="!['npc', 'writer'].includes(purpose)">
        <label
          ><input
            v-model="includeWorking"
            type="checkbox"
          />明确纳入项目工作资料</label
        >
        <label
          ><input
            v-model="includeMethods"
            type="checkbox"
          />明确纳入专业方法参考</label
        >
      </div>
      <details>
        <summary>指定本次涉及的设定</summary>
        <p>
          作者与写作用途始终保留锁定设定和世界规则；角色用途只返回已知事实。可指定必须带入的人物和命题，其余背景按相关性与容量选取，不会修改设定库。
        </p>
        <label
          >本次必须带入的人物与实体<select
            v-model="focusEntityIDs"
            multiple
            :disabled="busy || blocked"
          >
            <option
              v-for="entity in branch.snapshot.entities"
              :key="entity.id"
              :value="entity.id"
            >
              {{ entity.name }}
            </option>
          </select></label
        >
        <label
          >本次必须带入的命题<select
            v-model="requiredAssertionIDs"
            multiple
            :disabled="busy || blocked"
          >
            <option
              v-for="assertion in branch.snapshot.assertions"
              :key="assertion.id"
              :value="assertion.id"
            >
              {{
                branch.snapshot.entities.find((e) => e.id === assertion.subject)
                  ?.name ?? assertion.subject
              }}
              · {{ assertion.predicate }}：{{ assertion.value.slice(0, 80) }}
            </option>
          </select></label
        >
        <p>
          角色用途仍须满足知情范围；指定命题不会解锁秘密。必需内容超出容量时，会提示调整任务。
        </p>
      </details>
      <WorkingInputsPicker
        v-if="includeWorking"
        :project="project.id"
        :branch="branch.id"
        :blocked="busy || blocked"
        :revision="publicationRevision"
        @change="workingRefs = $event"
        @expired="emit('expired')"
      />
      <label v-if="includeWorking"
        ><input
          v-model="includePrivateWorking"
          type="checkbox"
        />在我的草稿与可访问候选中进行语义检索</label
      >
      <PrivateWorkingIndex
        v-if="includePrivateWorking"
        :project="project"
        :actor="actor"
        :branch="branch"
        :blocked="busy || blocked"
        :revision="publicationRevision"
        @queued="indexQueued"
        @expired="emit('expired')"
      />
      <fieldset v-if="resources.length" class="required-selection">
        <legend>本次必须参考的完整资料</legend>
        <p class="muted">
          可选，最多 16
          份。系统会沿已记录的依赖读取精确版本；资料及其依赖不会被相似度排名或容量压缩丢弃。容量不足、引用过期或用途不允许时，需要调整后再生成。
        </p>
        <label v-for="item in resources" :key="item.id">
          <input
            v-model="requiredIDs"
            type="checkbox"
            :value="item.id"
            :disabled="
              busy ||
              blocked ||
              (requiredIDs.length >= 16 && !requiredIDs.includes(item.id))
            "
          />
          必须参考：{{ item.name }}
        </label>
      </fieldset>
      <label
        >上下文容量（保守估算）<input
          v-model.number="tokenBudget"
          type="number"
          min="4096"
          max="262144"
          required
      /></label>
      <button :disabled="busy || blocked">查找并保存证据包</button>
    </form>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <div v-if="result" class="knowledge-result">
      <p>
        本包容量估算 {{ result.estimated_tokens }} /
        {{ result.token_budget }}；必要设定不会被静默截断。
      </p>
      <p v-if="result.structured_selection" data-testid="structured-coverage">
        本次取用
        {{ result.structured_selection.selected_assertions }} 条命题、{{
          result.structured_selection.selected_entities
        }}
        个实体； 其中
        {{ result.structured_selection.required_assertions }} 条命题与
        {{ result.structured_selection.required_entities }} 个实体属于必需内容。
        本次未纳入
        {{ result.structured_selection.omitted_assertions }} 条命题、{{
          result.structured_selection.omitted_entities
        }}
        个实体，不代表这些内容不存在。
      </p>
      <button
        v-if="canEdit && result.input.purpose !== 'npc'"
        class="secondary"
        :disabled="busy || blocked"
        @click="
          emit('use', {
            id: result.id,
            purpose: result.input.purpose,
            branch: branch.id,
          })
        "
      >
        用于下方 AI 候选任务
      </button>
      <h3>本次设定 · {{ result.facts.length }} 条</h3>
      <p v-for="fact in result.facts" :key="fact.id">
        {{ fact.subject }} · {{ fact.predicate }}：{{ fact.value }}
        <small>（{{ fact.kind }}）</small>
      </p>
      <details v-if="result.hidden_constraints.length">
        <summary>
          作者隐藏约束 ·
          {{ result.hidden_constraints.length }} 条（不可直接进入角色对白）
        </summary>
        <p v-for="fact in result.hidden_constraints" :key="fact.id">
          {{ fact.predicate }}：{{ fact.value }}
        </p>
      </details>
      <section
        v-if="result.required_inputs?.length"
        data-testid="required-context"
      >
        <h3>必须参考的完整输入 · {{ result.required_inputs.length }} 项</h3>
        <p class="muted">
          历史版本与其他分支的内容保留出处，不会成为当前故事的正式事实；方法只供参考，不会执行文件中的代码。
        </p>
        <details
          v-for="(item, index) in result.required_inputs"
          :key="index"
          class="required-card"
        >
          <summary>
            {{ item.name }} · {{ requiredSpaceNames[item.space] }}
          </summary>
          <p>
            {{ item.dependency.reason }} ·
            {{ item.dependency.pinned ? "固定历史版本" : "引用版本仍须有效" }}
          </p>
          <p v-if="item.hidden" class="muted">
            作者隐藏约束，不可直接进入角色表述。
          </p>
          <p class="evidence-text">
            {{ requiredText(item.dependency.source.kind, item.text) }}
          </p>
        </details>
      </section>
      <h3>原文证据 · {{ result.evidence.length }} 段</h3>
      <section v-if="result.private_hits?.length" data-testid="private-hits">
        <h3>未确认的语义线索 · {{ result.private_hits.length }} 段</h3>
        <p class="muted">
          私人草稿和候选片段，只供完善或审查；不代表正式事实。所选完整输入在下方独立保留。
        </p>
        <article
          v-for="item in result.private_hits"
          :key="item.id"
          class="evidence-card"
        >
          <strong
            >{{ item.name }} ·
            {{
              item.state === "PRIVATE_DRAFT"
                ? "私人草稿"
                : item.state === "READY"
                  ? "已采用未发布"
                  : "待确认候选"
            }}</strong
          >
          <p class="evidence-text">{{ item.text }}</p>
          <small
            >原文字符 {{ item.start + 1 }}—{{ item.end }} · 语义相关度
            {{ item.semantic_score.toFixed(3) }} · 来源版本
            {{ item.ref.version }}</small
          >
          <button
            class="quiet"
            :disabled="busy || blocked"
            @click="privateSource(item)"
          >
            核对未确认原文
          </button>
        </article>
      </section>
      <section
        v-if="result.unconfirmed_inputs?.length"
        data-testid="unconfirmed-context"
      >
        <h3>未确认资料 · {{ result.unconfirmed_inputs.length }} 项</h3>
        <p class="muted">
          以下是用于协作完善的草稿或候选，不是正式设定、已发生事件或获准执行的方法。保存此证据包不会采用或发布任何内容。
        </p>
        <details
          v-for="(item, index) in result.unconfirmed_inputs"
          :key="index"
        >
          <summary>
            {{ item.parts[0]?.name }} ·
            {{
              item.state === "PRIVATE_DRAFT"
                ? "私人草稿"
                : item.state === "READY"
                  ? "已采用但未发布"
                  : "待确认候选"
            }}
          </summary>
          <article v-for="(part, partIndex) in item.parts" :key="partIndex">
            <h4>{{ part.name }}</h4>
            <p class="evidence-text">
              {{
                requiredText(
                  part.format === "text" ? "resource" : "structured",
                  part.text,
                )
              }}
            </p>
          </article>
        </details>
      </section>
      <article
        v-for="item in result.evidence"
        :key="item.id"
        class="evidence-card"
      >
        <strong>{{ item.name }}</strong>
        <p class="evidence-text">{{ item.text }}</p>
        <small
          >原文字符 {{ item.start + 1 }}—{{ item.end }} · 语义相关度
          {{ item.semantic_score.toFixed(3) }}</small
        >
        <button class="quiet" :disabled="busy || blocked" @click="source(item)">
          核对完整原文
        </button>
      </article>
      <p v-for="omission in result.omissions" :key="omission" class="muted">
        {{ omission }}
      </p>
      <template v-for="group in referenceGroups" :key="group.name">
        <section v-if="group.items.length">
          <h3>{{ group.name }}</h3>
          <article
            v-for="item in group.items"
            :key="item.id"
            class="evidence-card"
          >
            <strong>{{ item.name }}</strong>
            <p class="evidence-text">{{ item.text }}</p>
            <button
              class="quiet"
              :disabled="busy || blocked"
              @click="source(item)"
            >
              核对完整原文
            </button>
          </article>
        </section>
      </template>
      <details>
        <summary>证据包版本记录</summary>
        <p>证据包：{{ result.id }}</p>
        <p v-if="result.expanded_query">
          别名扩展检索：{{ result.expanded_query }}
        </p>
        <p>索引：{{ result.index_id }}</p>
        <p v-if="result.private_index_id">
          私人索引：{{ result.private_index_id }}
        </p>
        <p>设定版本：{{ result.ip_head || "尚无发布设定" }}</p>
      </details>
    </div>
    <details v-if="excerpt" open>
      <summary>经核对的完整原文</summary>
      <p class="evidence-text">{{ excerpt }}</p>
    </details>
  </section>
</template>

<style scoped>
.knowledge-panel {
  margin-top: 1.5rem;
}
form {
  display: grid;
  gap: 1rem;
  margin-top: 1rem;
}
.form-row {
  display: flex;
  flex-wrap: wrap;
  gap: 1rem;
}
.evidence-card {
  padding: 1rem;
  border: 1px solid #dcd8cd;
  border-radius: 0.6rem;
  margin-bottom: 0.7rem;
}
.required-selection {
  border: 1px solid #dcd8cd;
  border-radius: 0.6rem;
  padding: 1rem;
}
.required-selection label {
  display: block;
  margin: 0.5rem 0;
}
.required-card {
  padding: 0.5rem;
  border: 1px solid #dcd8cd;
}
.evidence-text {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.knowledge-result details {
  overflow-wrap: anywhere;
  margin: 1rem 0;
}
</style>
