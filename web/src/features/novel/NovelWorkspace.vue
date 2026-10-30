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
import type { PlanningOverview } from "./planningTypes";
import { workspaceStage, type AuthorIntent } from "./authorWorkbench";
import NovelAuthorAssistant from "./NovelAuthorAssistant.vue";
import NovelChapterCatalog from "./NovelChapterCatalog.vue";
import IPPage from "../ip/IPPage.vue";
import NovelSettingsEditor from "./NovelSettingsEditor.vue";
import NovelMaterials from "./NovelMaterials.vue";
import NovelPlanning from "./NovelPlanning.vue";
import NovelChapterEditor from "./NovelChapterEditor.vue";
import NovelBatchDesk from "./NovelBatchDesk.vue";
import NovelExportDesk from "./NovelExportDesk.vue";
import NovelBookReviewDesk from "./NovelBookReviewDesk.vue";
import NovelCharacterDesigner from "./NovelCharacterDesigner.vue";
import NovelImages from "./NovelImages.vue";
import NovelResearchDesk from "./NovelResearchDesk.vue";
import NovelSourceReview from "./NovelSourceReview.vue";
import NovelStoryLedger from "./NovelStoryLedger.vue";
import NovelProseSearch from "./NovelProseSearch.vue";
import NovelManuscriptDesk from "./NovelManuscriptDesk.vue";
import {
  initialProseSearchView,
  type ProseSearchView,
} from "./proseSearchReads";
import type { ProseSearchEditTarget } from "./proseSearchTypes";
import { useBoundedReads } from "../../composables/useBoundedReads";
import {
  resolveNovelResource,
  type ResourceNavigationRequest,
  type NovelResourceTarget,
} from "./resourceNavigation";
import {
  initialStoryLedgerView,
  type StoryLedgerView,
} from "./storyLedgerReads";
import type { StoryLedgerEditTarget } from "./storyLedgerTypes";
import type {
  BookReviewEditTarget,
  BookReviewViewState,
} from "./bookReviewTypes";
const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
}>();
const emit = defineEmits<{
  close: [];
  expired: [];
  updated: [book: NovelBook];
}>();
const editing = ref(false);
const focusCharacters = ref(false);
const settings = ref(false);
const materials = ref(false);
const planning = ref(false);
const chapterId = ref("");
const batch = ref(false);
const manuscript = ref(false),
  manuscriptBatchId = ref(""),
  returnToManuscript = ref(false);
const exporting = ref(false);
const reviewing = ref(false);
const designingCharacters = ref(false);
const images = ref(false);
const research = ref(false);
const sourceReview = ref(false);
const storyLedger = ref(false);
const storyLedgerView = ref<StoryLedgerView>(initialStoryLedgerView());
const storyLedgerTarget = ref<StoryLedgerEditTarget | null>(null);
const returnToStoryLedger = ref(false);
const proseSearch = ref(false);
const proseSearchView = ref<ProseSearchView>(initialProseSearchView());
const proseSearchTarget = ref<ProseSearchEditTarget | null>(null);
const returnToProseSearch = ref(false);
const planningResourceId = ref("");
const returnToReview = ref(false);
const reviewFilters = ref({ query: "", kind: "all" });
const bookReviewView = ref<BookReviewViewState>({
  runId: "",
  filters: {
    query: "",
    severity: "",
    category: "",
    chapterId: "",
    disposition: "",
  },
  focusedIssue: "",
});
const returnToBookReview = ref(false);
const bookReviewTarget = ref<BookReviewEditTarget | null>(null);
const researchId = ref("");
const book = ref(props.book);
const overview = ref<PlanningOverview | null>(null);
const loadingStage = ref(false);
const stageError = ref("");
const stage = computed(() => workspaceStage(overview.value?.stage));
const homeSection = ref("overview");
const homeSections = [
  { id: "overview", label: "本书概览" },
  { id: "planning", label: "故事规划" },
  { id: "writing", label: "章节写作" },
  { id: "library", label: "人物与资料" },
  { id: "review", label: "审稿与导出" },
];
type ChapterOrigin = "home" | "planning" | "batch" | "export";
const chapterOrigin = ref<ChapterOrigin>("home");
const ordinaryReturnLabel = computed(
  () =>
    ({
      home: "保存并返回本书工作区",
      planning: "保存并返回章节规划",
      batch: "保存并返回批量连写",
      export: "保存并返回成书导出",
    })[chapterOrigin.value],
);
const choosingChapter = ref<"write" | "refine" | null>(null);
const chapterChoices = ref<
  { id: string; title: string; ready: boolean; reason: string }[]
