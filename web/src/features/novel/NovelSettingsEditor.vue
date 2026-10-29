<script setup lang="ts">
import {
  computed,
  nextTick,
  onMounted,
  onBeforeUnmount,
  ref,
  watch,
} from "vue";
import { api, APIError, message, newOperationID } from "../../api";
import type { ResourceNavigationRequest } from "./resourceNavigation";
import type {
  NovelBook,
  NovelSettingsDraft,
  NovelSettingsPreview,
  NovelSettingsVersion,
} from "./types";

const props = defineProps<{ book: NovelBook; novelNavigation?: boolean }>();
const emit = defineEmits<{
  close: [];
  updated: [book: NovelBook];
  expired: [];
  editResource: [request: ResourceNavigationRequest];
}>();
const root = `/novels/${props.book.id}/settings`;
const draft = ref<NovelSettingsDraft | null>(null);
const versions = ref<NovelSettingsVersion[]>([]);
const models = ref<{ id: string; name: string; model: string }[]>([]);
const preview = ref<NovelSettingsPreview | null>(null);
const previewNeedsRefresh = ref(false);
const settingsElement = ref<HTMLElement | null>(null);
const focusedResource = ref("");
const saved = ref("");
const saving = ref(false);
const action = ref(false);
const consent = ref(false);
const discardConsent = ref(false);
const currentRevision = ref(props.book.settings_revision);
const error = ref("");
const fields = ref<Record<string, string>>({});
const operation = ref(newOperationID());
let alive = true;
let navigationEpoch = 0;
let timer: ReturnType<typeof setTimeout> | undefined;
const content = computed(() =>
  draft.value
    ? JSON.stringify({
        base_revision: draft.value.base_revision,
        setup: draft.value.setup,
      })
    : "",
);
const dirty = computed(() => content.value !== saved.value);
const stale = computed(
  () => !!draft.value && draft.value.base_revision !== currentRevision.value,
);
function openResource(item: NovelSettingsPreview["review_resources"][number]) {
  if (
    !props.novelNavigation ||
    dirty.value ||
    saving.value ||
    action.value ||
    !preview.value?.review_resources.some((resource) => resource.id === item.id)
  )
    return;
  focusedResource.value = `settings:${item.id}`;
  emit("editResource", {
    ref: {
      kind: "resource",
      branch_id: props.book.branch_id,
      id: item.id,
      revision: item.head,
      hash: "",
    },
    title: item.name,
    explanation:
      "设置修改差异将此资料列为待复核范围；请结合当前版本人工检查，原差异不代表已核对新稿。",
    focus_key: focusedResource.value,
  });
}
async function afterNovelEdit() {
  if (!alive) return;
  ++navigationEpoch;
  previewNeedsRefresh.value = true;
  consent.value = false;
  await nextTick();
  if (!alive) return;
  const element = Array.from(
    settingsElement.value?.querySelectorAll<HTMLButtonElement>(
      "[data-novel-focus]",
    ) ?? [],
  ).find((button) => button.dataset.novelFocus === focusedResource.value);
  element?.scrollIntoView({ block: "center" });
  element?.focus();
}
function canOpenNovelResource(): boolean {
  return (
    alive && !!preview.value && !action.value && !saving.value && !dirty.value
  );
}
defineExpose({ afterNovelEdit, canOpenNovelResource });
const textFields = [
  ["title", "书名"],
  ["genre", "题材"],
  ["language", "语言"],
  ["idea", "核心梗概"],
  ["conflict", "主线目标与冲突"],
  ["audience", "目标读者"],
  ["world", "世界背景"],
  ["theme", "主题"],
  ["include", "希望出现的内容"],
  ["avoid", "希望避免的内容"],
  ["perspective", "叙事视角"],
  ["style", "文风"],
] as const;
const numberFields = [
  ["target_words", "目标总字数"],
  ["volume_count", "计划卷数"],
  ["chapter_min", "每章最少字数"],
  ["chapter_max", "每章最多字数"],
] as const;
const labels: Record<string, string> = Object.fromEntries([
  ...textFields,
  ...numberFields,
]);
Object.assign(labels, {
  settings: "小说设置",
  creation_record: "创建记录",
  protection: "设定保护",
  creation_mode: "创作方式",
  model_choice: "模型准备",
  connection_id: "写作模型",
});
function failure(cause: unknown) {
  error.value = message(cause);
  if (cause instanceof APIError) {
    fields.value = cause.fields;
    if (cause.status === 401) emit("expired");
    if (cause.status === 409)
      error.value =
        "设置版本或复核范围已变化，当前输入已保留。请重新查看差异，或读取最新设置后继续。";
  }
}
function schedule() {
  clearTimeout(timer);
  timer = setTimeout(() => {
    if (alive && !saving.value && !action.value && dirty.value) void save();
  }, 1000);
}
watch(
  content,
  () => {
    preview.value = null;
    consent.value = false;
    operation.value = newOperationID();
    if (draft.value) schedule();
  },
  { flush: "sync" },
);
async function save() {
  if (!draft.value || saving.value) return false;
  if (!dirty.value) return true;
  saving.value = true;
  error.value = "";
  fields.value = {};
  const input = JSON.parse(JSON.stringify(draft.value)) as NovelSettingsDraft;
  const sent = content.value;
  try {
    const result = await api<NovelSettingsDraft>(`${root}/draft`, "PUT", input);
    if (!alive || !draft.value) return false;
    draft.value.revision = result.revision;
    saved.value = sent;
    return true;
  } catch (cause) {
    if (alive) failure(cause);
    return false;
  } finally {
    saving.value = false;
    if (alive && content.value !== sent && dirty.value) schedule();
  }
}
async function review() {
  if (action.value || saving.value || !draft.value) return;
  action.value = true;
  clearTimeout(timer);
  const reviewed = content.value;
  const navigation = navigationEpoch;
  try {
    if (!(await save()) || !alive || content.value !== reviewed) return;
    const value = await api<NovelSettingsPreview>(`${root}/preview`, "POST", {
      draft_revision: draft.value.revision,
    });
    if (!alive || content.value !== reviewed || navigation !== navigationEpoch)
      return;
    preview.value = value;
    previewNeedsRefresh.value = false;
    consent.value = false;
  } catch (cause) {
    if (alive) failure(cause);
  } finally {
    action.value = false;
  }
}
async function confirm() {
  if (
    !preview.value ||
    !consent.value ||
    action.value ||
    saving.value ||
    dirty.value ||
    previewNeedsRefresh.value
  )
    return;
  action.value = true;
  error.value = "";
  try {
    const book = await api<NovelBook>(`${root}/confirm`, "POST", {
      draft_revision: preview.value.draft_revision,
      expected_revision: preview.value.base_revision,
      preview_hash: preview.value.hash,
      operation_id: operation.value,
      accept_settings: true,
    });
    const latest = await api<NovelBook>(`/novels/${book.id}`);
    if (alive) emit("updated", latest);
  } catch (cause) {
    if (alive) {
      failure(cause);
      if (cause instanceof APIError && cause.status === 409) {
        preview.value = null;
        consent.value = false;
      }
    }
  } finally {
    action.value = false;
  }
}
async function close() {
  if (action.value || saving.value) return;
  if (!draft.value || ((await save()) && !dirty.value)) emit("close");
}
function guard(event: BeforeUnloadEvent) {
  if (draft.value && (dirty.value || saving.value || action.value)) {
    event.preventDefault();
    event.returnValue = "";
  }
}
async function loadLatest() {
  if (!discardConsent.value || saving.value || action.value) return;
  action.value = true;
  clearTimeout(timer);
  try {
    const [book, stored] = await Promise.all([
      api<NovelBook>(`/novels/${props.book.id}`),
      api<NovelSettingsDraft>(`${root}/draft`),
    ]);
    if (!alive) return;
    currentRevision.value = book.settings_revision;
    draft.value = {
      revision: stored.revision,
      base_revision: book.settings_revision,
      setup: book.setup,
    };
    discardConsent.value = false;
    await save();
  } catch (cause) {
    if (alive) failure(cause);
  } finally {
    action.value = false;
  }
}
onMounted(async () => {
  window.addEventListener("beforeunload", guard);
  action.value = true;
  try {
    const [current, history, connections] = await Promise.all([
      api<NovelSettingsDraft>(`${root}/draft`),
      api<{ items: NovelSettingsVersion[] }>(`${root}/versions`),
      api<{ items: { id: string; name: string; model: string }[] }>(
        "/novel-setups/model-choices",
      ),
    ]);
    if (!alive) return;
    draft.value = current;
    saved.value = content.value;
    versions.value = history.items;
    models.value = connections.items;
  } catch (cause) {
    if (alive) failure(cause);
  } finally {
    action.value = false;
  }
});
onBeforeUnmount(() => {
  alive = false;
  clearTimeout(timer);
  window.removeEventListener("beforeunload", guard);
});
</script>

