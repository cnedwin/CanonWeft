<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, APIError, message, type Actor, type Project } from "../../api";
import type { NovelBook } from "./types";
import type { PlanningOverview } from "./planningTypes";
import { workspaceStage, type AuthorIntent } from "./authorWorkbench";
import NovelAuthorAssistant from "./NovelAuthorAssistant.vue";
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
const exporting = ref(false);
const reviewing = ref(false);
const designingCharacters = ref(false);
const images = ref(false);
const research = ref(false);
const sourceReview = ref(false);
const researchId = ref("");
const book = ref(props.book);
const overview = ref<PlanningOverview | null>(null);
const loadingStage = ref(false);
const stageError = ref("");
const stage = computed(() => workspaceStage(overview.value?.stage));
const choosingChapter = ref<"write" | "refine" | null>(null);
const chapterChoices = ref<
  { id: string; title: string; ready: boolean; reason: string }[]
>([]);
const loadingChapters = ref(false);
const chapterError = ref("");
const chapterEditor = ref<InstanceType<typeof NovelChapterEditor> | null>(null);
const initialChapterAction = ref<"write" | "refine" | undefined>();
const onHome = computed(
  () =>
    !sourceReview.value &&
    !research.value &&
    !images.value &&
    !chapterId.value &&
    !designingCharacters.value &&
    !reviewing.value &&
    !exporting.value &&
    !batch.value &&
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
  if (intent === "planning") planning.value = true;
  else if (intent === "characters") {
    focusCharacters.value = true;
    editing.value = true;
  } else if (intent === "materials") materials.value = true;
  else if (intent === "batch") batch.value = true;
  else {
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
  chapterId.value = id;
  choosingChapter.value = null;
  ++chapterRequest;
}
function closeChapter() {
  chapterId.value = "";
  initialChapterAction.value = undefined;
  reviewing.value = false;
  exporting.value = false;
  batch.value = false;
  // The chapter's close action promises to return to its planning context.
  planning.value = true;
  void loadStage();
}
onMounted(loadStage);
watch(onHome, (home, previous) => {
  if (home && !previous) void loadStage();
});
onBeforeUnmount(() => {
  disposed = true;
  ++chapterRequest;
});
</script>

<template>
  <div class="book-shell">
    <NovelAuthorAssistant
      :chapter-active="!!chapterId"
      :tool-active="!onHome && !chapterId"
      :disabled="!canEdit || (!onHome && !chapterId)"
      @intent="routeIntent"
    />
    <NovelSourceReview
      v-if="sourceReview"
      :book-id="book.id"
      :title="book.setup.title"
      @close="sourceReview = false"
      @research="
        researchId = $event;
        sourceReview = false;
        research = true;
      "
      @chapter="
        chapterId = $event;
        sourceReview = false;
      "
      @planning="
        sourceReview = false;
        planning = true;
      "
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
      :initial-action="initialChapterAction"
      ref="chapterEditor"
      @close="closeChapter"
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
      @close="reviewing = false"
      @edit="chapterId = $event"
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
      @edit="chapterId = $event"
      @expired="emit('expired')"
    />
    <NovelBatchDesk
      v-else-if="batch"
      :book="book"
      :project="project"
      :actor="actor"
      @close="batch = false"
      @edit="chapterId = $event"
      @expired="emit('expired')"
    />
    <NovelPlanning
      v-else-if="planning"
      :book="book"
      :project="project"
      :actor="actor"
      @close="planning = false"
      @write="chapterId = $event"
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
    <NovelSettingsEditor
      v-else-if="settings"
      :book="book"
      @close="settings = false"
      @updated="updated"
      @expired="emit('expired')"
    />
    <IPPage
      v-else-if="editing"
      :project="project"
      :actor="actor"
      :initial-branch-id="book.branch_id"
      :focus-characters="focusCharacters"
      @close="editing = false"
      @expired="emit('expired')"
    />
    <section v-else class="novel-workspace">
      <header class="section-heading">
        <div>
          <p class="eyebrow">小说工作台</p>
          <h1>{{ book.setup.title }}</h1>
          <p>
            {{ book.setup.genre }} · {{ book.setup.language }} ·
            {{ book.setup.target_words.toLocaleString() }}字计划
          </p>
        </div>
        <button @click="emit('close')">返回书架</button>
      </header>
      <section class="panel stage-panel" aria-label="本书当前阶段">
        <p class="eyebrow">
          {{ loadingStage ? "正在核对当前规划…" : "本书当前阶段" }}
        </p>
        <h2>{{ stage.title }}</h2>
        <p>{{ stage.description }}</p>
        <p v-if="overview?.reason" class="muted">{{ overview.reason }}</p>
        <p v-if="stageError" role="alert">
          {{ stageError }}
          <button class="quiet" @click="loadStage">重新核对</button>
        </p>
        <button :disabled="loadingStage" @click="planning = true">
          {{ stage.next }}
        </button>
      </section>
      <section
        v-if="choosingChapter"
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
            planning = true;
          "
        >
          前往章节规划
        </button>
      </section>
      <section class="panel">
        <h2>常用动作</h2>
        <div class="workspace-actions">
          <button @click="planning = true">规划世界、大纲与细纲</button>
          <button @click="batch = true">快速连写 · 最多10章</button>
          <button @click="materials = true">管理本书资料</button>
        </div>
        <details class="tools-drawer">
          <summary>全部创作工具</summary>
          <h3>研究与资料</h3>
          <div class="workspace-actions">
            <button v-if="canEdit" @click="research = true">
              题材研究与事实卡
            </button>
            <button v-if="canEdit" @click="materials = true">
              资料采集与素材入库
            </button>
            <button v-if="canEdit" @click="sourceReview = true">
              资料变更与待复核
            </button>
          </div>
          <h3>人物与世界</h3>
          <div class="workspace-actions">
            <button v-if="canEdit" @click="designingCharacters = true">
              批量角色与一句话完善
            </button>
            <button
              @click="
                focusCharacters = true;
                editing = true;
              "
            >
              本书角色与关系
            </button>
            <button
              @click="
                focusCharacters = false;
                editing = true;
              "
            >
              编辑本书世界与角色
            </button>
          </div>
          <h3>审稿与成书</h3>
          <div class="workspace-actions">
            <button v-if="canEdit" @click="reviewing = true">
              整卷与全书审稿
            </button>
            <button v-if="canEdit" @click="images = true">封面与场景图</button>
            <button v-if="canEdit" @click="exporting = true">
              整书与分卷导出
            </button>
          </div>
          <p class="muted">
            角色卡保留姓名与关键词搜索、关系图、直接编辑和三视图参考。
          </p>
        </details>
      </section>
      <details class="panel">
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
}
.tools-drawer {
  margin-top: 1.5rem;
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
  dl {
    grid-template-columns: 1fr;
  }
}
</style>