>([]);
const loadingChapters = ref(false);
const chapterError = ref("");
const chapterEditor = ref<InstanceType<typeof NovelChapterEditor> | null>(null);
const initialChapterAction = ref<"write" | "refine" | undefined>();
const ipEditor = ref<InstanceType<typeof IPPage> | null>(null);
const settingsEditor = ref<InstanceType<typeof NovelSettingsEditor> | null>(
  null,
);
const navigationLoading = ref(false),
  navigationError = ref(""),
  navigationNotice = ref("");
type ResourceOrigin = "ip" | "settings";
const changeSession = ref<{
  origin: ResourceOrigin;
  request: ResourceNavigationRequest;
  target: NovelResourceTarget;
  scroll: number;
} | null>(null);
const resourceTarget = ref<Extract<
  NovelResourceTarget,
  { kind: "chapter" }
> | null>(null);
const navigationRetry = ref<{
  origin: ResourceOrigin;
  request: ResourceNavigationRequest;
} | null>(null);
const navigationReads = useBoundedReads({
  stopped: "资料定位已停止。",
  timeout: "资料定位读取超时，原清单与输入已保留，可以重试。",
});
let navigationSequence = 0;
const changeReturnLabel = computed(() =>
  changeSession.value?.origin === "settings"
    ? "保存并返回设置差异"
    : "保存并返回变更清单",
);
const onHome = computed(
  () =>
    !sourceReview.value &&
    !storyLedger.value &&
    !proseSearch.value &&
    !research.value &&
    !images.value &&
    !chapterId.value &&
    !designingCharacters.value &&
    !reviewing.value &&
    !exporting.value &&
    !batch.value &&
    !manuscript.value &&
    !planning.value &&
    !materials.value &&
    !settings.value &&
    !editing.value,
);
let disposed = false;
let chapterRequest = 0;
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const ordinaryChapterNavigation = computed(
  () =>
    !changeSession.value &&
    !returnToReview.value &&
    !returnToBookReview.value &&
    !returnToStoryLedger.value &&
    !returnToProseSearch.value &&
    !returnToManuscript.value &&
    !bookReviewTarget.value &&
    !storyLedgerTarget.value &&
    !proseSearchTarget.value &&
    !resourceTarget.value,
);
function navigateChapter(id: string) {
  if (chapterId.value && ordinaryChapterNavigation.value)
    openCatalogChapter(id);
}
function updated(value: NovelBook) {
  book.value = value;
  settings.value = false;
  emit("updated", value);
  void loadStage();
}
async function loadStage() {
  loadingStage.value = true;
  stageError.value = "";
  try {
    const value = await api<PlanningOverview>(`/novels/${book.value.id}/plans`);
    if (!disposed) overview.value = value;
  } catch (e) {
    if (!disposed) {
      stageError.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (!disposed) loadingStage.value = false;
  }
}
async function routeIntent(intent: AuthorIntent) {
  if (chapterId.value) {
    await chapterEditor.value?.requestAssistant(intent);
    return;
  }
  if (!onHome.value) return;
  if (intent === "planning") openPlanning();
  else if (intent === "characters") {
    focusCharacters.value = true;
    editing.value = true;
  } else if (intent === "materials") materials.value = true;
  else if (intent === "batch") batch.value = true;
  else {
    homeSection.value = "writing";
    const request = ++chapterRequest;
    choosingChapter.value = intent;
    loadingChapters.value = true;
    chapterError.value = "";
    chapterChoices.value = [];
    try {
      const result = await api<{ items: typeof chapterChoices.value }>(
        `/novels/${book.value.id}/writing-batches/chapters`,
      );
      if (!disposed && request === chapterRequest)
        chapterChoices.value = result.items;
    } catch (e) {
      if (!disposed && request === chapterRequest) {
        chapterError.value = message(e);
        if (e instanceof APIError && e.status === 401) emit("expired");
      }
    } finally {
      if (!disposed && request === chapterRequest)
        loadingChapters.value = false;
    }
  }
}
function chooseChapter(id: string) {
  const choice = chapterChoices.value.find((item) => item.id === id);
  if (!choice?.ready) return;
  initialChapterAction.value = choosingChapter.value ?? undefined;
  chapterOrigin.value = "home";
  chapterId.value = id;
  choosingChapter.value = null;
  ++chapterRequest;
}
function closeChapter() {
  if (returnToManuscript.value) {
    chapterId.value = "";
    manuscript.value = true;
    returnToManuscript.value = false;
    return;
  }
  if (changeSession.value) {
    chapterId.value = "";
    initialChapterAction.value = undefined;
    resourceTarget.value = null;
    void returnFromResource();
    return;
  }
  chapterId.value = "";
  initialChapterAction.value = undefined;
  reviewing.value = returnToBookReview.value;
  storyLedger.value = returnToStoryLedger.value;
  proseSearch.value = returnToProseSearch.value;
  const ordinary =
    !returnToReview.value &&
    !returnToBookReview.value &&
    !returnToStoryLedger.value &&
    !returnToProseSearch.value;
  exporting.value = ordinary && chapterOrigin.value === "export";
  batch.value = ordinary && chapterOrigin.value === "batch";
  planning.value = ordinary && chapterOrigin.value === "planning";
  if (!planning.value) planningResourceId.value = "";
  returnToProseSearch.value = false;
  proseSearchTarget.value = null;
  returnToStoryLedger.value = false;
  storyLedgerTarget.value = null;
  returnToBookReview.value = false;
  bookReviewTarget.value = null;
  if (returnToReview.value) {
    sourceReview.value = true;
    returnToReview.value = false;
  }
  void loadStage();
}
function openReviewPlanning(id: string) {
  planningResourceId.value = id;
  returnToReview.value = true;
  sourceReview.value = false;
  planning.value = true;
}
function openBookReviewIssue(target: BookReviewEditTarget) {
  bookReviewTarget.value = target;
  returnToBookReview.value = true;
  openCatalogChapter(target.chapter_id);
}
function openStoryLedgerEntry(target: StoryLedgerEditTarget) {
  storyLedgerTarget.value = target;
  bookReviewTarget.value = null;
  returnToReview.value = false;
  returnToBookReview.value = false;
  returnToStoryLedger.value = true;
  storyLedger.value = false;
  openCatalogChapter(target.chapter_id);
}
function openProseSearchMatch(target: ProseSearchEditTarget) {
  proseSearchTarget.value = target;
  bookReviewTarget.value = null;
  storyLedgerTarget.value = null;
  returnToReview.value = false;
  returnToBookReview.value = false;
  returnToStoryLedger.value = false;
  returnToProseSearch.value = true;
  proseSearch.value = false;
  openCatalogChapter(target.chapter_id);
}
function openReviewChapter(id: string) {
  returnToReview.value = true;
  sourceReview.value = false;
  openCatalogChapter(id);
}
function closePlanning() {
  if (changeSession.value) {
    planning.value = false;
    planningResourceId.value = "";
    void returnFromResource();
    return;
  }
  planning.value = false;
  planningResourceId.value = "";
  if (returnToReview.value) {
    sourceReview.value = true;
    returnToReview.value = false;
  }
}
function openCatalogChapter(id: string) {
  initialChapterAction.value = undefined;
  chapterId.value = id;
  choosingChapter.value = null;
  ++chapterRequest;
}
function openChapterFrom(id: string, origin: ChapterOrigin) {
  chapterOrigin.value = origin;
  openCatalogChapter(id);
}
function openPlanning() {
  const kind =
    overview.value?.stage === "OUTLINE_PLANNING"
      ? "novel_plan_book"
      : "novel_plan_world";
  planningResourceId.value =
    overview.value?.resources.find((item) => item.kind === kind)?.id ?? "";
  planning.value = true;
}
function continueStage() {
  if (overview.value?.stage === "DETAIL_PLANNING")
    homeSection.value = "writing";
  else openPlanning();
}
async function openResource(
  request: ResourceNavigationRequest,
  origin: ResourceOrigin,
) {
  if (
    navigationLoading.value ||
    changeSession.value ||
    !canEdit.value ||
    (origin === "ip" ? !editing.value : !settings.value)
  )
    return;
  const sequence = ++navigationSequence;
  const scope = {
    project_id: book.value.project_id,
    branch_id: book.value.branch_id,
  };
  const scroll = window.scrollY;
  navigationLoading.value = true;
  navigationError.value = "";
  navigationNotice.value = "";
  navigationRetry.value = { origin, request };
  try {
    const value = await navigationReads.read<unknown>(
      `/projects/${encodeURIComponent(scope.project_id)}/resources?branch=${encodeURIComponent(scope.branch_id)}`,
    );
    if (
      !navigationReads.active() ||
      sequence !== navigationSequence ||
      (origin === "ip" ? !editing.value : !settings.value)
    )
      return;
    const sourceReady =
      origin === "ip"
        ? ipEditor.value?.canOpenNovelResource()
        : settingsEditor.value?.canOpenNovelResource();
    if (!canEdit.value || !sourceReady) {
      navigationError.value =
        "原页面有未保存修改或操作正在进行，请先完成后再打开资料。输入已保留。";
      return;
    }
    const target = resolveNovelResource(value, scope, request.ref);
    changeSession.value = { origin, request, target, scroll };
    editing.value = false;
    settings.value = false;
    bookReviewTarget.value = null;
    storyLedgerTarget.value = null;
    proseSearchTarget.value = null;
    returnToReview.value = false;
    returnToBookReview.value = false;
    returnToStoryLedger.value = false;
    returnToProseSearch.value = false;
    navigationRetry.value = null;
    if (target.kind === "planning") {
      planningResourceId.value = target.resource_id;
      planning.value = true;
    } else {
      resourceTarget.value = target;
      openCatalogChapter(target.chapter_id);
    }
  } catch (cause) {
    if (!navigationReads.active() || sequence !== navigationSequence) return;
    navigationError.value = message(cause);
    if (cause instanceof APIError && cause.status === 401) emit("expired");
  } finally {
    if (sequence === navigationSequence) navigationLoading.value = false;
  }
}
async function returnFromResource() {
  const session = changeSession.value;
  if (!session) return;
  editing.value = session.origin === "ip";
  settings.value = session.origin === "settings";
  navigationLoading.value = false;
  await nextTick();
  try {
    if (session.origin === "ip") await ipEditor.value?.afterNovelEdit();
    else await settingsEditor.value?.afterNovelEdit();
    navigationNotice.value =
      "已返回原清单。私人修改不等于正式采用或独立审查通过；原报告和处理记录保留，仍需核对当前版本。";
  } catch (cause) {
    navigationError.value = message(cause);
    if (cause instanceof APIError && cause.status === 401) emit("expired");
  } finally {
    changeSession.value = null;
    await nextTick();
    const button = document.querySelector<HTMLElement>(
      `[data-novel-focus="${CSS.escape(session.request.focus_key)}"]`,
    );
    button?.focus({ preventScroll: true });
    window.scrollTo({ top: session.scroll });
  }
}
onMounted(loadStage);
watch(onHome, (home, previous) => {
  if (home && !previous) {
    ++navigationSequence;
    navigationLoading.value = false;
    navigationRetry.value = null;
    navigationError.value = "";
    navigationNotice.value = "";
    void loadStage();
  }
});
onBeforeUnmount(() => {
  disposed = true;
  ++chapterRequest;
  ++navigationSequence;
});
</script>

<template>
  <div class="book-shell">
    <section
      v-if="navigationLoading || navigationError || navigationNotice"
      class="panel"
      aria-label="修订导航"
    >
      <p v-if="navigationLoading">正在核对现有资料与小说章节…</p>
      <p v-if="navigationError" role="alert">{{ navigationError }}</p>
      <p v-if="navigationNotice" role="status">{{ navigationNotice }}</p>
      <button
        v-if="navigationRetry && navigationError"
        :disabled="navigationLoading"
        @click="openResource(navigationRetry.request, navigationRetry.origin)"
      >
        重试打开资料
      </button>
    </section>
    <section
      v-if="changeSession && (chapterId || planning)"
      class="panel"
      aria-label="本次修订任务"
    >
      <h2>{{ changeSession.request.title }}</h2>
      <p>{{ changeSession.request.explanation }}</p>
      <p
        v-if="
          changeSession.target.source_revision !==
          changeSession.target.current_head
        "
      >
        原清单引用的正式版本已变化，请核对当前资料；不能把依赖组合摘要当作当前正文摘要。
      </p>
      <p>
        此处打开当前作者已保存稿或已有正式稿。原问题不是新稿审查结果；修改后返回原清单复核，不会自动采用、改写记忆或启动模型。
      </p>
    </section>
    <NovelSettingsEditor
      v-if="settings || changeSession?.origin === 'settings'"
      v-show="settings"
      ref="settingsEditor"
      :book="book"
      :novel-navigation="canEdit && !navigationLoading"
      @edit-resource="openResource($event, 'settings')"
      @close="settings = false"
      @updated="updated"
      @expired="emit('expired')"
    />
    <IPPage
      v-if="editing || changeSession?.origin === 'ip'"
      v-show="editing"
      ref="ipEditor"
      :project="project"
      :actor="actor"
      :initial-branch-id="book.branch_id"
      :focus-characters="focusCharacters"
      :novel-navigation="canEdit"
      :novel-navigation-busy="navigationLoading"
      return-label="返回本书工作区"
      @edit-resource="openResource($event, 'ip')"
      @close="editing = false"
      @expired="emit('expired')"
    />
    <NovelProseSearch
      v-if="proseSearch"
      :book-id="book.id"
      :initial-view="proseSearchView"
      @view="proseSearchView = $event"
      @edit="openProseSearchMatch"
      @expired="emit('expired')"
      @close="proseSearch = false"
    />
    <NovelStoryLedger
      v-else-if="storyLedger"
      :book-id="book.id"
      :initial-view="storyLedgerView"
      @view="storyLedgerView = $event"
      @edit="openStoryLedgerEntry"
      @expired="emit('expired')"
      @close="storyLedger = false"
    />
    <NovelSourceReview
      v-else-if="sourceReview"
      :book-id="book.id"
      :title="book.setup.title"
      :initial-filters="reviewFilters"
      @filters="reviewFilters = $event"
      @close="sourceReview = false"
      @research="
        researchId = $event;
        sourceReview = false;
        research = true;
      "
      @chapter="openReviewChapter"
      @planning="openReviewPlanning"
      @materials="
        sourceReview = false;
        materials = true;
      "
      @expired="emit('expired')"
    />
    <NovelResearchDesk
      v-else-if="research"
      :initial-resource-id="researchId"
      :book="book"
      :project="project"
      :actor="actor"
      @close="
        research = false;
        researchId = '';
      "
      @materials="
        research = false;
        materials = true;
      "
      @expired="emit('expired')"
    />
    <NovelImages
      v-else-if="images"
      :book="book"
      :project="project"
      :actor="actor"
      @close="images = false"
      @expired="emit('expired')"
    />
    <NovelChapterEditor
      v-else-if="chapterId"
      :key="chapterId"
      :book="book"
      :project="project"
      :actor="actor"
      :chapter-id="chapterId"
      :allow-chapter-navigation="ordinaryChapterNavigation"
      :initial-action="initialChapterAction"
      :review-target="bookReviewTarget"
      :story-target="storyLedgerTarget"
      :search-target="proseSearchTarget"
      :resource-target="resourceTarget"
      :return-label="
        returnToManuscript
          ? '保存并返回长稿导入'
          : changeSession
            ? changeReturnLabel
            : returnToProseSearch
              ? '保存并返回正文查找'
              : returnToStoryLedger
                ? '保存并返回剧情账本'
                : returnToBookReview
                  ? '保存并返回审稿报告'
                  : returnToReview
                    ? '保存并返回待复核清单'
                    : ordinaryReturnLabel
      "
      ref="chapterEditor"
      @close="closeChapter"
      @navigate="navigateChapter"
      @expired="emit('expired')"
    />
    <NovelCharacterDesigner
      v-else-if="designingCharacters"
      :book="book"
      :project="project"
      :actor="actor"
      @close="designingCharacters = false"
      @library="
        designingCharacters = false;
        focusCharacters = true;
        editing = true;
      "
      @expired="emit('expired')"
    />
    <NovelBookReviewDesk
      v-else-if="reviewing"
      :book="book"
      :project="project"
      :initial-run-id="bookReviewView.runId"
      :initial-filters="bookReviewView.filters"
      :initial-focused-issue="bookReviewView.focusedIssue"
      @view="bookReviewView = $event"
      @close="reviewing = false"
      @edit="openBookReviewIssue"
      @expired="emit('expired')"
    />
    <NovelExportDesk
      v-else-if="exporting"
      :book="book"
      :can-save-edition="
        ['owner', 'publisher'].includes(actor.role) &&
        ['owner', 'publisher'].includes(project.role)
      "
      @close="exporting = false"
      @edit="openChapterFrom($event, 'export')"
      @expired="emit('expired')"
    />
    <NovelBatchDesk
      v-else-if="batch"
      :book="book"
      :project="project"
      :actor="actor"
      @close="batch = false"
      @edit="openChapterFrom($event, 'batch')"
      @expired="emit('expired')"
    />
    <NovelManuscriptDesk
      v-else-if="manuscript"
      :book="book"
      :initial-batch-id="manuscriptBatchId"
      :can-edit="canEdit"
      @close="manuscript = false"
      @selected="manuscriptBatchId = $event"
      @edit="
        manuscript = false;
        returnToManuscript = true;
        chapterId = $event;
      "
      @expired="emit('expired')"
    />
    <NovelPlanning
      v-else-if="planning"
      :initial-resource-id="planningResourceId"
      :return-label="
        changeSession
          ? changeReturnLabel
          : returnToReview
            ? '保存并返回待复核清单'
            : undefined
      "
      :book="book"
      :project="project"
      :actor="actor"
      @close="closePlanning"
      @write="openChapterFrom($event, 'planning')"
      @expired="emit('expired')"
    />
    <NovelMaterials
      v-else-if="materials"
      :book="book"
      :project="project"
      :actor="actor"
      @close="materials = false"
      @expired="emit('expired')"
    />
    <section v-else-if="onHome" class="novel-workspace">
      <header class="section-heading">
        <div>
          <p class="eyebrow">小说工作台</p>
          <h1>{{ book.setup.title }}</h1>
          <p>
            {{ book.setup.genre }} · {{ book.setup.language }} ·
            {{ book.setup.target_words.toLocaleString() }}字计划
          </p>
        </div>
        <button class="quiet" @click="emit('close')">返回书架</button>
      </header>
      <nav class="task-navigation" aria-label="本书工作区">
        <button
          v-for="section in homeSections"
          :key="section.id"
          class="quiet"
          :aria-pressed="homeSection === section.id"
          @click="homeSection = section.id"
        >
          {{ section.label }}
        </button>
      </nav>
      <p v-if="stageError" class="error" role="alert">
        {{ stageError }}
        <button class="quiet" @click="loadStage">重新核对规划</button>
      </p>
      <section
        v-show="homeSection === 'overview'"
        class="panel stage-panel"
        aria-label="本书当前阶段"
      >
        <p class="eyebrow">
          {{ loadingStage ? "正在核对当前规划…" : "本书当前阶段" }}
        </p>
        <h2>{{ stage.title }}</h2>
        <p>{{ stage.description }}</p>
        <p v-if="overview?.reason" class="muted">{{ overview.reason }}</p>
        <button :disabled="loadingStage" @click="continueStage">
          {{
            overview?.stage === "DETAIL_PLANNING" ? "进入章节写作" : stage.next
          }}
        </button>
        <ol class="workflow-track" aria-label="长篇创作流程">
          <li><strong>确定故事</strong><span>世界、人物与主线</span></li>
          <li><strong>安排全书</strong><span>大纲、分卷与细纲</span></li>
          <li><strong>写作与精修</strong><span>逐章写作或批量连写</span></li>
          <li>
            <strong>审稿与成书</strong><span>定位问题、修改复核、导出</span>
          </li>
        </ol>
      </section>
      <section v-show="homeSection === 'planning'" class="panel task-section">
        <p class="eyebrow">先规划，再写作</p>
        <h2>故事规划</h2>
        <p>
          在同一份规划中完善世界与故事、全书大纲、分卷章节和细纲。确认后的版本继续作为写作依据。
        </p>
        <div class="workspace-actions">
          <button @click="openPlanning">打开本书规划</button>
          <button v-if="canEdit" class="quiet" @click="settings = true">
            修改小说设置与查看历史
          </button>
        </div>
      </section>
      <section v-show="homeSection === 'writing'" class="writing-section">
        <section class="panel task-section">
          <p class="eyebrow">逐章精写，也可批量推进</p>
          <h2>章节写作</h2>
          <p>
            从下面目录进入章节；已有长稿可分章导入，连续写作可查看批次进度与恢复。
          </p>
          <div class="workspace-actions">
            <button @click="batch = true">
              {{ canEdit ? "快速连写 · 最多10章" : "查看连写批次" }}
            </button>
            <button v-if="canEdit" class="quiet" @click="routeIntent('refine')">
              选择章节精修
            </button>
            <button class="quiet" @click="manuscript = true">
              长稿分章导入 / 续导
            </button>
          </div>
        </section>
        <NovelChapterCatalog
          v-if="canEdit"
          :key="`${book.id}:${book.settings_revision}`"
          :book="book"
          :enabled="overview?.stage === 'DETAIL_PLANNING'"
          @chapter="openChapterFrom($event, 'home')"
          @planning="openPlanning"
          @expired="emit('expired')"
        />
      </section>
      <section
        v-if="choosingChapter && homeSection === 'writing'"
        class="panel"
        aria-label="选择要写作的章节"
      >
        <h2>
          {{
            choosingChapter === "refine"
              ? "选择要精修的章节"
              : "选择要写作的章节"
          }}
        </h2>
        <p>请明确目标章节；未确认细纲的章节需先完成规划。</p>
        <p v-if="loadingChapters" role="status">正在读取章节…</p>
        <p v-if="chapterError" role="alert">{{ chapterError }}</p>
        <p v-if="!loadingChapters && !chapterError && !chapterChoices.length">
          还没有可写章节，请先安排章节并确认细纲。
        </p>
        <div class="workspace-actions">
          <button
            v-for="chapter in chapterChoices"
            :key="chapter.id"
            :disabled="!chapter.ready"
            :title="chapter.reason"
            @click="chooseChapter(chapter.id)"
          >
            {{ choosingChapter === "refine" ? "精修" : "写作" }} ·
            {{ chapter.title }}{{ chapter.ready ? "" : ` · ${chapter.reason}` }}
          </button>
        </div>
        <button
          class="quiet"
          @click="
            choosingChapter = null;
            ++chapterRequest;
          "
        >
          收起章节选择
        </button>
        <button
          class="quiet"
          @click="
            choosingChapter = null;
            openPlanning();
          "
        >
          前往章节规划
        </button>
      </section>
      <section v-show="homeSection === 'library'" class="panel task-section">
        <p class="eyebrow">贯穿全书的创作依据</p>
        <h2>人物与资料</h2>
        <h3>角色与世界</h3>
        <div class="workspace-actions">
          <button
            @click="
              focusCharacters = true;
              editing = true;
            "
          >
            本书角色与关系
          </button>
          <button
            class="quiet"
            @click="
              focusCharacters = false;
              editing = true;
            "
          >
            编辑本书世界与角色
          </button>
          <button
            v-if="canEdit"
            class="quiet"
            @click="designingCharacters = true"
          >
            批量角色与一句话完善
          </button>
        </div>
        <p class="muted">
          角色卡包含姓名与关键词搜索、关系图、直接编辑和三视图参考。
        </p>
        <h3>资料与修改联动</h3>
        <div class="workspace-actions">
          <button class="quiet" @click="materials = true">管理本书资料</button>
          <button v-if="canEdit" class="quiet" @click="research = true">
            题材研究与事实卡
          </button>
          <button v-if="canEdit" class="quiet" @click="sourceReview = true">
            资料变更与待复核
          </button>
        </div>
      </section>
      <section v-show="homeSection === 'review'" class="panel task-section">
        <p class="eyebrow">发现问题，定位修改，再复核</p>
        <h2>审稿与导出</h2>
        <div class="workspace-actions">
          <button v-if="canEdit" @click="reviewing = true">
            整卷与全书审稿
          </button>
          <button v-if="canEdit" class="quiet" @click="storyLedger = true">
            全书剧情账本
          </button>
          <button
            class="quiet"
            :disabled="!canEdit"
            @click="proseSearch = true"
          >
            全书正文查找
          </button>
        </div>
        <h3>整理成书</h3>
        <div class="workspace-actions">
          <button v-if="canEdit" class="quiet" @click="images = true">
            封面与场景图
          </button>
          <button v-if="canEdit" class="quiet" @click="exporting = true">
            整书与分卷导出
          </button>
        </div>
        <p class="muted">
          修改仍需保存、核对版本并复核；生成与图片操作仍需单独确认模型和预算。
        </p>
      </section>
      <details v-if="canEdit" class="panel assistant-drawer">
        <summary>不确定从哪里开始？打开任务助手</summary>
        <NovelAuthorAssistant
          :chapter-active="false"
          :tool-active="false"
          :disabled="!canEdit"
          @intent="routeIntent"
        />
      </details>
      <details
        v-show="homeSection === 'overview' || homeSection === 'planning'"
        class="panel"
      >
        <summary>本书故事与创作约定</summary>
        <p>{{ book.setup.idea }}</p>
        <p><strong>主要冲突：</strong>{{ book.setup.conflict }}</p>
        <p><strong>主题：</strong>{{ book.setup.theme || "可继续补充" }}</p>
        <p>
          小说设置已保存。先完善世界观、人物与故事方向，确认规划基础后，再安排全书大纲、分卷章节和细纲。
        </p>
        <p class="muted">
          角色卡支持姓名与关键词搜索、关系图、直接编辑和三视图参考。
          人物采用共享 IP 身份，供后续漫剧复用。
        </p>
        <h2>已保存的创作约定</h2>
        <button v-if="canEdit" @click="settings = true">
          修改小说设置与查看历史
        </button>
        <dl>
          <dt>叙事视角</dt>
          <dd>{{ book.setup.perspective }}</dd>
          <dt>文风与基调</dt>
          <dd>{{ book.setup.style }}</dd>
          <dt>篇幅</dt>
          <dd>
            {{ book.setup.volume_count }}卷，每章{{ book.setup.chapter_min }}—{{
              book.setup.chapter_max
            }}字
          </dd>
          <dt>创建时的人物设置</dt>
          <dd>
            {{
              book.setup.character_mode === "architect"
                ? "创建时选择由助手拟定；人物现状请查看角色库"
                : `创建时手动设置了${book.setup.characters?.length ?? 0}位初始人物；人物现状请查看角色库`
            }}
          </dd>
          <dt>希望避免的内容</dt>
          <dd>{{ book.setup.avoid || "未指定" }}</dd>
        </dl>
        <p>
          配置版本
          {{
            book.settings_revision
          }}；初始人物是候选，不会因创建小说自动成为正史。
        </p>
      </details>
    </section>
  </div>
</template>

<style scoped>
.novel-workspace {
  max-width: 1100px;
  margin: auto;
}
.book-shell {
  max-width: 1200px;
  margin: auto;
  padding: 0 24px 32px;
}
.task-navigation {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding: 12px 0;
  border-bottom: 1px solid #d7ded5;
}
.task-navigation button[aria-pressed="true"] {
  background: #285c4d;
  border-color: #285c4d;
  color: white;
}
.workflow-track {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  list-style: none;
  padding: 22px 0 0;
  margin: 26px 0 0;
  border-top: 1px solid #e1e5da;
  gap: 18px;
}
.workflow-track span {
  display: block;
  margin-top: 8px;
  color: #5d6e63;
  font-size: 13px;
}
.task-section h3 {
  margin-top: 28px;
}
.assistant-drawer {
  padding: 18px 24px;
}
.assistant-drawer :deep(.author-assistant) {
  margin-top: 18px;
}
summary {
  cursor: pointer;
  font-weight: 600;
}
.section-heading {
  display: flex;
  justify-content: space-between;
  gap: 20px;
  align-items: center;
  flex-wrap: wrap;
}
.panel {
  margin-top: 20px;
}
.workspace-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}
dl {
  display: grid;
  grid-template-columns: 130px 1fr;
  gap: 14px;
}
dt {
  font-weight: 600;
}
dd {
  margin: 0;
  white-space: pre-wrap;
}
@media (max-width: 600px) {
  .book-shell {
    padding: 0 16px 24px;
  }
  .workflow-track {
    grid-template-columns: 1fr 1fr;
  }
  .task-navigation button {
    padding: 10px 12px;
  }
  dl {
    grid-template-columns: 1fr;
  }
}
</style>