<template>
  <section ref="settingsElement" class="settings-editor">
    <header>
      <div>
        <h1>修改小说设置</h1>
        <p>草稿仅自己可见。查看差异并确认后，才生成新的设置版本。</p>
      </div>
      <button :disabled="action || saving" @click="close">
        保存并返回小说
      </button>
    </header>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <ul v-if="Object.keys(fields).length" class="error">
      <li v-for="(value, key) in fields" :key="key">
        {{ labels[key] || key }}：{{ value }}
      </li>
    </ul>
    <template v-if="draft">
      <p role="status">
        基于设置版本 {{ draft.base_revision }} ·
        {{
          saving ? "正在保存草稿…" : dirty ? "有未保存修改" : "设置草稿已暂存"
        }}
      </p>
      <p v-if="stale" class="notice">
        这份草稿基于较早版本，确认时会重新核对版本，已有作品不会被覆盖。
      </p>
      <form class="panel" @submit.prevent="review">
        <fieldset :disabled="action">
          <legend>创作约定</legend>
          <label v-for="field in textFields" :key="field[0]"
            >{{ field[1]
            }}<textarea
              v-model="draft.setup[field[0]]"
              :rows="
                [
                  'title',
                  'genre',
                  'language',
                  'audience',
                  'perspective',
                ].includes(field[0])
                  ? 1
                  : 3
              "
            />
          </label>
          <div class="numbers">
            <label v-for="field in numberFields" :key="field[0]"
              >{{ field[1]
              }}<input
                v-model.number="draft.setup[field[0]]"
                type="number"
                min="1"
                step="1"
            /></label>
          </div>
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
          <label
            >创作方式<select
              v-model="draft.setup.creation_mode"
              @change="
                draft.setup.model_choice = '';
                draft.setup.connection_id = '';
              "
            >
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
                  v-for="model in models"
                  :key="model.id"
                  :value="model.id"
                >
                  {{ model.name }} · {{ model.model }}
                </option>
              </select></label
            ></template
          >
          <p>原稿、IP来源和初始人物保留为创建记录。修改人物请返回角色库。</p>
        </fieldset>
        <footer>
          <button type="button" :disabled="saving || action" @click="save">
            保存设置修改草稿</button
          ><button :disabled="saving || action">查看修改差异</button>
        </footer>
      </form>
      <section v-if="preview" class="panel" aria-label="设置修改差异">
        <h2>确认修改范围</h2>
        <p v-if="previewNeedsRefresh" role="status">
          已从资料修订返回，原差异与待复核范围需要重新检查。设置输入和原清单仍保留，请明确点击“查看修改差异”后再确认采用。
        </p>
        <div
          v-for="change in preview.changes"
          :key="change.field"
          class="difference"
        >
          <strong>{{ change.label }}</strong>
          <div>
            <small>修改前</small>
            <p>{{ change.before || "未填写" }}</p>
          </div>
          <div>
            <small>修改后</small>
            <p>{{ change.after || "未填写" }}</p>
          </div>
        </div>
        <p v-if="preview.planning_changed">
          这些改动涉及创作规则，世界与故事规划需要复核。已有正文、角色和图片不会被自动改写。
        </p>
        <p v-else>本次不改变世界与故事规划。模型相关修改只用于后续生成。</p>
        <template v-if="preview.review_resources.length"
          ><h3>同分支待复核资料</h3>
          <p>以下是需要检查的范围，不表示每份资料都必须修改。</p>
          <ul>
            <li v-for="item in preview.review_resources" :key="item.id">
              {{ item.name }} · {{ item.head ? "已发布版本" : "未发布资料" }}
              <button
                v-if="novelNavigation"
                :aria-label="`打开小说资料 ${item.id}`"
                :data-novel-focus="`settings:${item.id}`"
                :disabled="dirty || saving || action"
                @click="openResource(item)"
              >
                打开资料修订
              </button>
            </li>
          </ul></template
        >
        <label class="consent"
          ><input
            v-model="consent"
            type="checkbox"
            :disabled="action || previewNeedsRefresh"
          />我已查看差异，确认采用这些设置</label
        ><button
          :disabled="
            !consent || action || saving || dirty || previewNeedsRefresh
          "
          @click="confirm"
        >
          确认保存新版本
        </button>
      </section>
      <details class="panel">
        <summary>设置历史（最近100版）</summary>
        <article v-for="version in versions" :key="version.revision">
          <h3>版本 {{ version.revision }} · {{ version.setup.title }}</h3>
          <p>{{ version.setup.idea }}</p>
          <p>
            {{ version.setup.perspective }} · {{ version.setup.style }} ·
            {{ version.setup.target_words.toLocaleString() }}字
          </p>
        </article>
      </details>
      <details class="panel">
        <summary>放弃当前修改，读取最新设置</summary>
        <p>此操作会替换你在本页的修改草稿。其他作者的草稿不受影响。</p>
        <label class="consent"
          ><input
            v-model="discardConsent"
            type="checkbox"
            :disabled="action || saving"
          />我确认放弃当前修改</label
        ><button
          :disabled="!discardConsent || saving || action"
          @click="loadLatest"
        >
          读取最新设置并暂存
        </button>
      </details>
    </template>
  </section>
</template>

<style scoped>
.settings-editor {
  max-width: 1000px;
  margin: auto;
}
header,
footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}
.panel {
  margin: 20px 0;
}
fieldset {
  border: 0;
  padding: 0;
  display: grid;
  gap: 16px;
}
legend {
  font-size: 1.4rem;
  margin-bottom: 16px;
}
label {
  display: grid;
  gap: 8px;
}
textarea,
input,
select {
  width: 100%;
  box-sizing: border-box;
}
.numbers {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
}
.difference {
  display: grid;
  grid-template-columns: 130px 1fr 1fr;
  gap: 16px;
  border-bottom: 1px solid #d7ded7;
  padding: 16px 0;
}
.difference p {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.consent {
  display: flex;
  align-items: center;
  margin: 16px 0;
}
.consent input {
  width: auto;
}
footer {
  justify-content: flex-end;
  margin-top: 20px;
}
summary {
  cursor: pointer;
}
@media (max-width: 650px) {
  .difference,
  .numbers {
    grid-template-columns: 1fr;
  }
  header {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
