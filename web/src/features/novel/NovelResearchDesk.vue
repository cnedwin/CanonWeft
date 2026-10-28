<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";
import type { NovelBook } from "./types";
import type { Branch } from "../ip/types";
import type {
  ResearchDocument,
  ResearchDraft,
  ResearchFact,
  ResearchCitation,
} from "./researchTypes";
import GenerationPanel from "../ip/GenerationPanel.vue";
import NovelResearchPlan from "./NovelResearchPlan.vue";
const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
  initialResourceId?: string;
}>();
const emit = defineEmits<{ close: []; materials: []; expired: [] }>();
const root = `/novels/${props.book.id}/research`;
const items = ref<ResearchDraft[]>([]),
  selected = ref<ResearchDraft | null>(null),
  branch = ref<Branch | null>(null);
const choices = ref<{ id: string; name: string; head: string }[]>([]);
const busy = ref(false),
  error = ref(""),
  notice = ref(""),
  assisting = ref(false),
  generationBusy = ref(false),
  executionBusy = ref(false),
  preparedRelease = ref("");
const sourceText = ref<string | null>(null);
let operation = newOperationID(),
  releaseOperation = newOperationID(),
  disposed = false;
const genres = [
  "历史",
  "刑侦悬疑",
  "医疗",
  "法律",
  "军事",
  "科技科幻",
  "商战职场",
  "生活职业",
];
const factFields = [
  { key: "claim", label: "单项结论" },
  { key: "era_scope", label: "适用年代" },
  { key: "region_scope", label: "适用地域" },
  { key: "limitations", label: "限制与不确定性" },
  { key: "writing_use", label: "写作用法" },
  { key: "review_after", label: "何时重新核对" },
] as const;
const reportFields = [
  { key: "conflicts", label: "来源冲突" },
  { key: "gaps", label: "尚缺的材料" },
  { key: "scene_checks", label: "场景检查点" },
] as const;
function blank(): ResearchDocument {
  return {
    title: "",
    question: "",
    genre: props.book.setup.genre,
    era: "未指定",
    region: "未指定",
    chapter_need: "待补充的场景细节",
    source_ids: [],
    sources: [],
    fact_cards: [],
    search_plan: [],
    conflicts: [],
    gaps: [],
    scene_checks: [],
  };
}
const document = ref<ResearchDocument>(blank());
const baseline = ref(JSON.stringify(document.value));
const dirty = computed(() => JSON.stringify(document.value) !== baseline.value);
const locked = computed(
  () => busy.value || generationBusy.value || executionBusy.value,
);
function fail(e: unknown) {
  if (!disposed) {
    error.value = message(e);
    if (e instanceof APIError && e.status === 401) emit("expired");
  }
}
function accept(d: ResearchDraft) {
  selected.value = d;
  document.value = structuredClone(d.document);
  document.value.source_ids ??= [];
  document.value.sources ??= [];
  document.value.fact_cards ??= [];
  document.value.search_plan ??= [];
  document.value.conflicts ??= [];
  document.value.gaps ??= [];
  document.value.scene_checks ??= [];
  baseline.value = JSON.stringify(document.value);
  releaseOperation = newOperationID();
}
async function refresh() {
  const [list, resources, current] = await Promise.all([
    api<{ items: ResearchDraft[] }>(root),
    api<{ items: { id: string; name: string; head: string }[] }>(
      `/projects/${props.project.id}/resources?branch=${props.book.branch_id}`,
    ),
    api<Branch>(
      `/projects/${props.project.id}/branches/${props.book.branch_id}`,
    ),
  ]);
  if (!disposed) {
    items.value = list.items;
    choices.value = resources.items.filter((r) => r.head);
    branch.value = current;
  }
}
async function open(id: string) {
  if (locked.value || dirty.value) return;
  busy.value = true;
  error.value = "";
  try {
    const d = await api<ResearchDraft>(`${root}/${id}`);
    if (!disposed) {
      accept(d);
      assisting.value = false;
      preparedRelease.value = "";
      sourceText.value = null;
    }
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function save() {
  if (locked.value) return false;
  busy.value = true;
  error.value = "";
  try {
    const d = selected.value
      ? await api<ResearchDraft>(
          `${root}/${selected.value.resource_id}`,
          "PUT",
          { ...selected.value, document: document.value },
        )
      : await api<ResearchDraft>(root, "POST", {
          operation_id: operation,
          document: document.value,
        });
    if (disposed) return false;
    accept(d);
    operation = newOperationID();
    notice.value = "研究稿已保存，未改变正式资料。";
    await refresh();
    return true;
  } catch (e) {
    fail(e);
    return false;
  } finally {
    if (!disposed) busy.value = false;
  }
}
function newNotebook() {
  if (locked.value || dirty.value) return;
  selected.value = null;
  document.value = blank();
  baseline.value = JSON.stringify(document.value);
  operation = newOperationID();
  assisting.value = false;
  preparedRelease.value = "";
  sourceText.value = null;
  notice.value = "";
}
async function assist() {
  if (!(await save()) || !selected.value) return;
  busy.value = true;
  try {
    await api(`${root}/agents`, "POST", {});
    if (!disposed) assisting.value = true;
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function suggestion(value: unknown) {
  const d = value as ResearchDraft;
  if (
    !selected.value ||
    d.resource_id !== selected.value.resource_id ||
    d.revision !== selected.value.revision ||
    d.hash !== selected.value.hash ||
    dirty.value
  ) {
    error.value = "研究稿已有变化，请重新准备研究任务。";
    return;
  }
  const savedBaseline = baseline.value;
  accept(d);
  baseline.value = savedBaseline;
  assisting.value = false;
  notice.value =
    "研究建议已放入编辑区，尚未保存；核对后保存，并逐项选择需要采用的卡片。";
}
function addFact() {
  document.value.fact_cards.push({
    id: newOperationID(),
    approved: false,
    claim: "待完善结论",
    status: "unverified",
    sources: [],
    era_scope: document.value.era,
    region_scope: document.value.region,
    limitations: "未独立核实",
    writing_use: document.value.chapter_need,
    review_after: "资料或故事条件改变时复核",
  });
}
function changeSource(cite: ResearchCitation, event: Event) {
  const source = document.value.sources.find(
    (s) => s.id === (event.target as HTMLSelectElement).value,
  );
  if (source)
    Object.assign(cite, {
      source_id: source.id,
      url: source.url,
      locator: source.locator,
      captured_at: source.captured_at,
      evidence: "",
    });
}
function addCitation(fact: ResearchFact) {
  const s = document.value.sources[0];
  if (s)
    fact.sources.push({
      source_id: s.id,
      url: s.url,
      locator: s.locator,
      captured_at: s.captured_at,
      evidence: "",
    });
}
async function readSource(id: string) {
  busy.value = true;
  try {
    const r = await api<{ text: string; head: string }>(
      `/projects/${props.project.id}/resources/${id}`,
    );
    if (document.value.sources.find((s) => s.id === id)?.revision !== r.head)
      throw new Error(
        "资料已换版，请刷新来源并核对受影响的事实卡；旧引用仍保留。",
      );
    if (!disposed) sourceText.value = r.text;
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function refreshSources() {
  if (!selected.value || locked.value || dirty.value) return;
  busy.value = true;
  error.value = "";
  try {
    const d = await api<ResearchDraft>(
      `${root}/${selected.value.resource_id}/refresh-sources`,
      "POST",
      {
        revision: selected.value.revision,
        hash: selected.value.hash,
      },
    );
    if (!disposed) {
      accept(d);
      sourceText.value = null;
      preparedRelease.value = "";
      notice.value =
        "来源版本已核对；受影响卡片已取消选择，旧版引证保留待复核。正式资料和章节未改动。";
      await refresh();
    }
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function prepareRelease() {
  if (!(await save()) || !selected.value) return;
  busy.value = true;
  try {
    const r = await api<{ id: string }>(
      `${root}/${selected.value.resource_id}/release`,
      "POST",
      {
        revision: selected.value.revision,
        hash: selected.value.hash,
        operation_id: releaseOperation,
      },
    );
    if (!disposed) {
      preparedRelease.value = r.id;
      notice.value =
        "已生成参考资料候选；模型来源仍需独立审查，通过后由作者确认采用和发布。";
    }
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function guard(e: BeforeUnloadEvent) {
  if (dirty.value || locked.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
onMounted(() => {
  window.addEventListener("beforeunload", guard);
  void refresh()
    .then(() =>
      props.initialResourceId ? open(props.initialResourceId) : undefined,
    )
    .catch(fail);
});
onBeforeUnmount(() => {
  disposed = true;
  window.removeEventListener("beforeunload", guard);
});
</script>
<template>
  <section class="research-desk">
    <header>
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>题材研究与事实卡</h1>
      </div>
      <button :disabled="dirty || locked" @click="emit('close')">
        返回小说工作台
      </button>
    </header>
    <p>
      把专业问题与出处整理成写作参考。引用存在不代表结论已核实；年代、地域、推断和争议分别保留，确认后才采用。
    </p>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <section class="panel">
      <h2>研究记录</h2>
      <button :disabled="dirty || locked" @click="newNotebook">
        新建研究问题</button
      ><button :disabled="dirty || locked" @click="emit('materials')">
        采集、导入与采用资料</button
      ><button :disabled="dirty || locked" @click="refresh().catch(fail)">
        刷新研究记录
      </button>
      <ul>
        <li v-for="item in items" :key="item.resource_id">
          <button :disabled="dirty || locked" @click="open(item.resource_id)">
            {{ item.document.title }} · 第{{ item.revision }}版
          </button>
        </li>
      </ul>
    </section>
    <section
      v-if="document.withdrawn_sources?.length"
      class="panel"
      aria-label="已撤回的研究来源"
    >
      <h3>已撤回的研究来源</h3>
      <p>
        以下来源已移出本稿的当前证据，旧引文保留在事实卡中。请补充可靠来源或修改结论后再选择采用。
      </p>
      <article v-for="(retired, i) in document.withdrawn_sources" :key="i">
        <strong>{{ retired.source.name }}</strong>
        <p>{{ retired.reason }}</p>
        <small>原引用定位：{{ retired.source.locator }}</small>
      </article>
    </section>
    <fieldset :disabled="locked || assisting" class="panel">
      <legend>研究问题与适用范围</legend>
      <label>研究标题<input v-model="document.title" maxlength="160" /></label
      ><label
        >需要解决的专业问题<textarea
          v-model="document.question"
          maxlength="4000"
          rows="3"
        />
      </label>
      <div class="columns">
        <label
          >题材<input
            v-model="document.genre"
            list="research-genres"
            maxlength="120" /><datalist id="research-genres">
            <option
              v-for="genre in genres"
              :key="genre"
              :value="genre"
            /></datalist></label
        ><label>故事年代<input v-model="document.era" maxlength="4000" /></label
        ><label
          >地域或制度范围<input v-model="document.region" maxlength="4000"
        /></label>
      </div>
      <label
        >需要用于什么情节或动作<textarea
          v-model="document.chapter_need"
          maxlength="4000"
        />
      </label>
      <details>
        <summary>选择已采用资料（最多12份）</summary>
        <p>
          原文和版本将进入研究请求。过长会明确报容量不足；可先在资料入口整理成更聚焦的材料。新增来源须先保存，再编辑引文。
        </p>
        <label
          v-for="r in choices.filter((r) => r.id !== selected?.resource_id)"
          :key="r.id"
          class="check"
          ><input
            v-model="document.source_ids"
            type="checkbox"
            :value="r.id"
            :disabled="
              !document.source_ids.includes(r.id) &&
              document.source_ids.length >= 12
            "
          />{{ r.name }}</label
        >
        <p v-if="!choices.length">
          暂无已采用资料，也可先让助手拆解问题，再按计划补充材料。
        </p>
      </details>
      <button @click="save">保存研究稿</button
      ><button @click="assist">让研究助手分析资料</button>
      <button :disabled="!selected || dirty" @click="refreshSources">
        刷新来源并标记待核对
      </button>
      <p>
        刷新会保存新版来源并取消受影响卡片的选择，保留旧引证和你的文字修改；不会修改已发布资料或正文。
      </p>
      <p>
        {{
          dirty ? "有未保存修改" : selected ? "研究稿已保存" : "填写问题后保存"
        }}
      </p>
    </fieldset>
    <section v-if="assisting && selected && branch" class="panel">
      <button :disabled="generationBusy" @click="assisting = false">
        返回编辑研究稿</button
      ><GenerationPanel
        :project="project"
        :actor="actor"
        :branch="branch"
        :assistance="{
          root: `${root}/${selected.resource_id}`,
          kind: '',
          role: 'researcher',
          title: '题材研究助手',
          payload: { revision: selected.revision, hash: selected.hash },
          notes: document.question,
        }"
        @blocking="generationBusy = $event"
        @expired="emit('expired')"
        @assistance-suggestion="suggestion"
      />
    </section>
    <fieldset :disabled="locked || assisting" class="panel">
      <legend>研究结果与事实卡</legend>
      <details v-if="document.search_plan.length">
        <summary>建议检索计划</summary>
        <div v-for="(q, i) in document.search_plan" :key="i">
          <label>检索词<input v-model="q.query" maxlength="4000" /></label
          ><label>待证问题<input v-model="q.purpose" maxlength="4000" /></label
          ><label
            >优先来源<input v-model="q.preferred_source" maxlength="4000"
          /></label>
        </div>
        <p>保存修改后，可在下方“执行检索计划”逐条检索并选择采集。</p>
      </details>
      <button :disabled="document.fact_cards.length >= 100" @click="addFact">
        添加事实卡
      </button>
      <article
        v-for="(fact, index) in document.fact_cards"
        :key="fact.id"
        :aria-label="`事实卡 ${index + 1}`"
      >
        <label v-for="field in factFields" :key="field.key"
          >{{ field.label
          }}<textarea v-model="fact[field.key]" maxlength="4000" />
        </label>
        <label
          >证据状态<select v-model="fact.status">
            <option value="supported">材料有支持（仍需审核）</option>
            <option value="contested">存在争议</option>
            <option value="inference">推断</option>
            <option value="unverified">未核实</option>
          </select></label
        >
        <section
          v-if="fact.stale_sources?.length"
          class="panel"
          aria-label="待复核旧版引证"
        >
          <strong>来源已换版，这些旧引证不能直接作为当前证据</strong>
          <div v-for="(cite, ci) in fact.stale_sources" :key="ci">
            <blockquote>{{ cite.evidence }}</blockquote>
            <small>{{ cite.locator }}</small>
            <button @click="fact.stale_sources.splice(ci, 1)">
              已核对并处理此旧引证
            </button>
          </div>
          <p>
            请查看新版原文，修正结论和当前引证后逐条确认；历史研究稿仍保留旧版本。
          </p>
        </section>
        <div v-for="(cite, ci) in fact.sources" :key="ci" class="citation">
          <label
            >引用资料<select
              :value="cite.source_id"
              @change="changeSource(cite, $event)"
            >
              <option v-for="s in document.sources" :key="s.id" :value="s.id">
                {{ s.name }}
              </option>
            </select></label
          ><button @click="readSource(cite.source_id)">查看引用原文</button
          ><label
            >原文短引（须与资料连续原文一致）<textarea
              v-model="cite.evidence"
              maxlength="2000"
            /></label
          ><small>固定定位：{{ cite.locator }}</small
          ><button @click="fact.sources.splice(ci, 1)">移除此条引证</button>
        </div>
        <button
          :disabled="!document.sources.length || fact.sources.length >= 20"
          @click="addCitation(fact)"
        >
          添加原文引证</button
        ><label class="check"
          ><input
            v-model="fact.approved"
            type="checkbox"
            :disabled="!!fact.stale_sources?.length"
          />我已核对，选择作为写作参考</label
        ><button @click="document.fact_cards.splice(index, 1)">
          删除此事实卡
        </button>
      </article>
      <details v-if="sourceText" open>
        <summary>参考原文</summary>
        <pre>{{ sourceText }}</pre>
        <button @click="sourceText = null">收起原文</button>
      </details>
      <label v-for="field in reportFields" :key="field.key"
        >{{ field.label }}（每行一条）<textarea
          :value="document[field.key].join('\n')"
          @input="
            document[field.key] = ($event.target as HTMLTextAreaElement).value
              .split('\n')
              .filter(Boolean)
          "
        />
      </label>
      <button @click="save">保存事实卡修改</button
      ><button
        :disabled="!document.fact_cards.some((f) => f.approved)"
        @click="prepareRelease"
      >
        将选中卡片整理为参考资料候选
      </button>
    </fieldset>
    <NovelResearchPlan
      v-if="selected && branch"
      :key="selected.resource_id"
      :book="book"
      :project="project"
      :actor="actor"
      :branch="branch"
      :notebook="selected"
      :blocked="busy || generationBusy || assisting || dirty"
      @blocking="executionBusy = $event"
      @expired="emit('expired')"
      @prepared="
        notice = '采集资料候选已保存，请到资料管理中审查、采用和发布。'
      "
    />
    <button
      v-if="preparedRelease"
      :disabled="dirty || locked"
      @click="emit('materials')"
    >
      查看候选并进行独立审查
    </button>
  </section>
</template>
<style scoped>
.research-desk {
  max-width: 1100px;
  margin: auto;
}
header,
.columns {
  display: flex;
  gap: 18px;
  justify-content: space-between;
  flex-wrap: wrap;
}
.columns > label {
  flex: 1;
  min-width: 180px;
}
label {
  display: block;
  margin: 12px 0;
}
input:not([type="checkbox"]),
textarea,
select {
  display: block;
  width: 100%;
}
.check {
  display: flex;
  gap: 8px;
  align-items: center;
}
.check input {
  width: auto;
}
article {
  border: 1px solid #d8e3dc;
  border-radius: 8px;
  padding: 18px;
  margin: 18px 0;
}
.citation {
  border-left: 3px solid #79988a;
  padding-left: 14px;
  margin: 12px 0;
}
pre {
  white-space: pre-wrap;
  max-height: 400px;
  overflow: auto;
}
small {
  display: block;
  overflow-wrap: anywhere;
}
button {
  margin: 4px 8px 4px 0;
}
</style>
