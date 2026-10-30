<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  api,
  APIError,
  message,
  newOperationID,
  type Project,
} from "../../api";
import type {
  NovelBook,
  NovelDraft,
  NovelSetup,
  NovelSetupPreset,
  NovelSetupPresetPreview,
} from "./types";
import NovelSetupAssistant from "./NovelSetupAssistant.vue";

const props = defineProps<{ projects: Project[] }>();
const emit = defineEmits<{
  close: [];
  created: [book: NovelBook];
  expired: [];
}>();
const drafts = ref<NovelDraft[]>([]);
const draft = ref<NovelDraft | null>(null);
const defaults = ref<NovelSetup | null>(null);
const busy = ref(false);
const confirming = ref(false);
const importing = ref(false);
const assisting = ref(false);
const assistantBase = ref<{ id: string; revision: number; content: string }>();
async function openAssistant() {
  if (actionBlocked.value) return;
  if (!draft.value) await fresh();
  const current = draft.value as NovelDraft | null;
  if (!current) return;
  const reviewedID = current.id;
  const reviewedContent = content.value;
  if (!(await save()) || !alive) return;
  if (
    draft.value?.id !== reviewedID ||
    content.value !== reviewedContent ||
    dirty.value
  ) {
    notice.value =
      "你在保存期间继续修改了内容，已保留当前文字。请保存后重试进入助手。";
    if (dirty.value) scheduleSave();
    return;
  }
  assistantBase.value = {
    id: reviewedID,
    revision: draft.value.revision,
    content: reviewedContent,
  };
  assisting.value = true;
}
function adoptAssistant(value: NovelDraft) {
  const base = assistantBase.value;
  const current = draft.value;
  if (!assisting.value) return;
  if (
    !base ||
    !current ||
    current.id !== base.id ||
    current.revision !== base.revision ||
    content.value !== base.content ||
    dirty.value ||
    value.id !== base.id ||
    value.revision <= base.revision
  ) {
    assisting.value = false;
    assistantBase.value = undefined;
    if (current?.id === value.id && value.revision > current.revision)
      conflict.value = true;
    notice.value =
      "本地草稿在助手处理期间发生了变化，已保留当前文字，助手回执没有覆盖它。请核对或另存恢复草稿后重试。";
    if (dirty.value && !conflict.value) scheduleSave();
    return;
  }
  draft.value = value;
  lastSaved.value = JSON.stringify({ step: value.step, setup: value.setup });
  assisting.value = false;
  assistantBase.value = undefined;
  fields.value = {};
  presetOnly.value = false;
  notice.value = "所选建议已保存到私人建书草稿，请核对下方完整方案。";
}
const error = ref("");
const notice = ref("");
const fields = ref<Record<string, string>>({});
const consent = ref(false);
const operation = ref(newOperationID());
const lastSaved = ref("");
const branches = ref<{ id: string; name: string; head: string }[]>([]);
const connections = ref<{ id: string; name: string; model: string }[]>([]);
const presets = ref<NovelSetupPreset[]>([]);
const presetGroups = computed(() => {
  const male = new Set([
    "urban-career",
    "fantasy-growth",
    "mystery-rules",
    "historical-strategy",
  ]);
  const female = new Set([
    "modern-romance",
    "era-career",
    "ancient-intrigue",
    "heroine-fantasy",
  ]);
  return [
    {
      name: "男频常见方向",
      items: presets.value.filter((preset) => male.has(preset.id)),
    },
    {
      name: "女频常见方向",
      items: presets.value.filter((preset) => female.has(preset.id)),
    },
    {
      name: "其他方向",
      items: presets.value.filter(
        (preset) => !male.has(preset.id) && !female.has(preset.id),
      ),
    },
  ].filter((group) => group.items.length);
});
const catalogError = ref("");
const catalogBusy = ref(false);
const selectedPreset = ref("");
const initialIdea = ref("");
const previewing = ref(false);
const pendingPreset = ref<NovelSetupPresetPreview>();
const pendingContent = ref("");
const conflict = ref(false);
const presetOnly = ref(false);
const quickIdea = computed({
  get: () => draft.value?.setup.idea ?? initialIdea.value,
  set: (value: string) => {
    if (draft.value) draft.value.setup.idea = value;
    else initialIdea.value = value;
  },
});
const storyFields: (keyof NovelSetup)[] = [
  "title",
  "genre",
  "idea",
  "conflict",
  "audience",
  "world",
  "theme",
  "include",
  "avoid",
  "style",
  "target_words",
  "volume_count",
  "chapter_min",
  "chapter_max",
  "character_mode",
  "characters",
];
let timer: ReturnType<typeof setTimeout> | undefined;
let alive = true;
const content = computed(() =>
  draft.value
    ? JSON.stringify({ step: draft.value.step, setup: draft.value.setup })
    : "",
);
const dirty = computed(() => content.value !== lastSaved.value);
const actionBlocked = computed(
  () =>
    busy.value ||
    confirming.value ||
    importing.value ||
    assisting.value ||
    previewing.value,
);
function guardUnload(event: BeforeUnloadEvent) {
  if (
    draft.value
      ? !dirty.value && !actionBlocked.value
      : !initialIdea.value.trim()
  )
    return;
  event.preventDefault();
  event.returnValue = "";
}
function failed(cause: unknown) {
  error.value = message(cause);
  if (cause instanceof APIError) {
    fields.value = cause.fields;
    if (cause.status === 409) conflict.value = true;
    if (cause.status === 401) emit("expired");
  }
}
function scheduleSave() {
  clearTimeout(timer);
  timer = setTimeout(() => {
    if (alive && !actionBlocked.value && dirty.value) void save();
  }, 1000);
}
watch(content, () => {
  consent.value = false;
  if (draft.value) scheduleSave();
});
async function save() {
  if (!draft.value || busy.value) return false;
  if (!dirty.value && draft.value.revision) return true;
  busy.value = true;
  error.value = "";
  const sent = JSON.parse(JSON.stringify(draft.value)) as NovelDraft;
  const sentContent = content.value;
  try {
    const result = await api<NovelDraft>(
      `/novel-setups/${sent.id}`,
      "PUT",
      sent,
    );
    if (alive && draft.value?.id === sent.id) {
      draft.value.revision = result.revision;
      lastSaved.value = sentContent;
      conflict.value = false;
      notice.value = "设置草稿已保存";
    }
    return alive && draft.value?.id === sent.id;
  } catch (cause) {
    failed(cause);
    return false;
  } finally {
    busy.value = false;
    if (alive && content.value !== sentContent && dirty.value) scheduleSave();
  }
}
async function fresh() {
  if (draft.value && !(await save())) return;
  if (!defaults.value) return;
  draft.value = {
    id: crypto.randomUUID().replaceAll("-", ""),
    revision: 0,
    step: 1,
    setup: JSON.parse(JSON.stringify(defaults.value)) as NovelSetup,
    confirmed_novel_id: "",
  };
  draft.value.setup.idea = initialIdea.value;
  draft.value.setup.creation_mode = "assisted";
  draft.value.setup.model_choice = "later";
  draft.value.setup.character_mode ||= "architect";
  lastSaved.value = "";
  fields.value = {};
  error.value = "";
  operation.value = newOperationID();
  await save();
}
async function loadPresets() {
  catalogBusy.value = true;
  catalogError.value = "";
  try {
    const result = await api<{ items: NovelSetupPreset[] }>(
      "/novel-setups/presets",
    );
    if (!alive) return;
    presets.value = result.items;
    if (!result.items.length)
      catalogError.value = "预设目录暂时为空，可以自由填写或稍后重试。";
  } catch (cause) {
    if (alive) catalogError.value = message(cause);
  } finally {
    if (alive) catalogBusy.value = false;
  }
}
async function previewPreset() {
  if (actionBlocked.value || !selectedPreset.value || !defaults.value) return;
  if (draft.value && !(await save())) return;
  const snapshot = content.value;
  const idea = quickIdea.value;
  const presetID = selectedPreset.value;
  previewing.value = true;
  pendingPreset.value = undefined;
  error.value = "";
  try {
    const result = await api<NovelSetupPresetPreview>(
      "/novel-setups/preset-preview",
      "POST",
      { preset_id: presetID, idea },
    );
    if (!alive) return;
    if (
      snapshot !== content.value ||
      idea !== quickIdea.value ||
      presetID !== selectedPreset.value
    ) {
      notice.value = "你在等待期间修改了内容，已保留当前文字；请重新获取方案。";
      return;
    }
    pendingPreset.value = result;
    pendingContent.value = snapshot;
    if (!draft.value) {
      await fresh();
      const createdDraft = draft.value as NovelDraft | null;
      if (!createdDraft || dirty.value) return;
      createdDraft.setup.language = result.setup.language;
      createdDraft.setup.perspective = result.setup.perspective;
      pendingContent.value = content.value;
      await applyPreset(true);
    }
  } catch (cause) {
    if (alive) failed(cause);
  } finally {
    if (alive) {
      previewing.value = false;
      if (dirty.value) scheduleSave();
    }
  }
}
async function applyPreset(replace: boolean) {
  const proposal = pendingPreset.value;
  const value = draft.value;
  if (!proposal || !value) return;
  if (pendingContent.value !== content.value) {
    pendingPreset.value = undefined;
    notice.value = "方案展示后你又修改了内容，已保留手改；请重新获取方案。";
    return;
  }
  // Presets never replace source manuscripts, IP bindings, model bindings or protection.
  const updated = { ...value.setup } as unknown as Record<string, unknown>;
  for (const field of storyFields) {
    const current = updated[field];
    if (
      replace ||
      current === "" ||
      current == null ||
      (Array.isArray(current) && !current.length)
    ) {
      updated[field] = JSON.parse(JSON.stringify(proposal.setup[field] ?? ""));
    }
  }
  if ((updated.characters as unknown[]).length)
    updated.character_mode = "manual";
  updated.preset_id = proposal.preset_id;
  updated.preset_revision = proposal.preset_revision;
  value.setup = updated as unknown as NovelSetup;
  presetOnly.value = true;
  pendingPreset.value = undefined;
  notice.value = "预设草案已填入私人草稿。未调用AI，请逐项编辑后确认。";
  await save();
}
async function recoverCopy() {
  if (!draft.value || actionBlocked.value) return;
  draft.value = {
    ...draft.value,
    id: crypto.randomUUID().replaceAll("-", ""),
    revision: 0,
    confirmed_novel_id: "",
  };
  lastSaved.value = "";
  conflict.value = false;
  operation.value = newOperationID();
  await save();
}
async function resume(item: NovelDraft) {
  if (actionBlocked.value) return;
  if (!draft.value && initialIdea.value.trim()) {
    await fresh();
    if (!draft.value) return;
  }
  if (draft.value && !(await save())) return;
  busy.value = true;
  try {
    const loaded = await api<NovelDraft>(`/novel-setups/${item.id}`);
    if (!alive) return;
    loaded.setup.characters ??= [];
    draft.value = loaded;
    lastSaved.value = JSON.stringify({
      step: loaded.step,
      setup: loaded.setup,
    });
    operation.value = newOperationID();
    pendingPreset.value = undefined;
    conflict.value = false;
    fields.value = {};
    error.value = "";
    await loadBranches(false);
  } catch (cause) {
    if (alive) failed(cause);
  } finally {
    busy.value = false;
  }
}
async function close() {
  if (actionBlocked.value) return;
  if (!draft.value && initialIdea.value.trim()) {
    await fresh();
    if (!draft.value) return;
  }
  if (!draft.value || ((await save()) && !dirty.value)) emit("close");
}
function changeIP() {
  if (!draft.value) return;
  Object.assign(draft.value.setup, {
    project_id: "",
    branch_id: "",
    base_head: "",
  });
  branches.value = [];
}
async function loadBranches(reset = true) {
  const value = draft.value;
  if (!value) return;
  if (reset) {
    value.setup.branch_id = "";
    value.setup.base_head = "";
  }
  const project = value.setup.project_id;
  if (!project) {
    branches.value = [];
    return;
  }
  try {
    const result = await api<{
      items: { id: string; name: string; head: string }[];
    }>(`/projects/${project}/branches`);
    if (
      draft.value?.id === value.id &&
      draft.value.setup.project_id === project
    )
      branches.value = result.items.filter((b) => b.head);
  } catch (cause) {
    failed(cause);
  }
}
function chooseBranch() {
  if (draft.value)
    draft.value.setup.base_head =
      branches.value.find((b) => b.id === draft.value?.setup.branch_id)?.head ??
      "";
}
async function importText(event: Event) {
  const file = (event.target as HTMLInputElement).files?.[0];
  if (!file || !draft.value) return;
  if (actionBlocked.value) return;
  const target = draft.value;
  importing.value = true;
  try {
    if (file.size > 800000)
      throw new Error(
        "此处支持80万字节以内的UTF-8文本；超出时请保留原稿，使用后续长稿导入流程。",
      );
    const text = new TextDecoder("utf-8", { fatal: true }).decode(
      await file.arrayBuffer(),
    );
    if ([...text].length > 200000)
      throw new Error("此处最多接收20万字原稿，文件没有被截断或覆盖。");
    if (
      !alive ||
      draft.value?.id !== target.id ||
      draft.value.setup.source_mode !== "import"
    )
      return;
    draft.value.setup.source_text = text;
    notice.value = `已读取 ${file.name} 的完整文本；保存草稿后保留。`;
  } catch (cause) {
    failed(cause);
  } finally {
    importing.value = false;
    (event.target as HTMLInputElement).value = "";
    if (alive && dirty.value) scheduleSave();
  }
}
async function confirm() {
  if (!draft.value || actionBlocked.value || !consent.value) return;
  const reviewedID = draft.value.id;
  const reviewedContent = content.value;
  confirming.value = true;
  clearTimeout(timer);
  try {
    if (!(await save())) return;
    // A confirmation authorizes only the exact settings the author reviewed.
    if (
      !alive ||
      draft.value?.id !== reviewedID ||
      content.value !== reviewedContent ||
      !consent.value
    )
      return;
    error.value = "";
    fields.value = {};
    const book = await api<NovelBook>(
      `/novel-setups/${reviewedID}/confirm`,
      "POST",
      {
        revision: draft.value.revision,
        operation_id: operation.value,
        accept_settings: true,
      },
    );
    if (!alive) return;
    clearTimeout(timer);
    lastSaved.value = reviewedContent;
    emit("created", book);
  } catch (cause) {
    if (alive) failed(cause);
  } finally {
    confirming.value = false;
  }
}
onMounted(async () => {
  window.addEventListener("beforeunload", guardUnload);
  busy.value = true;
  try {
    const [base, saved] = await Promise.all([
      api<NovelSetup>("/novel-setups/defaults"),
      api<{ items: NovelDraft[] }>("/novel-setups"),
    ]);
    defaults.value = base;
    drafts.value = saved.items;
    void api<{ items: typeof connections.value }>("/novel-setups/model-choices")
      .then((models) => {
        if (alive) connections.value = models.items;
      })
      .catch(() => {
        if (alive) notice.value = "暂未取得模型连接；仍可免费建书，稍后配置。";
      });
  } catch (cause) {
    failed(cause);
  } finally {
    busy.value = false;
  }
  void loadPresets();
});
onBeforeUnmount(() => {
  alive = false;
  clearTimeout(timer);
  window.removeEventListener("beforeunload", guardUnload);
});
</script>

