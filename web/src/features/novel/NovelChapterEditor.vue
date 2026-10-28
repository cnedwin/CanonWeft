<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, APIError, message, type Actor, type Project } from "../../api";
import type { NovelBook } from "./types";
import type { ChapterDraft } from "./chapterTypes";
import type { PlanResource } from "./planningTypes";
import type { Branch } from "../ip/types";
import GenerationPanel from "../ip/GenerationPanel.vue";
import NovelLedgerEditor from "./NovelLedgerEditor.vue";
import NovelEditorialDesk from "./NovelEditorialDesk.vue";
import NovelChapterResearch from "./NovelChapterResearch.vue";
import NovelInlineRefinement from "./NovelInlineRefinement.vue";
import NovelWritingCard from "./NovelWritingCard.vue";
import {
  captureRefinement,
  refinementPrompt,
  validateRefinement,
  type AuthorIntent,
  type RefinementMode,
  type RefinementScope,
} from "./authorWorkbench";

const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
  chapterId: string;
  initialAction?: "write" | "refine";
}>();
const emit = defineEmits<{ close: []; expired: [] }>();
const root = `/novels/${props.book.id}/chapters`;
const draft = ref<ChapterDraft | null>(null);
const branch = ref<Branch | null>(null);
const suggestion = ref<ChapterDraft | null>(null);
const assistant = ref(false),
  assistantBlocking = ref(false);
const ledgerOpen = ref(false);
const assistantNotes = ref("");
const assistantMethod = ref<"fanqie-polish" | undefined>();
const selectedScope = ref<RefinementScope | null>(null);
const selectedQuote = computed(() => selectedScope.value?.quote ?? "");
const refinementRegion = ref<HTMLElement | null>(null);
const sessionScope = ref<RefinementScope | null>(null);
const sessionDocument = ref("");
const sessionChanged = ref(false);
const sessionRefining = ref(false);
const assistantSequence = ref(0);
const assistantMount = ref(0);
const assistantCancelled = ref(false);
const writingRequest = ref<{
  root: string;
  revision: number;
  hash: string;
  initialNotes: string;
  method?: "fanqie-polish";
} | null>(null);
const receiveCandidate = ref<(value: ChapterDraft) => void>(() => {});
const refinementCandidate = computed(() => {
  if (!sessionScope.value || !suggestion.value || !draft.value) return "";
  try {
    return validateRefinement(
      sessionScope.value,
      draft.value.document.text,
      suggestion.value.document.text,
    );
  } catch {
    return "";
  }
});
function selectQuote(event: Event) {
  const field = event.target as HTMLTextAreaElement;
  try {
    selectedScope.value = captureRefinement(
      field.value,
      field.selectionStart,
      field.selectionEnd,
    );
  } catch {
    selectedScope.value = null;
  }
}
async function refine(prompt: string) {
  assistantNotes.value = prompt;
  assistantMethod.value = "fanqie-polish";
  sessionScope.value = selectedScope.value;
  sessionRefining.value = true;
  await openAssistant();
}
async function refineInPlace(
  scope: RefinementScope | null,
  mode: RefinementMode,
  instruction: string,
) {
  try {
    assistantNotes.value = refinementPrompt(scope, mode, instruction);
    assistantMethod.value = "fanqie-polish";
    sessionScope.value = scope;
    sessionRefining.value = true;
    await openAssistant();
  } catch (e) {
    fail(e);
  }
}
async function regularAssistant() {
  assistantNotes.value = "";
  assistantMethod.value = undefined;
  sessionScope.value = null;
  sessionRefining.value = false;
  await openAssistant();
}
async function requestAssistant(intent: AuthorIntent) {
  if (intent === "write" && !locked.value && !assistant.value)
    await regularAssistant();
  else if (intent === "refine")
    refinementRegion.value?.scrollIntoView({
      behavior: "smooth",
      block: "center",
    });
}
defineExpose({ requestAssistant });
const busy = ref(false),
  saving = ref(false),
  error = ref(""),
  notice = ref("");
