<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Actor,
  type Project,
} from "../../api";
import type { NovelBook } from "./types";
import PlanningReadView from "./PlanningReadView.vue";
import GenerationPanel from "../ip/GenerationPanel.vue";
import type { Branch } from "../ip/types";
import {
  planNames,
  planFields,
  chapterFields,
  sceneFields,
  planningID,
  type PlanKind,
  type PlanDraft,
  type PlanningDocument,
  type PlanningOverview,
  type PlanRelease,
  type PlanResource,
  type PlanningSuggestion,
} from "./planningTypes";

const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
}>();
const emit = defineEmits<{
  close: [];
  expired: [];
  write: [chapterId: string];
}>();
const root = `/novels/${props.book.id}/plans`;
const overview = ref<PlanningOverview | null>(null);
const draft = ref<PlanDraft | null>(null);
const history = ref<PlanRelease[]>([]);
const assistantBranch = ref<Branch | null>(null);
const assistantBlocking = ref(false);
const suggestion = ref<PlanningSuggestion | null>(null);
const candidate = ref<PlanRelease | null>(null);
const candidateDocument = ref<PlanningDocument | null>(null);
const accepted = ref(false),
  busy = ref(true),
  saving = ref(false);
const error = ref(""),
  notice = ref("");
const savedText = ref("");
const savedBaseHead = ref("");
const savedSources = ref("[]");
const sources = ref<string[]>([]);
const sourceSelectionChanged = computed(
  () => JSON.stringify([...sources.value].sort()) !== savedSources.value,
);
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const documentText = computed(() =>
  draft.value ? JSON.stringify(draft.value.document) : "",
);
const dirty = computed(
  () =>
    !!draft.value &&
    (documentText.value !== savedText.value ||
      draft.value.base_head !== savedBaseHead.value),
);
const locked = computed(
  () =>
    busy.value ||
    !!candidate.value ||
    !!assistantBranch.value ||
    !canEdit.value,
);
const missingSources = computed(() =>
  sources.value.filter(
    (id) => !overview.value?.sources.some((s) => s.id === id),
  ),
);
let disposed = false;
let timer: ReturnType<typeof setTimeout> | undefined;
let pending: Promise<void> | undefined;
let operation = newOperationID();
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
function queueSave() {
  clearTimeout(timer);
  timer = setTimeout(() => {
    void save().catch(fail);
  }, 600);
}
watch([documentText, () => draft.value?.base_head], () => {
  if (dirty.value && canEdit.value) {
    operation = newOperationID();
    queueSave();
  }
});
async function save(force = false): Promise<void> {
  if (disposed || !draft.value || !canEdit.value) return;
  if (pending) {
    await pending;
    if (dirty.value) return save();
    return;
  }
  if (!force && !dirty.value && draft.value.revision > 0) return;
  clearTimeout(timer);
  const current = draft.value;
  const text = documentText.value;
  const baseHead = current.base_head;
  saving.value = true;
  pending = (async () => {
    const result = await api<PlanDraft>(
      `${root}/${current.resource.id}/draft`,
      "PUT",
      {
        revision: current.revision,
        base_head: current.base_head,
        document: JSON.parse(text),
      },
    );
    if (disposed || draft.value?.resource.id !== current.resource.id) return;
    draft.value.revision = result.revision;
    draft.value.hash = result.hash;
    draft.value.resource = result.resource;
    draft.value.base_needs_save = result.base_needs_save;
    savedText.value = text;
    savedBaseHead.value = baseHead;
  })();
  try {
    await pending;
  } finally {
    pending = undefined;
    if (!disposed) saving.value = false;
  }
}
async function writeChapter() {
  if (
    locked.value ||
    !draft.value?.published ||
    draft.value.document.kind !== "detail"
  )
    return;
  busy.value = true;
  error.value = "";
  try {
    await save();
    if (disposed || dirty.value) return;
    if (
      sourceSelectionChanged.value ||
      JSON.stringify(draft.value.document) !==
        JSON.stringify(draft.value.published)
    )
      throw new Error("本章细纲还有未采用的修改，请先确认细纲，再开始写作。");
    if (!draft.value.ready)
      throw new Error(draft.value.reason || "请先更新本章规划依据。");
    emit("write", draft.value.document.scope);
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function loadOverview() {
  const value = await api<PlanningOverview>(root);
  if (!disposed) overview.value = value;
}
async function loadDocument(id: string, title?: string) {
  const [value, releases] = await Promise.all([
    api<PlanDraft>(`${root}/${id}/draft`),
    api<{ items: PlanRelease[] }>(
      `/projects/${props.project.id}/releases?branch=${props.book.branch_id}`,
    ),
  ]);
  if (disposed) return;
  savedText.value = JSON.stringify(value.document);
  savedBaseHead.value = value.base_head;
  draft.value = value;
  if (title && value.revision === 0 && !value.published)
    draft.value.document.title = title;
  const planIDs = new Set(overview.value?.resources.map((r) => r.id));
  sources.value = (value.document.inputs ?? [])
    .filter((p) => !planIDs.has(p.id))
    .map((p) => p.id);
  savedSources.value = JSON.stringify([...sources.value].sort());
  history.value = releases.items.filter(
    (r) =>
      r.manifest.artifacts.length === 1 &&
      r.manifest.artifacts[0]?.resource_id === id,
  );
  candidate.value = null;
  candidateDocument.value = null;
  accepted.value = false;
}
async function open(kind: PlanKind, scope = "", title?: string) {
  if (busy.value || candidate.value || assistantBranch.value) return;
  if (draft.value && sourceSelectionChanged.value) {
    error.value = "参考资料选择尚未应用，请先更新并核对规划依据。";
    return;
  }
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await save();
    if (dirty.value) return;
    const name = planNames[kind] + (scope ? ` · ${scope}` : "");
    let resource = overview.value?.resources.find(
      (r) => r.kind === `novel_plan_${kind}` && r.name === name,
    );
    if (!resource) {
      if (!canEdit.value) {
        notice.value = "此部分尚未创建，可查看已经确认的规划。";
        return;
      }
      resource = await api<PlanResource>(root, "POST", { kind, scope });
      await loadOverview();
    }
    await loadDocument(resource.id, title);
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function openAssistant() {
  if (!draft.value || locked.value) return;
  if (sourceSelectionChanged.value) {
    error.value = "请先更新并核对所选规划依据。";
    return;
  }
  busy.value = true;
  error.value = "";
  try {
    // Persist the normalized base after a previous publication, even when the
    // visible content is unchanged; contextual draft references use real CAS.
    await save(!!draft.value.base_needs_save);
    if (dirty.value || disposed) return;
    await api(`${root}/agents`, "POST", {});
    const branch = await api<Branch>(
      `/projects/${props.project.id}/branches/${props.book.branch_id}`,
    );
    if (!disposed) {
      assistantBranch.value = branch;
      suggestion.value = null;
    }
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function closeAssistant() {
  if (assistantBlocking.value) {
    error.value = "请先保存助手请求或清空未保存的输入。";
    return;
  }
  assistantBranch.value = null;
  suggestion.value = null;
}
async function applySuggestion() {
  const next = suggestion.value;
  if (!next || !draft.value || busy.value) return;
  if (assistantBlocking.value) {
    error.value = "请先保存助手请求或清空未保存的输入。";
    return;
  }
  if (
    next.draft.resource.id !== draft.value.resource.id ||
    next.draft.revision !== draft.value.revision ||
    next.draft.hash !== draft.value.hash
  ) {
    error.value = "草稿已变化，请重新核对助手建议。";
    return;
  }
  busy.value = true;
  try {
    draft.value.document = JSON.parse(JSON.stringify(next.draft.document));
    await save();
    if (disposed) return;
    assistantBranch.value = null;
    suggestion.value = null;
    notice.value =
      "建议已加入私人草稿，可继续修改；更新规划依据并确认采用后才影响后续写作。";
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function refreshBasis() {
  if (!draft.value || locked.value) return;
  busy.value = true;
  error.value = "";
  try {
    await save();
    if (dirty.value || !draft.value) return;
    const value = await api<PlanDraft>(
      `${root}/${draft.value.resource.id}/basis`,
      "POST",
      { revision: draft.value.revision, sources: sources.value },
    );
    if (disposed) return;
    draft.value = value;
    savedSources.value = JSON.stringify([...sources.value].sort());
    await save();
    notice.value =
      "依据已更新，原规划内容仍保留。请逐项核对后再查看候选并确认。";
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function prepare() {
  if (!draft.value || locked.value) return;
  if (sourceSelectionChanged.value) {
    error.value = "参考资料选择尚未应用，请先更新并核对规划依据。";
    return;
  }
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await save();
    if (dirty.value || !draft.value) return;
    const result = await api<PlanRelease>(
      `${root}/${draft.value.resource.id}/prepare`,
      "POST",
      {
        revision: draft.value.revision,
        hash: draft.value.hash,
        operation_id: operation,
      },
    );
    if (disposed) return;
    candidate.value = result;
    candidateDocument.value = JSON.parse(JSON.stringify(draft.value.document));
    accepted.value = false;
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function inspect(release: PlanRelease) {
  if (busy.value || !draft.value) return;
  busy.value = true;
  error.value = "";
  try {
    await save();
    if (dirty.value) return;
    const result = await api<PlanRelease>(
      `/projects/${props.project.id}/releases/${release.id}`,
    );
    const content = result.contents?.find(
      (c) => c.resource_id === draft.value?.resource.id,
    );
    if (!content) throw new Error("此版本内容暂不可读取，当前草稿仍保留。");
    if (disposed) return;
    candidate.value = result;
    candidateDocument.value = JSON.parse(content.text);
    accepted.value = false;
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function confirm() {
  if (busy.value || !draft.value || !candidate.value || !accepted.value) return;
  busy.value = true;
  error.value = "";
  const resource = draft.value.resource.id;
  try {
    await api(`${root}/${resource}/confirm`, "POST", {
      release_id: candidate.value.id,
      hash: candidate.value.hash,
      accept: true,
    });
    if (disposed) return;
    await loadOverview();
    await loadDocument(resource);
    notice.value = "本版规划已确认采用，后续步骤会引用此版本。";
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function leavePreview() {
  candidate.value = null;
  candidateDocument.value = null;
  accepted.value = false;
}
function useHistory() {
  if (!draft.value || !candidateDocument.value || busy.value || !canEdit.value)
    return;
  draft.value.document = JSON.parse(JSON.stringify(candidateDocument.value));
  draft.value.base_head = draft.value.resource.head;
  resetSourceSelection();
  leavePreview();
  notice.value = "此历史内容已放入私人草稿，需重新核对依据并确认才会采用。";
}
function loadPublished() {
  if (!draft.value?.published || locked.value) return;
  draft.value.document = JSON.parse(JSON.stringify(draft.value.published));
  draft.value.base_head = draft.value.resource.head;
  resetSourceSelection();
  notice.value = "已载入当前正式规划，尚未提交新的采用版本。";
}
function resetSourceSelection() {
  const planIDs = new Set(overview.value?.resources.map((r) => r.id));
  sources.value = (draft.value?.document.inputs ?? [])
    .filter((pin) => !planIDs.has(pin.id))
    .map((pin) => pin.id);
  savedSources.value = JSON.stringify([...sources.value].sort());
}
function addVolume() {
  if (draft.value)
    (draft.value.document.volumes ??= []).push({
      id: planningID(),
      title: `第${(draft.value.document.volumes?.length ?? 0) + 1}卷`,
      summary: "",
    });
}
function addChapter() {
  if (draft.value)
    (draft.value.document.chapters ??= []).push({
      id: planningID(),
      title: "",
      summary: "",
      perspective: props.book.setup.perspective,
      goal: "",
      events: "",
      change: "",
      transition: "",
    });
}
function addScene() {
  if (draft.value)
    (draft.value.document.scenes ??= []).push({
      id: planningID(),
      time: "",
      place: "",
      characters: "",
      knowledge: "",
      goal: "",
      conflict: "",
      action: "",
      outcome: "",
      emotion: "",
      foreshadow: "",
      prerequisites: "",
      hook: "",
      words: props.book.setup.chapter_min,
    });
}
function move<T>(items: T[] | null, index: number, offset: number) {
  if (!items || index + offset < 0 || index + offset >= items.length) return;
  const [item] = items.splice(index, 1);
  if (item !== undefined) items.splice(index + offset, 0, item);
}
async function close() {
  if (busy.value || candidate.value || assistantBranch.value) return;
  if (sourceSelectionChanged.value) {
    error.value = "参考资料选择尚未应用，请先更新并核对规划依据。";
    return;
  }
  busy.value = true;
  try {
    await save();
    if (!dirty.value) emit("close");
  } catch (e) {
    fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
function guard(event: BeforeUnloadEvent) {
  if (
    dirty.value ||
    sourceSelectionChanged.value ||
    saving.value ||
    busy.value
  ) {
    event.preventDefault();
    event.returnValue = "";
  }
}
onMounted(async () => {
  window.addEventListener("beforeunload", guard);
  try {
    await loadOverview();
    if (!disposed) {
      busy.value = false;
      await open("world");
    }
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
});
onBeforeUnmount(() => {
  disposed = true;
  clearTimeout(timer);
  window.removeEventListener("beforeunload", guard);
});
</script>

<template>
  <section class="novel-planning">
    <header class="planning-header">
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>小说规划</h1>
      </div>
      <button
        :disabled="busy || !!candidate || !!assistantBranch"
        @click="close"
      >
        保存并返回小说
      </button>
    </header>
    <p>
      先确认世界与故事，再确认全部分卷与暂定章节，最后逐章确认细纲。可先手工完善，也可持续修改；未来情节始终保留在作者规划中。
    </p>
    <p v-if="overview" class="notice">
      {{
        overview.stage === "WORLD_PLANNING"
          ? "当前：完善世界与故事"
          : overview.stage === "OUTLINE_PLANNING"
            ? "当前：编排全书与分卷章节"
            : "当前：按章完善细纲"
      }}。{{ overview.reason }}
    </p>
    <nav class="actions" aria-label="小说规划阶段">
      <button
        :disabled="busy || !!candidate || !!assistantBranch"
        @click="open('world')"
      >
        世界与故事</button
      ><button
        :disabled="busy || !!candidate || !!assistantBranch"
        @click="open('book')"
      >
        全书与分卷安排
      </button>
    </nav>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <div v-if="draft && canEdit" class="actions">
      <button
        v-if="draft.document.kind === 'detail' && draft.published"
        :disabled="locked"
        @click="writeChapter"
      >
        开始本章写作
      </button>
      <button :disabled="locked" @click="openAssistant">
        请 Agent 帮我完善当前规划
      </button>
      <button v-if="assistantBranch" :disabled="busy" @click="closeAssistant">
        返回手工规划
      </button>
    </div>
    <section v-if="assistantBranch && draft" aria-label="规划辅助创作">
      <p>
        助手读取本版草稿、已确认上游规划与所选资料。当前表单暂时锁定，关闭助手即可继续手写。原任务可从历史列表重新打开。
      </p>
      <GenerationPanel
        :project="project"
        :actor="actor"
        :branch="assistantBranch"
        :planning="{
          root: `${root}/${draft.resource.id}`,
          revision: draft.revision,
          hash: draft.hash,
          role: draft.document.kind === 'world' ? 'architect' : 'planner',
        }"
        @expired="emit('expired')"
        @blocking="assistantBlocking = $event"
        @planning-suggestion="suggestion = $event"
      />
      <section v-if="suggestion" class="panel" aria-label="助手建议对照">
        <h2>检查助手修改建议</h2>
        <p>{{ suggestion.explanation }}</p>
        <ul v-if="suggestion.questions.length">
          <li v-for="(question, index) in suggestion.questions" :key="index">
            {{ question }}
          </li>
        </ul>
        <details>
          <summary>查看修改前的私人草稿</summary>
          <PlanningReadView :document="draft.document" />
        </details>
        <PlanningReadView :document="suggestion.draft.document" />
        <button :disabled="busy || assistantBlocking" @click="applySuggestion">
          将建议加入私人草稿
        </button>
        <button :disabled="busy" @click="suggestion = null">
          暂不使用此建议
        </button>
      </section>
    </section>
    <p v-if="draft" data-testid="plan-draft-status">
      {{
        saving || dirty
          ? "规划草稿正在暂存…"
          : "规划草稿已暂存；确认采用前不改变正式规划"
      }}
    </p>
    <section
      v-if="candidate && candidateDocument"
      class="panel"
      aria-label="规划候选对照"
    >
      <h2>
        {{
          candidate.status === "PUBLISHED" ? "历史采用版本" : "请核对本版规划"
        }}
      </h2>
      <details v-if="draft?.published">
        <summary>对照当前正式规划</summary>
        <PlanningReadView :document="draft.published" />
      </details>
      <PlanningReadView :document="candidateDocument" />
      <label v-if="candidate.status !== 'PUBLISHED' && canEdit"
        ><input
          v-model="accepted"
          type="checkbox"
          :disabled="busy"
        />我已查看本版内容与依据，确认采用此规划</label
      >
      <div class="actions">
        <button
          v-if="candidate.status !== 'PUBLISHED' && canEdit"
          :disabled="busy || !accepted"
          @click="confirm"
        >
          确认采用此规划</button
        ><button :disabled="busy" @click="leavePreview">返回修改</button
        ><button v-if="canEdit" :disabled="busy" @click="useHistory">
          将此版作为新的私人草稿
        </button>
      </div>
    </section>
    <template v-if="draft && !candidate">
      <section class="panel">
        <h2>{{ planNames[draft.document.kind] }}</h2>
        <p>
          引用小说设置第{{ draft.document.settings_revision }}版，{{
            draft.document.inputs?.length ?? 0
          }}项资料与规划依据。{{ draft.reason }}
        </p>
        <fieldset :disabled="locked">
          <legend>本次采用的参考资料</legend>
          <label v-for="source in overview?.sources" :key="source.id"
            ><input v-model="sources" type="checkbox" :value="source.id" />{{
              source.name
            }}</label
          >
          <p v-if="!overview?.sources.length">
            尚无已采用的参考资料。可先在“管理本书资料”中导入并确认。
          </p>
          <p v-if="missingSources.length">
            有{{ missingSources.length }}项旧版本来源当前不可用。<button
              type="button"
              @click="
                sources = sources.filter((id) => !missingSources.includes(id))
              "
            >
              移除失效来源，重新选择
            </button>
          </p>
          <button type="button" @click="refreshBasis">
            更新并核对规划依据
          </button>
          <p>
            更新只改变引用版本，不替你重写内容。请检查改稿与最新世界、资料和上游规划是否一致。
          </p>
        </fieldset>
        <form @submit.prevent="prepare">
          <fieldset :disabled="locked">
            <label
              v-for="field in planFields.filter((f) =>
                f.kinds.includes(draft!.document.kind),
              )"
              :key="field.key"
              >{{ field.label
              }}<textarea
                v-model="draft.document[field.key]"
                :rows="field.key === 'title' ? 1 : 3"
                maxlength="20000"
              />
            </label>
            <template v-if="draft.document.kind === 'book'">
              <h3>全书卷安排（顺序可调整）</h3>
              <p>每卷需完成并确认章节安排，之后才能确认整个全书大纲。</p>
              <article
                v-for="(volume, index) in draft.document.volumes"
                :key="volume.id"
                class="plan-item"
                :aria-label="`第${index + 1}卷安排`"
              >
                <label>分卷标题<input v-model="volume.title" /></label
                ><label>分卷摘要<textarea v-model="volume.summary" /></label>
                <div class="actions">
                  <button
                    type="button"
                    @click="open('volume', volume.id, volume.title)"
                  >
                    编排本卷章节</button
                  ><button
                    type="button"
                    :disabled="index === 0"
                    @click="move(draft.document.volumes, index, -1)"
                  >
                    上移此卷</button
                  ><button
                    type="button"
                    :disabled="
                      index === (draft.document.volumes?.length ?? 0) - 1
                    "
                    @click="move(draft.document.volumes, index, 1)"
                  >
                    下移此卷</button
                  ><button
                    type="button"
                    @click="draft.document.volumes?.splice(index, 1)"
                  >
                    移出本版卷安排
                  </button>
                </div>
              </article>
              <button type="button" @click="addVolume">添加一卷</button>
            </template>
            <template v-if="draft.document.kind === 'volume'">
              <h3>本卷暂定章节</h3>
              <article
                v-for="(chapter, index) in draft.document.chapters"
                :key="chapter.id"
                class="plan-item"
                :aria-label="`第${index + 1}章安排`"
              >
                <h4>第{{ index + 1 }}章</h4>
                <label v-for="field in chapterFields" :key="field.key"
                  >{{ field.label
                  }}<textarea
                    v-model="chapter[field.key]"
                    rows="2"
                    maxlength="20000"
                  />
                </label>
                <div class="actions">
                  <button
                    type="button"
                    :disabled="overview?.stage !== 'DETAIL_PLANNING'"
                    @click="
                      open('detail', chapter.id, `${chapter.title} · 细纲`)
                    "
                  >
                    完善本章细纲</button
                  ><button
                    type="button"
                    :disabled="index === 0"
                    @click="move(draft.document.chapters, index, -1)"
                  >
                    上移此章</button
                  ><button
                    type="button"
                    :disabled="
                      index === (draft.document.chapters?.length ?? 0) - 1
                    "
                    @click="move(draft.document.chapters, index, 1)"
                  >
                    下移此章</button
                  ><button
                    type="button"
                    @click="draft.document.chapters?.splice(index, 1)"
                  >
                    移出本版章节安排
                  </button>
                </div>
              </article>
              <button type="button" @click="addChapter">添加一章</button
              ><button type="button" @click="open('book')">返回全书安排</button>
            </template>
            <template v-if="draft.document.kind === 'detail'">
              <h3>场景与情节节拍</h3>
              <p>
                本章预计{{ book.setup.chapter_min }}—{{
                  book.setup.chapter_max
                }}字；以全部场景字数合计核对。
              </p>
              <article
                v-for="(scene, index) in draft.document.scenes"
                :key="scene.id"
                class="plan-item"
                :aria-label="`场景${index + 1}`"
              >
                <h4>场景{{ index + 1 }}</h4>
                <label v-for="field in sceneFields" :key="field.key"
                  >{{ field.label
                  }}<textarea
                    v-model="scene[field.key]"
                    rows="2"
                    maxlength="20000"
                  /></label
                ><label
                  >预计字数<input
                    v-model.number="scene.words"
                    type="number"
                    min="0"
                    max="50000"
                /></label>
                <div class="actions">
                  <button
                    type="button"
                    :disabled="index === 0"
                    @click="move(draft.document.scenes, index, -1)"
                  >
                    上移场景</button
                  ><button
                    type="button"
                    :disabled="
                      index === (draft.document.scenes?.length ?? 0) - 1
                    "
                    @click="move(draft.document.scenes, index, 1)"
                  >
                    下移场景</button
                  ><button
                    type="button"
                    @click="draft.document.scenes?.splice(index, 1)"
                  >
                    移出本版场景
                  </button>
                </div>
              </article>
              <button type="button" @click="addScene">添加场景</button>
            </template>
            <div class="actions">
              <button v-if="canEdit" type="submit">查看规划候选</button
              ><button
                v-if="draft.published && canEdit"
                type="button"
                @click="loadPublished"
              >
                放弃私人修改，载入当前正式版
              </button>
            </div>
          </fieldset>
        </form>
      </section>
      <details class="panel">
        <summary>规划版本与待确认候选（{{ history.length }}）</summary>
        <ul>
          <li v-for="item in history" :key="item.id">
            <button :disabled="busy" @click="inspect(item)">
              {{
                item.status === "PUBLISHED"
                  ? "已采用版本"
                  : item.status === "READY"
                    ? "已确认，等待发布完成"
                    : "待确认候选"
              }}
              · {{ item.id.slice(0, 8) }}
            </button>
          </li>
        </ul>
      </details>
    </template>
  </section>
</template>

<style scoped>
.novel-planning {
  max-width: 1100px;
  margin: auto;
}
.planning-header,
.actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
}
.planning-header {
  justify-content: space-between;
}
.panel {
  margin-top: 18px;
}
fieldset {
  display: grid;
  gap: 14px;
  margin: 14px 0;
  min-width: 0;
}
label {
  display: grid;
  gap: 6px;
}
label:has(input[type="checkbox"]) {
  display: flex;
  align-items: center;
}
textarea,
input:not([type="checkbox"]) {
  width: 100%;
  box-sizing: border-box;
}
.plan-item {
  border: 1px solid #dce1e7;
  border-radius: 8px;
  padding: 16px;
  display: grid;
  gap: 12px;
}
</style>
