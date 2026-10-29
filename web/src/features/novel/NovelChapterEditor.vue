<script setup lang="ts">
import {
  computed,
  nextTick,
  onBeforeUnmount,
  onMounted,
  ref,
  watch,
} from "vue";
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
import NovelChapterTextTools from "./NovelChapterTextTools.vue";
import NovelChapterNavigation from "./NovelChapterNavigation.vue";
import {
  chapterTextareaSelection,
  chapterSourceSelection,
  type ChapterTextChange,
} from "./chapterTextTools";
import NovelChapterSaveRecovery from "./NovelChapterSaveRecovery.vue";
import {
  classifyChapterSave,
  sameChapterDocument,
  type ChapterSaveAttempt,
  type ChapterSaveResolution,
} from "./chapterSaveRecovery";
import NovelReviewAnchor from "./NovelReviewAnchor.vue";
import type { BookReviewEditTarget } from "./bookReviewTypes";
import type { StoryLedgerEditTarget } from "./storyLedgerTypes";
import type { ProseSearchEditTarget } from "./proseSearchTypes";
import type { NovelResourceTarget } from "./resourceNavigation";
import NovelBasisComparison from "./NovelBasisComparison.vue";
import NovelChapterOldDraft, {
  type OldDraftTake,
} from "./NovelChapterOldDraft.vue";
import { useBoundedReads } from "../../composables/useBoundedReads";
import { validBasisPreview, type ChapterBasisPreview } from "./chapterBasis";
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
  returnLabel?: string;
  reviewTarget?: BookReviewEditTarget | null;
  storyTarget?: StoryLedgerEditTarget | null;
  searchTarget?: ProseSearchEditTarget | null;
  resourceTarget?: Extract<NovelResourceTarget, { kind: "chapter" }> | null;
  allowChapterNavigation?: boolean;
}>();
const emit = defineEmits<{ close: []; expired: []; navigate: [id: string] }>();
const root = `/novels/${props.book.id}/chapters`;
const basisReads = useBoundedReads({
  stopped: "依据对照读取已停止。",
  timeout: "依据对照读取超时，原稿未改动，可以重新读取。",
});
const draft = ref<ChapterDraft | null>(null);
const branch = ref<Branch | null>(null);
const suggestion = ref<ChapterDraft | null>(null);
const assistant = ref(false),
  assistantBlocking = ref(false);
const ledgerOpen = ref(false);
const oldDraftOpen = ref(false);
const oldDraftUndo = ref<{
  resourceId: string;
  beforeText: string;
  afterDocument: string;
} | null>(null);
const basisVisible = ref(false),
  basisLoading = ref(false),
  basisError = ref("");