<template>
  <section class="novel-entry">
    <header class="section-heading">
      <div>
        <h1>新建小说</h1>
        <p>选一个创作起点，核对方案后开始写书。</p>
      </div>
      <button class="quiet" :disabled="actionBlocked" @click="close">
        {{ draft ? "保存并返回书架" : "返回书架" }}
      </button>
    </header>
    <ol class="opening-phases" aria-label="建书流程">
      <li :aria-current="!draft ? 'step' : undefined">
        <span>1</span> 选择创作方向
      </li>
      <li :aria-current="draft ? 'step' : undefined">
        <span>2</span> 核对开书方案
      </li>
    </ol>
    <p v-if="error" role="alert" class="error">{{ error }}</p>
    <p v-if="notice" role="status" class="notice">{{ notice }}</p>
    <button v-if="conflict" :disabled="actionBlocked" @click="recoverCopy">
      保留当前文字，另存恢复草稿
    </button>
    <section v-if="!draft && drafts.length" class="panel resume-drafts">
      <div class="resume-heading">
        <h2>继续上次的开书草稿</h2>
        <p class="muted">{{ drafts.length }} 份待确认</p>
      </div>
      <p v-if="drafts.length === 100" class="muted">
        显示最近100份未确认草稿。
      </p>
      <ul>
        <li v-for="item in drafts" :key="item.id">
          <button :disabled="actionBlocked" @click="resume(item)">
            <strong>{{ item.setup.title || "未命名小说" }}</strong>
            <span>继续编辑 →</span>
          </button>
        </li>
      </ul>
    </section>
    <section
      v-if="!assisting"
      class="panel quick-opening"
      aria-label="一句话开书"
    >
      <h2>{{ draft ? "需要调整创作起点？" : "选个方向，写一句想法" }}</h2>
      <p>免费预设提供一份可编辑的完整起点；也可以从空白方案开始。</p>
      <details :open="!draft" class="direction-picker">
        <summary>{{ draft ? "调整预设方向与想法" : "选择免费预设" }}</summary>
        <label
          >一句话创作想法<textarea
            v-model="quickIdea"
            :disabled="confirming || importing"
            rows="3"
            maxlength="16000"
            placeholder="例如：一个修钟师发现全城的人都忘记了他的朋友。"
          />
        </label>
        <p class="muted">男频、女频是读者取向，任何作者都可以选择。</p>
        <p v-if="catalogBusy">正在加载预设…</p>
        <section v-for="group in presetGroups" :key="group.name">
          <h3>{{ group.name }}</h3>
          <div class="preset-grid">
            <article
              v-for="preset in group.items"
              :key="preset.id"
              class="preset-card"
              :class="{ selected: selectedPreset === preset.id }"
            >
              <button
                type="button"
                :aria-label="`选择预设：${preset.name}`"
                :title="preset.audience"
                :aria-pressed="selectedPreset === preset.id"
                :disabled="actionBlocked"
                @click="selectedPreset = preset.id"
              >
                <strong>{{ preset.name }}</strong
                ><span>{{ preset.audience }}</span>
              </button>
              <details class="preset-details">
                <summary>查看方向与示例</summary>
                <p>{{ preset.description }}</p>
                <p class="muted">{{ preset.reader_promise }}</p>
                <p class="preset-example">{{ preset.example_idea }}</p>
                <button
                  type="button"
                  :disabled="actionBlocked || !!quickIdea.trim()"
                  @click="
                    selectedPreset = preset.id;
                    quickIdea = preset.example_idea;
                  "
                >
                  使用这个示例
                </button>
              </details>
            </article>
          </div>
        </section>
        <p v-if="quickIdea.trim()" class="muted">
          已保留你填写的想法；清空想法后可以使用卡片示例。
        </p>
        <p v-if="catalogError" class="error">{{ catalogError }}</p>
        <button
          v-if="catalogError"
          :disabled="catalogBusy"
          @click="loadPresets"
        >
          重试加载预设
        </button>
        <div class="opening-actions">
          <button
            type="button"
            :disabled="actionBlocked || !selectedPreset || !defaults"
            @click="previewPreset"
          >
            {{ previewing ? "正在准备免费方案…" : "获取免费预设方案" }}
          </button>
          <button
            v-if="!draft"
            class="quiet"
            type="button"
            :disabled="actionBlocked || !defaults"
            @click="fresh"
          >
            自由填写方案
          </button>
        </div>
        <p class="muted">
          预设只把你的想法保留到对应题材的静态草案，不理解或改写这句话。
        </p>
      </details>
      <button
        type="button"
        class="quiet"
        :disabled="actionBlocked || !defaults"
        @click="openAssistant"
      >
        用想法或附件让AI完善
      </button>
      <p class="muted assistant-note">AI辅助会先展示模型、价格与授权。</p>
    </section>
    <section
      v-if="pendingPreset"
      class="panel preset-review"
      aria-label="待采用的预设草案"
    >
      <h2>预设草案，未调用AI</h2>
      <p>{{ pendingPreset.reader_promise }}</p>
      <p v-for="(note, index) in pendingPreset.notes" :key="index">
        {{ note }}
      </p>
      <p>
        替换范围：书名、题材、想法、冲突、读者、世界、主题、期待与避讳、文风、篇幅、角色。原稿、已有IP、语言视角、模型和保护设置保持当前值。只补空白会保留所有已有内容。
      </p>
      <details>
        <summary>查看即将填入的故事方案</summary>
        <p>{{ pendingPreset.setup.title }} · {{ pendingPreset.setup.genre }}</p>
        <p>{{ pendingPreset.setup.idea }}</p>
        <p>{{ pendingPreset.setup.world }}</p>
        <p>{{ pendingPreset.setup.conflict }}</p>
        <p>{{ pendingPreset.setup.theme }}</p>
        <p>{{ pendingPreset.setup.include }}</p>
        <p>
          {{ pendingPreset.setup.style }} ·
          {{ pendingPreset.setup.target_words }} 字 /
          {{ pendingPreset.setup.volume_count }} 卷
        </p>
        <ul>
          <li
            v-for="(person, index) in pendingPreset.setup.characters"
            :key="index"
          >
            {{ person.name }}：{{ person.identity }}；{{ person.goal }}；{{
              person.background
            }}；{{ person.personality }}
          </li>
        </ul>
      </details>
      <button
        type="button"
        :disabled="actionBlocked"
        @click="applyPreset(false)"
      >
        保留当前内容，只补充空白
      </button>
      <button
        type="button"
        :disabled="actionBlocked"
        @click="applyPreset(true)"
      >
        替换故事方案，保留高级设置
      </button>
      <button
        type="button"
        :disabled="actionBlocked"
        @click="pendingPreset = undefined"
      >
        取消换方向
      </button>
    </section>
    <NovelSetupAssistant
      v-if="assisting && draft"
      :key="draft.id"
      :draft="draft"
      @close="assisting = false"
      @adopted="adoptAssistant"
    />
    <template v-else-if="draft">
      <h2>核对并编辑开书方案</h2>
      <p v-if="presetOnly" class="preset-mark">预设草案，未调用AI</p>
      <p v-else-if="draft.setup.preset_id" class="muted">
        此草稿使用了预设起点，后续修改以当前内容为准。
      </p>
      <p class="muted">
        私人建书草稿 ·
        {{ busy ? "正在保存…" : dirty ? "有未保存修改" : "草稿已保存" }}
      </p>
      <ul
        v-if="Object.keys(fields).length"
        class="error"
        aria-label="需要修正的设置"
      >
        <li v-for="(detail, key) in fields" :key="key">
          {{ key }}：{{ detail }}
        </li>
      </ul>
      <form
        class="panel wizard-form"
        @invalid.capture="
          ($event.currentTarget as HTMLFormElement)
            .querySelectorAll('details')
            .forEach((group) => (group.open = true))
        "
        @submit.prevent="confirm"
      >
        <fieldset :disabled="confirming || importing">
          <legend>作品信息</legend>
          <label
            >书名<input v-model="draft.setup.title" maxlength="120"
          /></label>
          <label
            >题材<input
              v-model="draft.setup.genre"
              list="novel-genres"
              maxlength="120"
              placeholder="可以自由填写或组合题材"
          /></label>
          <datalist id="novel-genres">
            <option>玄幻</option>
            <option>仙侠</option>
            <option>都市</option>
            <option>悬疑</option>
            <option>历史</option>
            <option>科幻</option>
            <option>言情</option>
            <option>奇幻</option>
          </datalist>
          <details class="advanced" :open="Object.keys(fields).length > 0">
            <summary>
              原稿、已有IP与语言
              <span class="disclosure-description">
                {{ draft.setup.language || "语言待定" }} ·
                {{
                  draft.setup.source_mode === "import" ? "导入原稿" : "从零创作"
                }}
                ·
                {{
                  draft.setup.ip_mode === "existing" ? "沿用已有IP" : "新的IP"
                }}
              </span>
            </summary>
            <label
              >语言<input v-model="draft.setup.language" maxlength="80"
            /></label>
            <label
              >稿件来源<select v-model="draft.setup.source_mode">
                <option value="fresh">从零创作</option>
                <option value="import">导入原稿续写</option>
              </select></label
            >
            <template v-if="draft.setup.source_mode === 'import'"
              ><label
                >读取UTF-8文本文件<input
                  type="file"
                  accept=".txt,text/plain"
                  @change="importText" /></label
              ><label
                >已有原稿<textarea
                  v-model="draft.setup.source_text"
                  rows="8"
                  placeholder="原文会保留，不会在导入时自动改写"
                /></label
            ></template>
            <label
              >世界与IP来源<select
                v-model="draft.setup.ip_mode"
                @change="changeIP"
              >
                <option value="new">建立新的IP</option>
                <option value="existing">沿用已有IP的已发布版本</option>
              </select></label
            >
            <template v-if="draft.setup.ip_mode === 'existing'"
              ><label
                >已有IP<select
                  v-model="draft.setup.project_id"
                  @change="loadBranches()"
                >
                  <option value="">请选择</option>
                  <option
                    v-for="project in props.projects"
                    :key="project.id"
                    :value="project.id"
                  >
                    {{ project.name }}
                  </option>
                </select></label
              ><label
                >来源分支<select
                  v-model="draft.setup.branch_id"
                  @change="chooseBranch"
                >
                  <option value="">请选择已发布分支</option>
                  <option
                    v-for="branch in branches"
                    :key="branch.id"
                    :value="branch.id"
                  >
                    {{ branch.name }}
                  </option>
                </select></label
              >
              <p>
                采用版本：{{
                  draft.setup.base_head || "尚未选择"
                }}。小说使用独立分支，原作设定保持原版本。
              </p></template
            >
          </details>
        </fieldset>
        <fieldset :disabled="confirming || importing">
          <legend>故事方向</legend>
          <label
            >核心梗概或创作想法<textarea v-model="draft.setup.idea" rows="4" />
          </label>
          <label
            >主线目标与主要冲突<textarea
              v-model="draft.setup.conflict"
              rows="3"
            />
          </label>
          <label>目标读者<input v-model="draft.setup.audience" /></label>
          <label
            >世界背景<textarea v-model="draft.setup.world" rows="3" />
          </label>
          <label
            >主题与中心思想<textarea v-model="draft.setup.theme" rows="2" />
          </label>
          <label
            >读者期待与希望出现的内容<textarea
              v-model="draft.setup.include"
              rows="2"
            />
          </label>
          <label
            >希望避免的内容<textarea v-model="draft.setup.avoid" rows="2" />
          </label>
        </fieldset>
        <details class="form-disclosure" :open="Object.keys(fields).length > 0">
          <summary>
            <strong>叙事与篇幅</strong>
            <span class="disclosure-description">
              {{ draft.setup.perspective || "视角待定" }} ·
              {{ draft.setup.target_words.toLocaleString() }} 字 ·
              {{ draft.setup.volume_count }} 卷
            </span>
          </summary>
          <fieldset :disabled="confirming || importing">
            <legend class="visually-hidden">叙事与篇幅</legend>
            <details class="advanced" :open="Object.keys(fields).length > 0">
              <summary>高级设置：叙事视角</summary>
              <label
                >叙事视角<input
                  v-model="draft.setup.perspective"
                  list="novel-perspectives" /></label
              ><datalist id="novel-perspectives">
                <option>第三人称限知</option>
                <option>第一人称</option>
                <option>第三人称全知</option>
                <option>多视角交替</option>
              </datalist>
            </details>
            <label
              >文风与基调<textarea v-model="draft.setup.style" rows="3" />
            </label>
            <div class="numbers">
              <label
                >目标总字数<input
                  v-model.number="draft.setup.target_words"
                  type="number"
                  min="1"
                  max="100000000"
                  step="1" /></label
              ><label
                >计划卷数<input
                  v-model.number="draft.setup.volume_count"
                  type="number"
                  min="1"
                  max="10000"
                  step="1" /></label
              ><label
                >每章最少字数<input
                  v-model.number="draft.setup.chapter_min"
                  type="number"
                  min="1"
                  max="100000"
                  step="1" /></label
              ><label
                >每章最多字数<input
                  v-model.number="draft.setup.chapter_max"
                  type="number"
                  min="1"
                  max="100000"
                  step="1"
              /></label>
            </div>
            <p>篇幅是规划目标，后续可以调整，不会为了凑字数自动扩写。</p>
          </fieldset>
        </details>
        <details class="form-disclosure" :open="Object.keys(fields).length > 0">
          <summary>
            <strong>初始角色</strong>
            <span class="disclosure-description">
              {{
                draft.setup.character_mode === "manual"
                  ? `已设置 ${draft.setup.characters.length} 位主角`
                  : draft.setup.character_mode === "architect"
                    ? "稍后根据故事方向拟定，采用前由你审阅"
                    : "待选择主角设置方式"
              }}
            </span>
          </summary>
          <fieldset :disabled="confirming || importing">
            <legend class="visually-hidden">初始角色</legend>
            <label
              >主角设置方式<select v-model="draft.setup.character_mode">
                <option value="">请选择</option>
                <option value="manual">我来设置主角</option>
                <option
                  value="architect"
                  :disabled="draft.setup.characters.length > 0"
                >
                  根据故事方向由Agent拟定
                </option>
              </select></label
            >
            <p v-if="draft.setup.character_mode === 'architect'">
              保存待拟定意图。创建小说时不会调用模型；角色候选仍需你审阅采用。
            </p>
            <template v-if="draft.setup.character_mode === 'manual'"
              ><article
                v-for="(character, index) in draft.setup.characters"
                :key="index"
                class="initial-character"
              >
                <h3>主角 {{ index + 1 }}</h3>
                <label
                  >姓名或暂名<input
                    v-model="character.name"
                    maxlength="300" /></label
                ><label>身份与职业<input v-model="character.identity" /></label
                ><label
                  >核心目标<textarea v-model="character.goal" rows="2" /></label
                ><label
                  >人物背景<textarea
                    v-model="character.background"
                    rows="2"
                  /></label
                ><label
                  >性格<textarea
                    v-model="character.personality"
                    rows="2"
                  /></label
                ><button
                  type="button"
                  @click="draft.setup.characters.splice(index, 1)"
                >
                  移除此人物草稿
                </button>
              </article>
              <button
                type="button"
                :disabled="draft.setup.characters.length >= 100"
                @click="
                  draft.setup.characters.push({
                    name: '',
                    identity: '',
                    goal: '',
                    background: '',
                    personality: '',
                  })
                "
              >
                添加一位主角
              </button>
              <p>
                同名人物保持独立身份。配角和关系可在角色库继续添加。
              </p></template
            >
          </fieldset>
        </details>
        <fieldset :disabled="confirming || importing">
          <legend>确认开书</legend>
          <details class="advanced" :open="Object.keys(fields).length > 0">
            <summary>高级设置：模型用途与核心保护</summary>
            <p>
              可先用AI辅助模式建书并稍后配置。已有
              {{ connections.length }} 个模型连接可选；建书不会调用模型。
            </p>
            <label
              >创作方式<select
                v-model="draft.setup.creation_mode"
                @change="
                  draft.setup.model_choice = '';
                  draft.setup.connection_id = '';
                "
              >
                <option value="">请选择</option>
                <option value="manual">以人工创作为主</option>
                <option value="assisted">AI辅助创作</option>
              </select></label
            >
            <template v-if="draft.setup.creation_mode === 'assisted'"
              ><label
                >模型准备<select
                  v-model="draft.setup.model_choice"
                  @change="draft.setup.connection_id = ''"
                >
                  <option value="">请选择</option>
                  <option value="later">稍后配置模型</option>
                  <option value="configured">使用已配置模型</option>
                </select></label
              ><label v-if="draft.setup.model_choice === 'configured'"
                >写作模型<select v-model="draft.setup.connection_id">
                  <option value="">请选择</option>
                  <option
                    v-for="connection in connections"
                    :key="connection.id"
                    :value="connection.id"
                  >
                    {{ connection.name }} · {{ connection.model }}
                  </option>
                </select></label
              ></template
            >
            <label
              >核心设定保护<select v-model="draft.setup.protection">
                <option value="core_identity_life_world">
                  保护已确认的核心身份、生死与世界上限
                </option>
                <option value="review_all_changes">
                  所有设定修改都先审查与确认
                </option>
              </select></label
            >
          </details>
          <div class="summary">
            <strong>{{ draft.setup.title || "尚未填写书名" }}</strong>
            <p>
              {{ draft.setup.genre || "尚未选择题材" }} ·
              {{ draft.setup.target_words.toLocaleString() }} 字 ·
              {{ draft.setup.volume_count }} 卷
            </p>
            <p>{{ draft.setup.idea || "尚未填写创作想法" }}</p>
            <p>
              创建后先完善世界观与故事规划，之后依次确认大纲、细纲，再逐章创作。
            </p>
          </div>
          <label class="consent"
            ><input
              v-model="consent"
              type="checkbox"
            />我确认采用以上小说设置</label
          >
          <p>创建操作不生成正文、不调用付费模型，也不自动发布人物设定。</p>
          <p>
            本次只采用此页建书设置。世界与故事规划、全书分卷大纲、章节细纲仍需随后逐一展示、修改并确认。
          </p>
        </fieldset>
        <footer>
          <button type="button" :disabled="actionBlocked" @click="save">
            保存设置草稿</button
          ><button :disabled="actionBlocked || !consent">确认创建小说</button>
        </footer>
      </form>
    </template>
  </section>
