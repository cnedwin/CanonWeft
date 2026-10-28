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
import type { Branch } from "../ip/types";
import type { ChapterDraft, LedgerDraft } from "./chapterTypes";
import GenerationPanel from "../ip/GenerationPanel.vue";
import SemanticReviewPanel from "../ip/SemanticReviewPanel.vue";

const props = defineProps<{
  root: string;
  chapter: ChapterDraft;
  project: Project;
  actor: Actor;
  branch: Branch;
}>();
const emit = defineEmits<{ close: []; expired: []; published: [] }>();
const ledger = ref<LedgerDraft | null>(null),
  candidate = ref<LedgerDraft | null>(null);
const busy = ref(false),
  assistant = ref(false),
  blocking = ref(false);
const error = ref(""),
  notice = ref(""),
  saved = ref("");
type ChapterRelease = { id: string; hash: string; status: string };
const release = ref<ChapterRelease | null>(null);
const reviewing = ref(false),
  reviewBlocking = ref(false);
const releaseOperation = ref(newOperationID());
const reviewReady = ref(false);
const canPublish = computed(
  () =>
    ["owner", "publisher"].includes(props.actor.role) &&
    ["owner", "publisher"].includes(props.project.role),
);
async function publish() {
  if (
    !release.value ||
    !reviewReady.value ||
    reviewBlocking.value ||
    dirty.value
  )
    return;
  await perform(async () => {
    const result = await api<ChapterRelease>(
      `${props.root}/release/${release.value!.id}/confirm`,
      "POST",
      { hash: release.value!.hash, accept: true },
    );
    if (!disposed) {
      release.value = result;
      notice.value = "正文、剧情账本与对应记忆已一起发布。";
      emit("published");
    }
  });
}
async function prepareReview() {
  await perform(async () => {
    await save();
    if (!ledger.value || disposed) return;
    const result = await api<ChapterRelease>(`${props.root}/release`, "POST", {
      revision: props.chapter.revision,
      hash: props.chapter.hash,
      ledger_revision: ledger.value.revision,
      operation_id: releaseOperation.value,
    });
    if (!disposed) {
      release.value = result;
      reviewReady.value = false;
      releaseOperation.value = newOperationID();
      reviewing.value = true;
      notice.value = "已固定正文、章节依据和剧情账本，尚未调用审稿模型或发布。";
    }
  });
}
function closeReview() {
  if (reviewBlocking.value) return;
  reviewing.value = false;
}
const serialized = computed(() =>
  ledger.value ? JSON.stringify(ledger.value.document) : "",
);
const dirty = computed(
  () => !!ledger.value && serialized.value !== saved.value,
);
const entities = computed(() =>
  props.branch.snapshot.entities.filter((e) => e.status !== "disabled"),
);
const kinds = {
  fact: "事实",
  belief: "信念",
  claim: "陈述/传闻",
  intent: "意图",
  hypothesis: "假设",
  event: "事件",
  relation: "关系",
  state: "状态",
  promise: "承诺/伏笔",
  highlight: "高光",
  summary: "摘要",
};
const contexts = {
  reality: "现实",
  flashback: "回忆",
  dream: "梦境",
  hypothetical: "假设语境",
  plan: "未发生的计划",
};
let disposed = false;
function install(value: LedgerDraft) {
  saved.value = JSON.stringify(value.document);
  ledger.value = value;
}
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
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
async function save() {
  if (!ledger.value || (!dirty.value && ledger.value.revision > 0)) return;
  const result = await api<LedgerDraft>(
    `${props.root}/ledger`,
    "PUT",
    ledger.value,
  );
  if (!disposed) {
    install(result);
    notice.value = "剧情账本已保存为私人草稿，尚未写入正史或记忆。";
  }
}
async function openAssistant() {
  await perform(async () => {
    await save();
    if (disposed) return;
    await api(`${props.root}/agents`, "POST", {});
    if (!disposed) {
      candidate.value = null;
      assistant.value = true;
    }
  });
}
function closeAssistant() {
  if (blocking.value) {
    error.value = "请先保存助手请求或清空未保存的输入。";
    return;
  }
  candidate.value = null;
  assistant.value = false;
}
async function apply() {
  if (!candidate.value || !ledger.value || blocking.value) return;
  await perform(async () => {
    if (dirty.value || candidate.value!.revision !== ledger.value!.revision)
      throw new Error("当前账本已有修改，请先保存，再重新查看候选。");
    const value = await api<LedgerDraft>(
      `${props.root}/ledger`,
      "PUT",
      candidate.value,
    );
    if (!disposed) {
      install(value);
      candidate.value = null;
      assistant.value = false;
      notice.value = "账本候选已加入私人草稿，可以逐项修改。";
    }
  });
}
function addEntry() {
  ledger.value?.document.entries.push({
    id: crypto.randomUUID().replaceAll("-", ""),
    kind: "claim",
    context: "reality",
    subject_id: "",
    subject_name: "",
    holder_id: "",
    object_id: "",
    predicate: "",
    statement: "",
    story_time: props.chapter.document.story_time,
    known_from: props.chapter.document.story_time,
    visibility: "author",
    known_by: [],
    quotes: [""],
    evidence: [],
  });
}
async function close() {
  if (reviewing.value || reviewBlocking.value) return;
  if (assistant.value) return;
  await perform(async () => {
    await save();
    if (!disposed && !dirty.value) emit("close");
  });
}
function undo() {
  if (ledger.value && saved.value)
    ledger.value.document = JSON.parse(saved.value);
  error.value = "";
}
function unload(e: BeforeUnloadEvent) {
  if (dirty.value || busy.value || blocking.value || reviewBlocking.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
onMounted(async () => {
  window.addEventListener("beforeunload", unload);
  await perform(async () => {
    const value = await api<LedgerDraft>(`${props.root}/ledger`, "POST", {});
    if (!disposed) install(value);
    const prior = await api<ChapterRelease | null>(`${props.root}/release`);
    if (!disposed) release.value = prior;
  });
});
onBeforeUnmount(() => {
  disposed = true;
  window.removeEventListener("beforeunload", unload);
});
</script>

<template>
  <section class="ledger-editor panel" aria-label="本章剧情账本">
    <header class="actions">
      <h2>本章剧情账本</h2>
      <button :disabled="busy || assistant || reviewing" @click="close">
        保存账本并返回正文
      </button>
    </header>
    <p>
      记账只依据实际正文。人物信念、传闻和计划不会自动变成事实；每条保留原文证据，正式采用仍须独立审查。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <template v-if="ledger">
      <p v-if="!ledger.ready" role="alert">{{ ledger.reason }}</p>
      <p role="status">
        {{
          dirty
            ? "账本有未保存修改"
            : ledger.revision
              ? "私人账本已保存"
              : "尚未保存账本"
        }}
      </p>
      <div class="actions">
        <button
          :disabled="busy || assistant || reviewing"
          @click="() => perform(save)"
        >
          保存私人账本
        </button>
        <button
          :disabled="busy || assistant || reviewing"
          @click="openAssistant"
        >
          请独立 Agent 提取账本
        </button>
        <button
          v-if="dirty"
          :disabled="busy || assistant || reviewing"
          @click="undo"
        >
          撤回未保存的账本编辑
        </button>
        <button
          v-if="assistant"
          :disabled="busy || blocking"
          @click="closeAssistant"
        >
          返回手工记账
        </button>
      </div>
      <div class="actions">
        <button
          :disabled="
            busy ||
            assistant ||
            reviewing ||
            !ledger.ready ||
            !ledger.document.generation_id
          "
          @click="prepareReview"
        >
          保存并提交本章独立审稿
        </button>
        <button
          v-if="release && !reviewing"
          :disabled="busy || assistant || dirty"
          @click="reviewing = true"
        >
          继续查看本章审稿
        </button>
      </div>
      <section
        v-if="reviewing && release"
        aria-label="本章联合审稿候选"
        class="panel"
      >
        <h3>本章联合审稿候选</h3>
        <p>
          正文、章节依据和剧情账本已固定为同一份候选。审稿会读取已确认的世界规划、大纲与细纲；编辑草稿后需要重新提交。
        </p>
        <p>候选编号：{{ release.id }}</p>
        <p>
          准备候选不会调用模型。独立审稿通过并选用报告后，由你确认发布正文、账本和对应记忆。
        </p>
        <button :disabled="reviewBlocking" @click="closeReview">
          返回账本编辑
        </button>
        <SemanticReviewPanel
          v-if="release.status !== 'PUBLISHED'"
          :key="release.id"
          :project="project"
          :actor="actor"
          :branch="branch.id"
          target-kind="artifact_release"
          :target-i-d="release.id"
          :target-hash="release.hash"
          :required="true"
          :blocked="busy"
          @ready="reviewReady = $event"
          @blocking="reviewBlocking = $event"
          @expired="emit('expired')"
        />
        <p v-if="release.status === 'PUBLISHED'" role="status">
          本候选已正式发布，修改正文后需重新记账和审稿。
        </p>
        <button
          v-else
          :disabled="
            busy || !canPublish || !reviewReady || reviewBlocking || dirty
          "
          @click="publish"
        >
          确认采用并发布本章
        </button>
      </section>
      <fieldset :disabled="busy || assistant || reviewing" class="ledger-form">
        <label
          >本章变化摘要<textarea
            v-model="ledger.document.summary"
            rows="3"
            maxlength="2000"
          />
        </label>
        <p v-if="!ledger.document.entries.length">
          当前没有剧情记录，可手动添加或让独立 Agent 从正文提取。
        </p>
        <details
          v-for="(entry, index) in ledger.document.entries"
          :key="entry.id"
          open
          class="ledger-entry"
        >
          <summary>
            {{ index + 1 }} · {{ entry.subject_name || "未关联主体" }} ·
            {{ kinds[entry.kind as keyof typeof kinds] || entry.kind }}
          </summary>
          <div class="ledger-columns">
            <label
              >记录类型<select v-model="entry.kind" aria-label="记录类型">
                <option v-for="(name, key) in kinds" :key="key" :value="key">
                  {{ name }}
                </option>
              </select></label
            >
            <label
              >叙述语境<select v-model="entry.context" aria-label="叙述语境">
                <option v-for="(name, key) in contexts" :key="key" :value="key">
                  {{ name }}
                </option>
              </select></label
            >
            <label
              >主体名称<input v-model="entry.subject_name" maxlength="300"
            /></label>
            <label
              >关联主体身份<select
                v-model="entry.subject_id"
                aria-label="关联主体身份"
              >
                <option value="">尚未关联，只保留名称</option>
                <option
                  v-for="entity in entities"
                  :key="entity.id"
                  :value="entity.id"
                >
                  {{ entity.name }} · {{ entity.id }}
                </option>
              </select></label
            >
            <label
              >信念持有人<select
                v-model="entry.holder_id"
                aria-label="信念持有人"
              >
                <option value="">未知或不适用</option>
                <option
                  v-for="entity in entities"
                  :key="entity.id"
                  :value="entity.id"
                >
                  {{ entity.name }} · {{ entity.id }}
                </option>
              </select></label
            >
            <label
              >关联对象<select v-model="entry.object_id" aria-label="关联对象">
                <option value="">未知或不适用</option>
                <option
                  v-for="entity in entities"
                  :key="entity.id"
                  :value="entity.id"
                >
                  {{ entity.name }} · {{ entity.id }}
                </option>
              </select></label
            >
            <label
              >关系或变化类型<input v-model="entry.predicate" maxlength="200"
            /></label>
            <label
              >故事发生位置<input
                v-model.number="entry.story_time"
                type="number"
                min="0"
                max="9007199254740991"
                step="1"
            /></label>
            <label
              >揭示位置<input
                v-model.number="entry.known_from"
                type="number"
                min="0"
                max="9007199254740991"
                step="1"
            /></label>
            <label
              >知情范围<select v-model="entry.visibility" aria-label="知情范围">
                <option value="author">作者可见，待核对</option>
                <option value="public">公开</option>
                <option value="restricted">指定知情者</option>
              </select></label
            >
            <label v-if="entry.visibility === 'restricted'"
              >知情者<select
                v-model="entry.known_by"
                multiple
                aria-label="知情者"
              >
                <option
                  v-for="entity in entities"
                  :key="entity.id"
                  :value="entity.id"
                >
                  {{ entity.name }} · {{ entity.id }}
                </option>
              </select></label
            >
          </div>
          <label
            >记录内容<textarea
              v-model="entry.statement"
              rows="3"
              maxlength="4000"
            />
          </label>
          <div
            v-for="(_, quoteIndex) in entry.quotes"
            :key="quoteIndex"
            class="quote-input"
          >
            <label
              >正文原文证据<textarea
                v-model="entry.quotes[quoteIndex]"
                rows="3"
                maxlength="8000"
              />
            </label>
            <button
              type="button"
              :disabled="entry.quotes.length <= 1"
              @click="entry.quotes.splice(quoteIndex, 1)"
            >
              移除此段引文
            </button>
          </div>
          <p class="muted">
            请逐字引用足够完整且唯一的原文，系统会核对出处。引文存在不等于记录的解释已审查通过。
          </p>
          <button
            type="button"
            :disabled="entry.quotes.length >= 8"
            @click="entry.quotes.push('')"
          >
            添加原文证据
          </button>
          <button
            type="button"
            @click="ledger.document.entries.splice(index, 1)"
          >
            移除此条记录
          </button>
        </details>
        <button
          type="button"
          :disabled="ledger.document.entries.length >= 128"
          @click="addEntry"
        >
          添加剧情记录
        </button>
      </fieldset>
      <ul v-if="ledger.document.notes.length">
        <li v-for="(note, index) in ledger.document.notes" :key="index">
          {{ note }}
        </li>
      </ul>
      <GenerationPanel
        v-if="assistant"
        :project="project"
        :actor="actor"
        :branch="branch"
        :writing="{
          root,
          revision: chapter.revision,
          hash: chapter.hash,
          role: 'bookkeeper',
        }"
        @ledger-suggestion="candidate = $event"
        @blocking="blocking = $event"
        @expired="emit('expired')"
      />
      <section v-if="candidate" aria-label="账本候选对照" class="panel">
        <h3>账本候选对照</h3>
        <p>
          当前账本：{{ ledger.document.summary }}（{{
            ledger.document.entries.length
          }}条）
        </p>
        <p>
          候选：{{ candidate.document.summary }}（{{
            candidate.document.entries.length
          }}条）
        </p>
        <ul v-if="candidate.document.notes.length">
          <li v-for="(note, index) in candidate.document.notes" :key="index">
            {{ note }}
          </li>
        </ul>
        <article
          v-for="entry in candidate.document.entries"
          :key="entry.id"
          class="ledger-entry"
        >
          <h4>
            {{ kinds[entry.kind as keyof typeof kinds] }} ·
            {{ contexts[entry.context as keyof typeof contexts] }} ·
            {{ entry.subject_name || "未知主体" }}
          </h4>
          <p>{{ entry.statement }}</p>
          <blockquote v-for="(quote, index) in entry.quotes" :key="index">
            {{ quote }}
          </blockquote>
        </article>
        <button :disabled="busy || blocking" @click="apply">
          将账本候选加入私人草稿
        </button>
        <button :disabled="busy" @click="candidate = null">保留原账本</button>
      </section>
    </template>
  </section>
</template>

<style scoped>
.ledger-editor,
.ledger-form {
  display: grid;
  gap: 1rem;
}
.ledger-form {
  border: 0;
  padding: 0;
}
.ledger-form label {
  display: grid;
  gap: 0.35rem;
}
.ledger-columns {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 0.8rem;
  margin: 1rem 0;
}
.ledger-entry {
  border: 1px solid #cbd5e1;
  border-radius: 0.6rem;
  padding: 1rem;
}
.ledger-entry textarea {
  width: 100%;
  box-sizing: border-box;
}
.ledger-entry blockquote {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.actions {
  display: flex;
  gap: 0.6rem;
  align-items: center;
  flex-wrap: wrap;
}
@media (max-width: 760px) {
  .ledger-columns {
    grid-template-columns: 1fr;
  }
}
</style>