const basisPreview = ref<ChapterBasisPreview | null>(null);
let basisSequence = 0;
let basisDocument = "";
const assistantNotes = ref("");
const assistantMethod = ref<"fanqie-polish" | undefined>();
const selectedScope = ref<RefinementScope | null>(null);
const selectedQuote = computed(() => selectedScope.value?.quote ?? "");
const refinementRegion = ref<HTMLElement | null>(null);
const refinementEditor = ref<InstanceType<typeof NovelInlineRefinement> | null>(
  null,
);
const editorialEditor = ref<InstanceType<typeof NovelEditorialDesk> | null>(
  null,
);
const manuscriptField = ref<HTMLTextAreaElement | null>(null);
const reviewAnchorVisible = ref(true);
const activeStoryTarget = computed(() =>
  props.reviewTarget ? null : (props.storyTarget ?? null),
);
const activeSearchTarget = computed(() =>
  props.reviewTarget || props.storyTarget ? null : (props.searchTarget ?? null),
);
const activeResourceTarget = computed(() =>
  props.reviewTarget || props.storyTarget || props.searchTarget
    ? null
    : (props.resourceTarget ?? null),
);
const evidenceTarget = computed(
  () =>
    props.reviewTarget ?? activeStoryTarget.value ?? activeSearchTarget.value,
);
async function locateReviewQuote(value: {
  start: number;
  end: number;
  text: string;
}) {
  if (
    locked.value ||
    assistant.value ||
    !draft.value ||
    draft.value.document.text !== value.text
  )
    return;
  try {
    selectedScope.value = captureRefinement(value.text, value.start, value.end);
  } catch {
    selectedScope.value = null;
  }
  await nextTick();
  if (draft.value?.document.text !== value.text) return;
  const visible = chapterTextareaSelection(value.text, value.start, value.end);
  if (manuscriptField.value?.value !== visible.text) return;
  manuscriptField.value?.focus();
  manuscriptField.value?.setSelectionRange(visible.start, visible.end);
  manuscriptField.value?.scrollIntoView({
    behavior: "smooth",
    block: "center",
  });
}
function addReviewInstruction(value: string) {
  if (!refinementEditor.value?.addInstruction(value)) {
    notice.value =
      "已有精修要求较长或当前不可编辑，请先整理后再带入；原输入保持原样。";
    return;
  }
  notice.value = activeStoryTarget.value
    ? "已补充账本证据到原地精修要求，尚未生成或修改正文、账本。"
    : activeSearchTarget.value
      ? "已补充正文查找命中到原地精修要求，尚未生成或修改正文。"
      : "已补充审稿问题到原地精修要求，尚未生成或修改正文。";
  refinementRegion.value?.scrollIntoView({
    behavior: "smooth",
    block: "center",
  });
}
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
    const source = draft.value?.document.text ?? "";
    if (field.value !== source.replace(/\r\n?/g, "\n"))
      throw new Error("正文已变化");
    const selected = chapterSourceSelection(
      source,
      field.selectionStart,
      field.selectionEnd,
    );
    selectedScope.value = captureRefinement(
      source,
      selected.start,
      selected.end,
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
const unconfirmedSave = ref<ChapterSaveAttempt | null>(null);
let saveSequence = 0;
const focusMode = ref(false);
const textToolsOpen = ref(false);
const manualEditContext = computed(() => {
  if (!draft.value) return "";
  const { text: manuscript, ...context } = draft.value.document;
  void manuscript;
  return JSON.stringify([props.book.id, draft.value.resource.id, context]);
});
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const locked = computed(() => busy.value || ledgerOpen.value || !canEdit.value);
const chapterNavigationAllowed = computed(
  () =>
    !!props.allowChapterNavigation &&
    !props.reviewTarget &&
    !props.storyTarget &&
    !props.searchTarget &&
    !props.resourceTarget,
);
const chapterNavigationBlocked = computed(
  () =>
    locked.value ||
    !draft.value ||
    dirty.value ||
    saving.value ||
    !!unconfirmedSave.value ||
    assistant.value ||
    assistantBlocking.value ||
    basisLoading.value ||
    oldDraftOpen.value ||
    basisVisible.value ||
    !!refinementEditor.value?.hasInput() ||
    !!editorialEditor.value?.hasInput(),
);
function navigateChapter(id: string) {
  if (
    disposed ||
    !chapterNavigationAllowed.value ||
    chapterNavigationBlocked.value ||
    id === props.chapterId
  )
    return;
  emit("navigate", id);
}
watch([assistant, ledgerOpen], () => {
  if (assistant.value || ledgerOpen.value) focusMode.value = false;
});
function toggleFocus() {
  if (busy.value || assistant.value || ledgerOpen.value) return;
  focusMode.value = !focusMode.value;
  void nextTick(() => manuscriptField.value?.focus());
}
function changeChapterText(value: ChapterTextChange) {
  if (
    disposed ||
    !draft.value ||
    locked.value ||
    assistant.value ||
    saving.value ||
    unconfirmedSave.value ||
    basisLoading.value ||
    value.context !== manualEditContext.value ||
    value.sourceText !== draft.value.document.text
  )
    return;
  draft.value.document.text = value.text;
  notice.value =
    value.action === "undo"
      ? "已撤销这次章内替换，正在自动暂存正文。"
      : "替换已加入当前章私人稿，正在自动暂存。请继续核对语意、账本与后续章节。";
}
const canUndoOldDraft = computed(
  () =>
    !!oldDraftUndo.value &&
    oldDraftUndo.value.resourceId === draft.value?.resource.id &&
    oldDraftUndo.value.afterDocument === text.value,
);
function takeOldDraft(value: OldDraftTake) {
  if (
    !draft.value ||
    locked.value ||
    assistant.value ||
    dirty.value ||
    saving.value ||
    unconfirmedSave.value ||
    value.resourceId !== draft.value.resource.id ||
    value.revision !== draft.value.revision ||
    value.hash !== draft.value.hash ||
    value.document !== text.value
  )
    return;
  const next = { ...draft.value.document, text: value.text };
  oldDraftUndo.value = {
    resourceId: draft.value.resource.id,
    beforeText: draft.value.document.text,
    afterDocument: JSON.stringify(next),
  };
  draft.value.document.text = value.text;
  notice.value =
    "旧正文已取回，正在沿现有流程自动暂存为私人稿；其他写作信息保持原样，尚未重新记账、审稿或正式采用。";
}
function undoOldDraft() {
  if (
    !draft.value ||
    !oldDraftUndo.value ||
    !canUndoOldDraft.value ||
    locked.value ||
    assistant.value ||
    dirty.value ||
    saving.value ||
    unconfirmedSave.value
  )
    return;
  const priorText = oldDraftUndo.value.beforeText;
  oldDraftUndo.value = null;
  draft.value.document.text = priorText;
  notice.value = "已撤销这次取回，正在自动暂存替换前正文。";
}
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

function invalidateBasis(reason = "") {
  ++basisSequence;
  basisPreview.value = null;
  basisLoading.value = false;
  basisError.value = reason;
}
function closeBasis() {
  basisVisible.value = false;
  invalidateBasis();
}
watch(
  [
    text,
    () => draft.value?.revision,
    () => draft.value?.hash,
    () => draft.value?.resource.id,
    assistant,
    ledgerOpen,
  ],
  () => {
    if (basisVisible.value)
      invalidateBasis("章稿或写作上下文已变化，请重新读取依据对照。");
  },
  { flush: "sync" },
);

function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && [401, 403].includes(e.status))
    invalidateBasis("当前权限不能继续读取依据对照，请核对登录和访问权限。");
  if (e instanceof APIError && e.status === 401) emit("expired");
}
function install(value: ChapterDraft) {
  const target = activeResourceTarget.value;
  if (target) {
    const resource = value.resource as PlanResource & { branch_id?: string };
    if (
      resource.id !== target.control_id ||
      resource.branch_id !== props.book.branch_id ||
      resource.kind !== "novel_chapter_control" ||
      resource.name !== `章节工作稿 · ${target.chapter_id}` ||
      value.document.chapter_id !== target.chapter_id ||
      (target.resource_kind === "body" &&
        value.document.body_id !== target.resource_id) ||
      (target.resource_kind === "control" && resource.id !== target.resource_id)
    )
      throw new Error("来源资源与当前小说章节不一致，请返回原清单重新读取。");
  }
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
  if (unconfirmedSave.value)
    throw new Error("上次保存结果需要先核对；本页输入仍保留，尚未重发保存。");
  if (
    !dirty.value &&
    (draft.value.revision > 0 ||
      props.storyTarget ||
      props.searchTarget ||
      activeResourceTarget.value)
  )
    return;
  clearTimeout(timer);
  const current = draft.value,
    content = text.value;
  const attempt: ChapterSaveAttempt = {
    id: ++saveSequence,
    resourceId: current.resource.id,
    revision: current.revision,
    hash: current.hash,
    baseHead: current.base_head,
    previous: JSON.parse(saved.value),
    document: JSON.parse(content),
    content,
  };
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
    if (
      result.resource?.id !== attempt.resourceId ||
      result.revision !== attempt.revision + 1 ||
      result.base_head !== attempt.baseHead ||
      !result.document ||
      !sameChapterDocument(result.document, attempt.document) ||
      !result.hash
    )
      throw new Error("保存回执不完整或与提交内容不一致，请先核对已存稿。");
    draft.value.revision = result.revision;
    draft.value.hash = result.hash;
    draft.value.ready = result.ready;
    draft.value.reason = result.reason;
    saved.value = content;
  })();
  try {
    await pending;
  } catch (cause) {
    if (!disposed && draft.value?.resource.id === attempt.resourceId) {
      unconfirmedSave.value = attempt;
      clearTimeout(timer);
    }
    throw cause;
  } finally {
    pending = undefined;
    if (!disposed) saving.value = false;
  }
}
watch(text, () => {
  if (oldDraftUndo.value && text.value !== oldDraftUndo.value.afterDocument)
    oldDraftUndo.value = null;
  if (selectedScope.value?.sourceText !== draft.value?.document.text)
    selectedScope.value = null;
  if (assistant.value && sessionDocument.value !== text.value) {
    sessionChanged.value = true;
    suggestion.value = null;
    notice.value = "正文已修改，旧候选已失效；你的手改会继续暂存。";
  }
  if (!dirty.value || !canEdit.value || unconfirmedSave.value) return;
  clearTimeout(timer);
  timer = setTimeout(() => void save().catch(fail), 600);
});
async function resolveSave(value: ChapterSaveResolution) {
  const attempt = unconfirmedSave.value;
  if (
    !attempt ||
    !draft.value ||
    value.attemptId !== attempt.id ||
    locked.value ||
    saving.value ||
    assistant.value ||
    draft.value.resource.id !== attempt.resourceId ||
    text.value !== value.currentDocument
  )
    return;
  const status = classifyChapterSave(attempt, value.remote);
  if (
    (value.kind === "committed" && status !== "committed") ||
    (value.kind === "retry" && status !== "unchanged")
  )
    return;
  clearTimeout(timer);
  draft.value.revision = value.remote.revision;
  draft.value.hash = value.remote.hash;
  draft.value.ready = value.remote.ready;
  draft.value.reason = value.remote.reason;
  saved.value =
    value.kind === "committed"
      ? attempt.content
      : JSON.stringify(value.remote.document);
  unconfirmedSave.value = null;
  error.value = "";
  if (value.kind === "retry") await perform(save);
  else
    notice.value = dirty.value
      ? "已读回并核实上次提交，本页继续手写的内容保持原样，尚未重新发送；请保存当前修改。"
      : "已读回并核实上次提交，私人稿已保存，没有重发写请求。";
}
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
  if (basisVisible.value)
    invalidateBasis("已切换到写作助手，请返回后重新核对依据。");
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
  if (basisVisible.value)
    invalidateBasis("已切换到账本核对，请返回后重新读取依据对照。");
  await perform(async () => {
    const target = activeStoryTarget.value;
    const resourceTarget = activeResourceTarget.value;
    const existingLedger = resourceTarget?.resource_kind === "ledger";
    if ((!target && !existingLedger) || dirty.value || saving.value)
      await save();
    if (disposed || dirty.value || !draft.value) return;
    const value = await api<ChapterDraft>(
      `${root}/${draft.value.resource.id}/draft`,
    );
    if (disposed) return;
    if (
      target &&
      (!target.control_id ||
        !target.ledger_id ||
        value.resource.id !== target.control_id ||
        value.document.chapter_id !== target.chapter_id)
    )
      throw new Error("账本来源与当前章节资源不一致，请返回剧情账本重新读取。");
    install(value);
    if (
      !target &&
      !existingLedger &&
      (!value.ready || !value.document.text.trim())
    )
      throw new Error(value.reason || "请先写入本章正文。");
    branch.value = await api<Branch>(
      `/projects/${props.project.id}/branches/${props.book.branch_id}`,
    );
    if (!disposed) ledgerOpen.value = true;
  });
}
async function returnToStoryLedger() {
  ledgerOpen.value = false;
  await close();
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
    invalidateBasis();
    await save();
    if (!draft.value || disposed || dirty.value) return;
    basisVisible.value = true;
    basisDocument = text.value;
    const current = {
      id: draft.value.resource.id,
      chapter: draft.value.document.chapter_id,
      revision: draft.value.revision,
      hash: draft.value.hash,
      baseHead: draft.value.base_head,
    };
    const sequence = ++basisSequence;
    basisLoading.value = true;
    const isCurrent = () =>
      !disposed &&
      basisReads.active() &&
      sequence === basisSequence &&
      !!draft.value &&
      basisDocument === text.value &&
      draft.value.resource.id === current.id &&
      draft.value.revision === current.revision &&
      draft.value.hash === current.hash &&
      !dirty.value;
    // A read never holds the editor's write lock or inherits write cancellation.
    void (async () => {
      try {
        const value = await basisReads.read<unknown>(
          `${root}/${current.id}/basis-preview`,
        );
        if (!isCurrent()) return;
        if (
          !validBasisPreview(value) ||
          value.resource_id !== current.id ||
          value.chapter_id !== current.chapter ||
          value.revision !== current.revision ||
          value.hash !== current.hash ||
          value.base_head !== current.baseHead
        )
          throw new Error("依据对照与当前保存稿不一致，请重新读取。");
        basisPreview.value = value;
      } catch (e) {
        if (!isCurrent()) return;
        basisPreview.value = null;
        basisError.value = message(e);
        if (e instanceof APIError && e.status === 401) emit("expired");
      } finally {
        if (isCurrent()) basisLoading.value = false;
      }
    })();
  });
}
async function confirmBasis() {
  if (assistant.value || ledgerOpen.value || !canEdit.value || saving.value)
    return;
  const preview = basisPreview.value;
  const current = draft.value;
  if (
    !preview ||
    !current ||
    !preview.can_refresh ||
    dirty.value ||
    basisDocument !== text.value ||
    preview.resource_id !== current.resource.id ||
    preview.revision !== current.revision ||
    preview.hash !== current.hash
  )
    return;
  const original = JSON.parse(text.value) as ChapterDraft["document"];
  await perform(async () => {
    let value: ChapterDraft;
    try {
      value = await api<ChapterDraft>(
        `${root}/${current.resource.id}/basis`,
        "POST",
        { revision: current.revision, basis_hash: preview.basis_hash },
      );
    } catch (e) {
      if (e instanceof APIError && [401, 403, 409].includes(e.status))
        invalidateBasis("依据或权限已经变化，请重新读取对照后再确认。");
      throw e;
    }
    if (disposed) return;
    const {
      detail: oldDetail,
      settings_revision: oldRevision,
      settings_hash: oldHash,
      ip_head: oldIP,
      ...oldManuscript
    } = original;
    const {
      detail: newDetail,
      settings_revision: newRevision,
      settings_hash: newHash,
      ip_head: newIP,
      ...newManuscript
    } = value.document;
    void oldDetail;
    void oldRevision;
    void oldHash;
    void oldIP;
    void newDetail;
    void newRevision;
    void newHash;
    void newIP;
    if (
      value.resource.id !== current.resource.id ||
      value.revision !== current.revision ||
      JSON.stringify(oldManuscript) !== JSON.stringify(newManuscript)
    ) {
      invalidateBasis(
        "更新回执与原稿不一致，未替换当前正文。请重新核对保存状态。",
      );
      throw new Error("写作依据更新回执不匹配，当前手稿保持原样。");
    }
    draft.value = value;
    await save();
    if (disposed) return;
    closeBasis();
    notice.value =
      "已更新细纲与设定依据，正文原样保留。请按刚才的变化核对正文，可继续手改或使用下方原地精修；尚未重新记账、审稿或正式采用。";
  });
}
async function adopt() {
  if (
    !suggestion.value ||
    !draft.value ||
    assistantBlocking.value ||
    unconfirmedSave.value
  )
    return;
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
  if (
    dirty.value ||
    saving.value ||
    assistantBlocking.value ||
    unconfirmedSave.value
  ) {
    e.preventDefault();
    e.returnValue = "";
  }
}
onMounted(async () => {
  window.addEventListener("beforeunload", unload);
  await perform(async () => {
    const target =
      activeStoryTarget.value ??
      activeSearchTarget.value ??
      activeResourceTarget.value;
    if (target && (!target.control_id || target.chapter_id !== props.chapterId))
      throw new Error(
        activeSearchTarget.value
          ? "正文查找来源缺少已有章节资源，不能打开正文；请返回正文查找重新读取。"
          : "账本来源缺少已有章节资源，不能打开正文；请返回剧情账本重新读取。",
      );
    const resourceTarget = activeResourceTarget.value;
    if (
      resourceTarget?.resource_kind === "ledger" &&
      (!resourceTarget.ledger_id ||
        resourceTarget.ledger_id !== resourceTarget.resource_id)
    )
      throw new Error("来源缺少准确的已有账本资源，请返回原清单重新读取。");
    const resource = target
      ? { id: target.control_id }
      : await api<PlanResource>(root, "POST", {
          chapter_id: props.chapterId,
        });
    const [value, currentBranch] = await Promise.all([
      api<ChapterDraft>(`${root}/${resource.id}/draft`),
      api<Branch>(
        `/projects/${props.project.id}/branches/${props.book.branch_id}`,
      ),
    ]);
    if (!disposed) {
      if (
        target &&
        (value.resource.id !== target.control_id ||
          value.document.chapter_id !== target.chapter_id)
      )
        throw new Error(
          activeSearchTarget.value
            ? "正文查找来源与当前章节资源不一致，请返回正文查找重新读取。"
            : "账本来源与当前章节资源不一致，请返回剧情账本重新读取。",
        );
      if (
        activeResourceTarget.value &&
        currentBranch.id !== props.book.branch_id
      )
        throw new Error("当前分支与来源小说不一致，请返回原清单重新读取。");
      install(value);
      branch.value = currentBranch;
      if (props.initialAction === "refine")
        setTimeout(
          () => refinementRegion.value?.scrollIntoView({ block: "center" }),
          0,
        );
    }
  });
  if (
    !disposed &&
    (activeStoryTarget.value?.open_ledger ||
      activeResourceTarget.value?.resource_kind === "ledger") &&
    draft.value &&
    branch.value
  )
    await openLedger();
});
onBeforeUnmount(() => {
  disposed = true;
  ++assistantSequence.value;
  clearTimeout(timer);
  window.removeEventListener("beforeunload", unload);
});
</script>