const saved = ref("");
const text = computed(() =>
  draft.value ? JSON.stringify(draft.value.document) : "",
);
const dirty = computed(() => !!draft.value && text.value !== saved.value);
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const locked = computed(() => busy.value || ledgerOpen.value || !canEdit.value);
const characters = computed(
  () =>
    branch.value?.snapshot.entities.filter(
      (e) => e.kind === "character" && e.status !== "disabled",
    ) ?? [],
);
const wordCount = computed(
  () => [...(draft.value?.document.text ?? "").replace(/\s/g, "")].length,
);
let disposed = false;
let timer: ReturnType<typeof setTimeout> | undefined;
let pending: Promise<void> | undefined;

function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
function install(value: ChapterDraft) {
  saved.value = JSON.stringify(value.document);
  draft.value = value;
}
async function save(): Promise<void> {
  if (disposed || !draft.value || !canEdit.value) return;
  if (pending) {
    await pending;
    if (dirty.value) return save();
    return;
  }
  if (!dirty.value && draft.value.revision > 0) return;
  clearTimeout(timer);
  const current = draft.value,
    content = text.value;
  saving.value = true;
  pending = (async () => {
    const result = await api<ChapterDraft>(
      `${root}/${current.resource.id}/draft`,
      "PUT",
      {
        revision: current.revision,
        base_head: current.base_head,
        document: JSON.parse(content),
      },
    );
    if (disposed || draft.value?.resource.id !== current.resource.id) return;
    draft.value.revision = result.revision;
    draft.value.hash = result.hash;
    draft.value.ready = result.ready;
    draft.value.reason = result.reason;
    saved.value = content;
  })();
  try {
    await pending;
  } finally {
    pending = undefined;
    if (!disposed) saving.value = false;
  }
}
watch(text, () => {
  if (selectedScope.value?.sourceText !== draft.value?.document.text)
    selectedScope.value = null;
  if (assistant.value && sessionDocument.value !== text.value) {
    sessionChanged.value = true;
    suggestion.value = null;
    notice.value = "正文已修改，旧候选已失效；你的手改会继续暂存。";
  }
  if (!dirty.value || !canEdit.value) return;
  clearTimeout(timer);
  timer = setTimeout(() => void save().catch(fail), 600);
});
async function perform(action: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    await action();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    if (!disposed) busy.value = false;
  }
}
async function close() {
  await perform(async () => {
    await save();
    if (!disposed && !dirty.value && !assistantBlocking.value) emit("close");
  });
}
async function openAssistant() {
  if (assistant.value || busy.value || !canEdit.value) return;
  const sequence = ++assistantSequence.value;
  await perform(async () => {
    await save();
    if (disposed || dirty.value || !draft.value) return;
    const value = await api<ChapterDraft>(
      `${root}/${draft.value.resource.id}/draft`,
    );
    if (disposed) return;
    install(value);
    if (
      sessionScope.value &&
      sessionScope.value.sourceText !== value.document.text
    )
      throw new Error("选段对应的正文已经变化，请重新选择。");
    if (!value.ready) throw new Error(value.reason || "请先核对本章写作依据。");
    await api(`${root}/${value.resource.id}/agents`, "POST", {});
    if (assistantMethod.value)
      await api(`${root}/${value.resource.id}/polish-agent`, "POST", {});
    if (disposed) return;
    branch.value = await api<Branch>(
      `/projects/${props.project.id}/branches/${props.book.branch_id}`,
    );
    if (!disposed) {
      assistantMount.value++;
      assistantCancelled.value = false;
      sessionDocument.value = JSON.stringify(value.document);
      sessionChanged.value = false;
      writingRequest.value = {
        root: `${root}/${value.resource.id}`,
        revision: value.revision,
        hash: value.hash,
        initialNotes: assistantNotes.value,
        method: assistantMethod.value,
      };
      receiveCandidate.value = (candidate) => {
        if (
          disposed ||
          sequence !== assistantSequence.value ||
          !assistant.value ||
          !draft.value
        )
          return;
        try {
          if (
            sessionChanged.value ||
            JSON.stringify(draft.value.document) !== sessionDocument.value ||
            draft.value.revision !== value.revision ||
            draft.value.hash !== value.hash ||
            candidate.resource.id !== value.resource.id ||
            candidate.revision !== value.revision ||
            candidate.hash !== value.hash
          )
            throw new Error(
              "当前章稿已变化，旧候选已失效，请依据新稿重新生成。",
            );
          if (sessionRefining.value) {
            const {
              text: originalText,
              writer_generation_id: originalGeneration,
              ...originalFields
            } = value.document;
            const {
              text: candidateText,
              writer_generation_id: candidateGeneration,
              ...candidateFields
            } = candidate.document;
            void originalText;
            void originalGeneration;
            void candidateGeneration;
            if (
              JSON.stringify(originalFields) !== JSON.stringify(candidateFields)
            )
              throw new Error(
                "精修候选改变了标题或其他章节依据，不能采用。请保持本次精修范围。",
              );
            if (sessionScope.value)
              validateRefinement(
                sessionScope.value,
                draft.value.document.text,
                candidateText,
              );
          }
          suggestion.value = candidate;
        } catch (e) {
          suggestion.value = null;
          fail(e);
        }
      };
      assistant.value = true;
      suggestion.value = null;
    }
  });
}
async function openLedger() {
  if (assistant.value || ledgerOpen.value || !canEdit.value) return;
  await perform(async () => {
    await save();
    if (disposed || dirty.value || !draft.value) return;
    const value = await api<ChapterDraft>(
      `${root}/${draft.value.resource.id}/draft`,
    );
    if (disposed) return;
    install(value);
    if (!value.ready || !value.document.text.trim())
      throw new Error(value.reason || "请先写入本章正文。");
    branch.value = await api<Branch>(
      `/projects/${props.project.id}/branches/${props.book.branch_id}`,
    );
    if (!disposed) ledgerOpen.value = true;
  });
}
async function afterPublication() {
  await perform(async () => {
    if (!draft.value) return;
    const value = await api<ChapterDraft>(
      `${root}/${draft.value.resource.id}/draft`,
    );
    if (!disposed) {
      install(value);
      ledgerOpen.value = false;
      notice.value =
        "本章正文、剧情账本与对应记忆已发布，可以继续编辑或返回规划。";
    }
  });
}
function closeAssistant() {
  // The generation panel owns saved tasks and their explicit cancel action.
  // Leaving this preview invalidates callbacks; it never retries or commits a task.
  ++assistantSequence.value;
  suggestion.value = null;
  if (assistantBlocking.value) {
    assistantCancelled.value = true;
    notice.value =
      "已停止本次候选预览，手动写作可继续。助手输入与任务仍保留；未保存的要求请先保存或清空。";
    return;
  }
  assistant.value = false;
}
function assistantBlocked(value: boolean) {
  assistantBlocking.value = value;
  if (!value && assistantCancelled.value) {
    assistant.value = false;
    assistantCancelled.value = false;
  }
}
function discardAssistantInput() {
  ++assistantSequence.value;
  assistantCancelled.value = false;
  assistant.value = false;
  assistantBlocking.value = false;
  suggestion.value = null;
}
async function refreshBasis() {
  if (assistant.value || ledgerOpen.value || !canEdit.value) return;
  await perform(async () => {
    await save();
    if (!draft.value || disposed || dirty.value) return;
    const value = await api<ChapterDraft>(
      `${root}/${draft.value.resource.id}/basis`,
      "POST",
      { revision: draft.value.revision },
    );
    if (disposed) return;
    draft.value = value;
    await save();
    notice.value =
      "已更新细纲与设定依据，正文原样保留。请核对内容是否需要修改。";
  });
}
async function adopt() {
  if (!suggestion.value || !draft.value || assistantBlocking.value) return;
  await perform(async () => {
    const next = suggestion.value!,
      current = draft.value!;
    if (
      next.resource.id !== current.resource.id ||
      next.revision !== current.revision ||
      next.hash !== current.hash ||
      dirty.value
    )
      throw new Error("当前章稿已变化，请重新生成或查看适用于本版的候选。");
    if (
      sessionChanged.value ||
      sessionDocument.value !== JSON.stringify(current.document)
    )
      throw new Error("正文已有新修改，请重新生成候选。");
    if (sessionScope.value)
      validateRefinement(
        sessionScope.value,
        current.document.text,
        next.document.text,
      );
    const result = await api<ChapterDraft>(
      `${root}/${current.resource.id}/draft`,
      "PUT",
      {
        revision: next.revision,
        base_head: next.base_head,
        document: next.document,
      },
    );
    if (disposed) return;
    install(result);
    suggestion.value = null;
    ++assistantSequence.value;
    assistant.value = false;
    notice.value = "候选已加入私人稿，可继续修改；尚未作为正式章节发布。";
  });
}
function download() {
  if (!draft.value) return;
  const content = `${draft.value.document.title}\n\n${draft.value.document.text}`;
  const url = URL.createObjectURL(
    new Blob([content], { type: "text/plain;charset=utf-8" }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = `${draft.value.document.title.replace(/[<>:"/\\|?*\x00-\x1f]/g, "_").slice(0, 80) || "本章"}-私人稿.txt`;
  link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
function unload(e: BeforeUnloadEvent) {
  if (dirty.value || saving.value || assistantBlocking.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
onMounted(async () => {
  window.addEventListener("beforeunload", unload);
  await perform(async () => {
    const resource = await api<PlanResource>(root, "POST", {
      chapter_id: props.chapterId,
    });
    const [value, currentBranch] = await Promise.all([
      api<ChapterDraft>(`${root}/${resource.id}/draft`),
      api<Branch>(
        `/projects/${props.project.id}/branches/${props.book.branch_id}`,
      ),
    ]);
    if (!disposed) {
      install(value);
      branch.value = currentBranch;
      if (props.initialAction === "refine")
        setTimeout(
          () => refinementRegion.value?.scrollIntoView({ block: "center" }),
          0,
        );
    }
  });
});
onBeforeUnmount(() => {
  disposed = true;
  ++assistantSequence.value;
  clearTimeout(timer);
  window.removeEventListener("beforeunload", unload);
});
</script>

<template>
  <section class="chapter-workspace" aria-label="章节写作">
    <header class="chapter-heading">
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>章节写作</h1>
      </div>
      <button :disabled="busy || assistant || ledgerOpen" @click="close">
        保存并返回规划
      </button>
    </header>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <template v-if="draft">
      <p role="status">
        {{
          saving
            ? "正在保存私人稿…"
            : dirty
              ? "有尚未保存的修改"
              : "私人稿已暂存"
        }}
        · {{ wordCount.toLocaleString() }} 字（不计空白）
      </p>
      <p v-if="!draft.ready" role="alert">
        {{ draft.reason }}。原稿仍可编辑和下载。
      </p>
      <div class="actions">
        <button :disabled="locked" @click="() => perform(save)">
          保存私人稿
        </button>
        <button :disabled="locked || assistant" @click="refreshBasis">
          更新写作依据，保留正文
        </button>
        <button :disabled="locked || assistant" @click="regularAssistant">
          请 Agent 写作或修改
        </button>
        <button :disabled="locked || assistant" @click="openLedger">
          提取与编辑本章账本
        </button>
        <button @click="download">下载当前私人稿</button>
        <button v-if="assistant" :disabled="busy" @click="closeAssistant">
          返回手动写作
        </button>
        <button
          v-if="assistant && assistantCancelled"
          class="quiet"
          :disabled="busy"
          @click="discardAssistantInput"
        >
          放弃助手输入并返回正文
        </button>
      </div>
      <fieldset :disabled="locked" class="chapter-form">
        <label
          >章节标题<input v-model="draft.document.title" maxlength="300"
        /></label>
        <div class="chapter-context">
          <label
            >故事位置<input
              v-model.number="draft.document.story_time"
              type="number"
              min="0"
              max="9007199254740991"
              step="1"
          /></label>
          <label
            >知情视角<select v-model="draft.document.perspective">
              <option value="">按小说叙事方式与场景安排</option>
              <option
                v-for="entity in characters"
                :key="entity.id"
                :value="entity.id"
              >
                {{ entity.name }} · {{ entity.id }}
              </option>
              <option
                v-if="
                  draft.document.perspective &&
                  !characters.some((e) => e.id === draft!.document.perspective)
                "
                :value="draft.document.perspective"
              >
                原视角已不可用，请重新选择
              </option>
            </select></label
          >
        </div>
        <label
          >章节正文<textarea
            v-model="draft.document.text"
            @select="selectQuote"
            rows="24"
            placeholder="可以先手写，也可以让 Agent 依据已确认细纲生成候选。"
          />
        </label>
      </fieldset>
      <div ref="refinementRegion">
        <NovelInlineRefinement
          :text="draft.document.text"
          :selection="selectedScope"
          :disabled="locked || assistant"
          @refine="refineInPlace"
        />
      </div>
      <NovelWritingCard
        :root="`${root}/${draft.resource.id}`"
        :revision="draft.revision"
        :hash="draft.hash"
        :ready="draft.ready"
        :saved="!dirty && !saving"
        @expired="emit('expired')"
      />
      <NovelChapterResearch
        :key="draft.resource.id"
        :root="`${root}/${draft.resource.id}`"
        :project-id="project.id"
        :draft="draft"
        :disabled="locked"
        :saved="!dirty && !saving"
        @change="draft.document.references = $event"
        @expired="emit('expired')"
      />
      <p class="muted">
        正文保存在私人稿中。正式采用还需独立记账、审稿与联合发布。
      </p>
      <NovelLedgerEditor
        v-if="ledgerOpen && branch"
        :root="`${root}/${draft.resource.id}`"
        :chapter="draft"
        :project="project"
        :actor="actor"
        :branch="branch"
        @close="ledgerOpen = false"
        @published="afterPublication"
        @expired="emit('expired')"
      />
      <NovelEditorialDesk
        v-if="!assistant && !ledgerOpen"
        :text="draft.document.text"
        :quote="selectedQuote"
        :disabled="locked"
        @assist="refine"
      />
      <GenerationPanel
        v-if="assistant && branch && writingRequest"
        :key="assistantMount"
        :project="project"
        :actor="actor"
        :branch="branch"
        :writing="writingRequest"
        @chapter-suggestion="receiveCandidate"
        @blocking="assistantBlocked"
        @expired="emit('expired')"
      />
      <section v-if="suggestion" class="panel" aria-label="正文候选对照">
        <h2>正文候选对照</h2>
        <p>检查修改后再加入私人稿。此操作不发布正式章节。</p>
        <p v-if="sessionScope">
          本次只替换已选
          {{ [...sessionScope.quote].length }}
          字；选区之外已经逐字核对保持原样。
        </p>
        <ul v-if="suggestion.notes?.length">
          <li v-for="(note, index) in suggestion.notes" :key="index">
            {{ note }}
          </li>
        </ul>
        <div class="chapter-comparison">
          <article>
            <h3>
              {{ sessionScope ? "选段原文" : "当前原稿" }} ·
              {{ draft.document.title }}
            </h3>
            <pre>{{
              sessionScope?.quote ?? (draft.document.text || "尚无正文")
            }}</pre>
          </article>
          <article>
            <h3>候选 · {{ suggestion.document.title }}</h3>
            <pre>{{
              sessionScope ? refinementCandidate : suggestion.document.text
            }}</pre>
          </article>
        </div>
        <button :disabled="busy || assistantBlocking" @click="adopt">
          将候选加入私人稿
        </button>
        <button :disabled="busy" @click="suggestion = null">
          保留原稿，收起候选
        </button>
      </section>
    </template>
  </section>
</template>

<style scoped>
.chapter-workspace {
  display: grid;
  gap: 1rem;
}
.chapter-heading,
.actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 0.75rem;
  flex-wrap: wrap;
}
.chapter-form {
  display: grid;
  gap: 1rem;
  border: 0;
  padding: 0;
}
.chapter-form label {
  display: grid;
  gap: 0.4rem;
}
.chapter-form textarea {
  width: 100%;
  box-sizing: border-box;
  line-height: 1.9;
  resize: vertical;
  font-size: 1rem;
}
.chapter-context,
.chapter-comparison {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}
.chapter-comparison pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: inherit;
  line-height: 1.9;
  max-height: 36rem;
  overflow: auto;
}
@media (max-width: 760px) {
  .chapter-context,
  .chapter-comparison {
    grid-template-columns: 1fr;
  }
}
</style>