</template>

<style scoped>
.novel-entry {
  max-width: 1000px;
  margin: auto;
}
footer,
.section-heading {
  display: flex;
  gap: 12px;
  flex-wrap: wrap;
  align-items: center;
}
.section-heading {
  justify-content: space-between;
}
.opening-phases {
  display: flex;
  gap: 24px;
  padding: 0;
  margin: 24px 0;
  list-style: none;
  color: #65766f;
}
.opening-phases li {
  display: flex;
  align-items: center;
  gap: 8px;
}
.opening-phases li > span {
  display: grid;
  place-items: center;
  width: 26px;
  height: 26px;
  border: 1px solid #cbd6cc;
  border-radius: 50%;
  font-size: 0.85rem;
}
.opening-phases li[aria-current="step"] {
  color: #183b32;
  font-weight: 700;
}
.opening-phases li[aria-current="step"] > span {
  color: white;
  border-color: #24634e;
  background: #24634e;
}
.wizard-form {
  margin-top: 18px;
}
.wizard-form fieldset + fieldset {
  margin-top: 28px;
  border-top: 1px solid #d7ded7;
  padding-top: 24px;
}
.quick-opening {
  margin: 24px 0;
}
.resume-drafts {
  margin-top: 24px;
}
.resume-heading,
.resume-drafts button {
  color: #263b36;
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 12px;
}
.resume-heading h2,
.resume-heading p {
  margin: 0;
}
.resume-drafts ul {
  display: grid;
  gap: 8px;
  max-height: 224px;
  overflow: auto;
  padding: 0;
  margin: 16px 0 0;
  list-style: none;
}
.resume-drafts button {
  width: 100%;
  text-align: left;
  background: #fbfcfa;
}
.resume-drafts button strong {
  min-width: 0;
  overflow-wrap: anywhere;
}
.resume-drafts button span {
  flex-shrink: 0;
  font-size: 0.85rem;
  color: #24634e;
}
.quick-opening label {
  display: grid;
  gap: 8px;
}
.preset-grid {
  display: grid;
  grid-template-columns: repeat(4, minmax(0, 1fr));
  gap: 12px;
  align-items: start;
  margin: 12px 0 18px;
}
.preset-card {
  padding: 10px;
  border: 1px solid #d7ded7;
  border-radius: 12px;
  background: #fbfcfa;
}
.preset-card.selected {
  border-color: #24634e;
  box-shadow: inset 0 0 0 1px #24634e;
  background: #eef6f0;
}
.preset-card button {
  background: transparent;
  color: #263b36;
  border: 1px solid transparent;
  width: 100%;
  white-space: normal;
  text-align: left;
}
.preset-card button:hover {
  background: #e7eee0;
}
.preset-card button[aria-pressed="true"] {
  background: #285c4d;
  color: white;
}
.preset-card button[aria-pressed="true"] > span {
  color: #edf1e6;
}
.preset-card strong,
.preset-card span {
  display: block;
}
.preset-card span,
.preset-card p {
  font-size: 0.9rem;
}
.preset-card button > span {
  display: -webkit-box;
  overflow: hidden;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  margin-top: 4px;
  color: #65766f;
  font-size: 0.8rem;
  font-weight: 400;
}
.preset-details {
  margin-top: 10px;
}
.preset-details > summary {
  margin-bottom: 0;
  color: #24634e;
  font-size: 0.8rem;
  font-weight: 400;
}
.preset-example {
  padding-left: 10px;
  border-left: 2px solid #cbd6cc;
}
.opening-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
}
.advanced {
  padding: 16px;
  border: 1px solid #d7ded7;
  border-radius: 10px;
}
.advanced label {
  margin-top: 14px;
}
.advanced summary {
  cursor: pointer;
  font-weight: 600;
}
.form-disclosure {
  margin-top: 24px;
  padding: 16px;
  border: 1px solid #d7ded7;
  border-radius: 10px;
  background: #fbfcfa;
}
.form-disclosure + .form-disclosure {
  margin-top: 12px;
}
.form-disclosure > summary {
  color: #183b32;
  cursor: pointer;
}
.form-disclosure > summary strong {
  font-size: 1.1rem;
}
.form-disclosure[open] > summary {
  margin-bottom: 20px;
}
.wizard-form > .form-disclosure + fieldset {
  margin-top: 28px;
  border-top: 1px solid #d7ded7;
  padding-top: 24px;
}
.disclosure-description {
  display: block;
  margin-top: 5px;
  font-size: 0.85rem;
  font-weight: 400;
  color: #65766f;
}
.visually-hidden {
  position: absolute;
  width: 1px;
  height: 1px;
  padding: 0;
  margin: -1px;
  overflow: hidden;
  clip-path: inset(50%);
  white-space: nowrap;
  border: 0;
}
.preset-mark {
  color: #24634e;
  font-weight: 600;
}
.preset-review button {
  margin: 8px 8px 0 0;
}
.wizard-form fieldset {
  border: 0;
  padding: 0;
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
.wizard-form fieldset > details,
.wizard-form fieldset > p,
.wizard-form fieldset > .numbers,
.wizard-form fieldset > .summary,
.wizard-form fieldset > .consent,
.wizard-form fieldset > button,
.wizard-form fieldset > .initial-character {
  grid-column: 1 / -1;
}
.wizard-form .initial-character {
  grid-template-columns: repeat(2, minmax(0, 1fr));
}
.initial-character h3,
.initial-character button {
  grid-column: 1 / -1;
}
.direction-picker {
  margin-bottom: 14px;
}
.direction-picker summary {
  cursor: pointer;
  margin-bottom: 16px;
  font-weight: 600;
}
.direction-picker .preset-details > summary {
  margin-bottom: 0;
  font-weight: 400;
}
.assistant-note {
  margin-bottom: 0;
  font-size: 0.85rem;
}
.wizard-form legend {
  font-size: 1.4rem;
  font-weight: 700;
  margin-bottom: 20px;
}
.wizard-form label {
  display: grid;
  gap: 7px;
}
.wizard-form input,
.wizard-form textarea,
.wizard-form select {
  width: 100%;
  box-sizing: border-box;
}
.numbers {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
.initial-character,
.summary {
  padding: 18px;
  border: 1px solid #d7ded7;
  border-radius: 12px;
  display: grid;
  gap: 12px;
}
.consent {
  display: flex !important;
  align-items: center;
}
.consent input {
  width: auto;
}
footer {
  margin-top: 24px;
  justify-content: flex-end;
}
.muted {
  color: #65766f;
}
@media (max-width: 600px) {
  .wizard-form fieldset,
  .wizard-form .initial-character {
    grid-template-columns: 1fr;
  }
  .preset-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
  .numbers {
    grid-template-columns: 1fr;
  }
  .opening-phases {
    gap: 16px;
    font-size: 0.9rem;
  }
}
</style>