<template>
  <section
    class="chapter-workspace"
    :class="{ 'focus-mode': focusMode }"
    aria-label="章节写作"
  >
    <header class="chapter-heading">
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>章节写作</h1>
      </div>
      <button :disabled="busy || assistant || ledgerOpen" @click="close">
        {{ returnLabel ?? "保存并返回规划" }}
      </button>
    </header>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <template v-if="draft">
      <NovelChapterNavigation
        v-if="chapterNavigationAllowed"
        :book-id="book.id"
        :chapter-id="chapterId"
        :blocked="chapterNavigationBlocked"
        @navigate="navigateChapter"
        @expired="emit('expired')"
      />
      <p role="status">
        {{
          unconfirmedSave
            ? "上次保存待核对，本页稿仍保留"
            : saving
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
      <div class="actions chapter-tools">
        <button
          class="focus-keep"
          :disabled="busy || assistant || ledgerOpen"
          :aria-pressed="focusMode"
          @click="toggleFocus"
        >
          {{ focusMode ? "退出专注写作" : "进入专注写作" }}
        </button>
        <button
          class="focus-keep"
          :aria-expanded="textToolsOpen"
          @click="textToolsOpen = !textToolsOpen"
        >
          章内查找与替换
        </button>
        <button
          class="focus-keep"
          :disabled="locked || !!unconfirmedSave"
          @click="() => perform(save)"
        >
          保存私人稿
        </button>
        <button
          :disabled="locked || assistant || basisLoading || !!unconfirmedSave"
          @click="refreshBasis"
        >
          更新写作依据，保留正文
        </button>
        <button
          :disabled="locked || assistant || !!unconfirmedSave"
          @click="regularAssistant"
        >
          请 Agent 写作或修改
        </button>
        <button
          :disabled="locked || assistant || !!unconfirmedSave"
          @click="openLedger"
        >
          {{ activeStoryTarget ? "打开对应账本" : "提取与编辑本章账本" }}
        </button>
        <button
          :disabled="locked || assistant || !!unconfirmedSave"
          @click="oldDraftOpen = !oldDraftOpen"
        >
          对照与取回旧稿
        </button>
        <button class="focus-keep" @click="download">下载当前私人稿</button>
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
      <NovelChapterSaveRecovery
        v-if="unconfirmedSave"
        :root="root"
        :attempt="unconfirmedSave"
        :current="draft"
        :disabled="locked || assistant || saving"
        @resolve="resolveSave"
        @expired="emit('expired')"
      />
      <NovelChapterOldDraft
        v-if="oldDraftOpen"
        v-show="!focusMode"
        :book-id="book.id"
        :current="draft"
        :saved="!dirty && !saving && !unconfirmedSave"
        :disabled="locked || assistant || !!unconfirmedSave"
        :undo-available="canUndoOldDraft"
        @take="takeOldDraft"
        @undo="undoOldDraft"
        @close="oldDraftOpen = false"
        @expired="emit('expired')"
      />
      <NovelBasisComparison
        v-if="basisVisible"
        v-show="!focusMode"
        :preview="basisPreview"
        :busy="basisLoading"
        :error="basisError"
        :disabled="locked || assistant || saving || dirty || !!unconfirmedSave"
        @retry="refreshBasis"
        @confirm="confirmBasis"
        @close="closeBasis"
      />
      <NovelReviewAnchor
        v-show="!focusMode"
        v-if="
          evidenceTarget &&
          reviewAnchorVisible &&
          evidenceTarget.chapter_id === draft.document.chapter_id
        "
        :target="evidenceTarget"
        :label="
          activeStoryTarget
            ? '账本证据'
            : activeSearchTarget
              ? '正文查找'
              : undefined
        "
        :text="draft.document.text"
        :hash="draft.hash"
        :saved="!dirty && !saving && !unconfirmedSave"
        :disabled="locked || assistant || !!unconfirmedSave"
        @locate="locateReviewQuote"
        @refine="addReviewInstruction"
        @close="reviewAnchorVisible = false"
      />
      <NovelChapterTextTools
        v-show="textToolsOpen"
        :text="draft.document.text"
        :context="manualEditContext"
        :disabled="
          locked || assistant || saving || basisLoading || !!unconfirmedSave
        "
        @change="changeChapterText"
        @locate="locateReviewQuote"
        @close="textToolsOpen = false"
      />
      <fieldset :disabled="locked" class="chapter-form">
        <label
          >章节标题<input v-model="draft.document.title" maxlength="300"
        /></label>
        <div v-show="!focusMode" class="chapter-context">
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
            ref="manuscriptField"
            v-model="draft.document.text"
            @select="selectQuote"
            rows="24"
            placeholder="可以先手写，也可以让 Agent 依据已确认细纲生成候选。"
          />
        </label>
      </fieldset>
      <div v-show="!focusMode" ref="refinementRegion">
        <NovelInlineRefinement
          ref="refinementEditor"
          :text="draft.document.text"
          :selection="selectedScope"
          :disabled="locked || assistant || !!unconfirmedSave"
          @refine="refineInPlace"
        />
      </div>
      <NovelWritingCard
        v-show="!focusMode"
        :root="`${root}/${draft.resource.id}`"
        :revision="draft.revision"
        :hash="draft.hash"
        :ready="draft.ready"
        :saved="!dirty && !saving && !unconfirmedSave"
        @expired="emit('expired')"
      />
      <NovelChapterResearch
        v-show="!focusMode"
        :key="draft.resource.id"
        :root="`${root}/${draft.resource.id}`"
        :project-id="project.id"
        :draft="draft"
        :disabled="locked || !!unconfirmedSave"
        :saved="!dirty && !saving && !unconfirmedSave"
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
        :existing-ledger-id="
          activeStoryTarget?.ledger_id ??
          (activeResourceTarget?.resource_kind === 'ledger'
            ? activeResourceTarget.ledger_id
            : undefined)
        "
        :expected-control-id="
          activeResourceTarget?.resource_kind === 'ledger'
            ? activeResourceTarget.control_id
            : undefined
        "
        :focus-entry-id="activeStoryTarget?.entry_id"
        :expected-ledger-hash="activeStoryTarget?.ledger_hash"
        :source-return-label="
          activeStoryTarget
            ? '保存账本并返回剧情账本'
            : activeResourceTarget?.resource_kind === 'ledger'
              ? '保存账本并返回修订清单'
              : undefined
        "
        @close="ledgerOpen = false"
        @source-return="returnToStoryLedger"
        @published="afterPublication"
        @expired="emit('expired')"
      />
      <NovelEditorialDesk
        ref="editorialEditor"
        v-if="!assistant && !ledgerOpen"
        v-show="!focusMode"
        :text="draft.document.text"
        :quote="selectedQuote"
        :disabled="locked || !!unconfirmedSave"
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
        <button
          :disabled="busy || assistantBlocking || !!unconfirmedSave"
          @click="adopt"
        >
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
.focus-mode {
  width: min(100%, 64rem);
  margin-inline: auto;
  min-width: 0;
}
.focus-mode .chapter-tools button:not(.focus-keep) {
  display: none;
}
.focus-mode .chapter-form textarea {
  min-height: 65vh;
  font-size: 1.125rem;
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
