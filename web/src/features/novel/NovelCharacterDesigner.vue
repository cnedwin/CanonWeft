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
import type { Branch, Snapshot } from "../ip/types";
import type {
  CharacterDesignDraft,
  CharacterDesignSuggestion,
  NameCatalog,
} from "./characterTypes";
import CharacterLibrary from "../ip/CharacterLibrary.vue";
import GenerationPanel from "../ip/GenerationPanel.vue";
import CharacterBatchPanel from "./CharacterBatchPanel.vue";
import CharacterSourcePicker from "./CharacterSourcePicker.vue";
import NovelMaterials from "./NovelMaterials.vue";
import NameWorkbench from "./NameWorkbench.vue";

const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
}>();
const emit = defineEmits<{ close: []; expired: []; library: [] }>();
const root = `/novels/${props.book.id}/character-designs`;
const history = ref<CharacterDesignDraft[]>([]);
const current = ref<CharacterDesignDraft | null>(null);
const saved = ref("");
const branch = ref<Branch | null>(null);
const existing = ref<Snapshot>({ entities: [], assertions: [] });
const mode = ref("create");
const count = ref(5);
const chosenExisting = ref<string[]>([]);
const chosenSources = ref<string[]>([]);
const materialsOpen = ref(false);
const sourceRefresh = ref(0);
const notes = ref("");
const genre = ref(props.book.setup.genre);
const createOperation = ref(newOperationID());
const selected = ref<string[]>([]);
const busy = ref(false);
const error = ref("");
const notice = ref("");
const assistant = ref(false);
const batchAssistant = ref(false);
const namingAssistant = ref(false);
const assistantBlocking = ref(false);
const suggestion = ref<CharacterDesignSuggestion | null>(null);
const fieldID = ref("");
const field = computed(() =>
  current.value?.document.candidate.assertions.find(
    (a) => a.id === fieldID.value,
  ),
);
const fieldLocked = computed(
  () =>
    !!field.value &&
    !!current.value?.document.base.assertions.some(
      (a) => a.id === field.value?.id && a.locked,
    ),
);
const dirty = computed(
  () =>
    !!current.value && JSON.stringify(current.value.document) !== saved.value,
);
const locked = computed(() =>
  (current.value?.document.base.entities ?? [])
    .filter((e) => e.locked)
    .map((e) => e.id),
);
const cards = computed(
  () =>
    current.value?.document.candidate.entities.filter((e) =>
      current.value?.document.ids.includes(e.id),
    ) ?? [],
);
const applyRequest = ref<{
  revision: number;
  hash: string;
  ip_revision: number;
  selected: string[];
  operation_id: string;
} | null>(null);
let disposed = false;

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
function show(draft: CharacterDesignDraft) {
  current.value = draft;
  saved.value = JSON.stringify(draft.document);
  selected.value = [...draft.document.ids];
  fieldID.value = "";
  suggestion.value = null;
}
async function loadHistory() {
  const data = await api<{ items: CharacterDesignDraft[] }>(root);
  if (!disposed) history.value = data.items;
}
async function loadExisting() {
  const data = await api<{ revision: number; snapshot: Snapshot }>(
    `/projects/${props.project.id}/branches/${props.book.branch_id}/draft`,
  );
  if (disposed) return;
  if (data.revision > 0) existing.value = data.snapshot;
  else {
    const value = await api<Branch>(
      `/projects/${props.project.id}/branches/${props.book.branch_id}`,
    );
    if (!disposed) existing.value = value.snapshot;
  }
}
function open(id: string) {
  if (dirty.value || assistant.value) return;
  void perform(async () => {
    const draft = await api<CharacterDesignDraft>(`${root}/${id}`);
    if (!disposed) {
      show(draft);
      applyRequest.value = null;
      notice.value = "";
    }
  });
}
watch(
  [notes, genre, mode, count, chosenExisting, chosenSources],
  () => {
    createOperation.value = newOperationID();
  },
  { deep: true },
);
function create() {
  void perform(async () => {
    const draft = await api<CharacterDesignDraft>(root, "POST", {
      operation_id: createOperation.value,
      notes: notes.value,
      genre: genre.value,
      count: mode.value === "create" ? count.value : 0,
      entity_ids: mode.value === "enrich" ? chosenExisting.value : [],
      source_ids: chosenSources.value,
    });
    if (disposed) return;
    show(draft);
    createOperation.value = newOperationID();
    applyRequest.value = null;
    notice.value = "批次已保存。可以手动编辑，也可让助手按要求生成。";
    await loadHistory();
  });
}
async function saveCurrent() {
  if (!current.value || !dirty.value) return;
  const value = await api<CharacterDesignDraft>(
    `${root}/${current.value.resource_id}`,
    "PUT",
    current.value,
  );
  if (!disposed) {
    current.value = value;
    saved.value = JSON.stringify(value.document);
    applyRequest.value = null;
    notice.value = "角色候选已保存，尚未加入本书角色库。";
  }
}
function save() {
  void perform(async () => {
    await saveCurrent();
    await loadHistory();
  });
}
function openMaterials() {
  if (assistant.value) return;
  void perform(async () => {
    await saveCurrent();
    materialsOpen.value = true;
  });
}
function closeMaterials() {
  materialsOpen.value = false;
  sourceRefresh.value++;
  void perform(loadExisting);
}
function discard() {
  if (current.value) current.value.document = JSON.parse(saved.value);
  suggestion.value = null;
  fieldID.value = "";
}
function assist(batched = false) {
  void perform(async () => {
    await saveCurrent();
    if (!current.value) return;
    const useBatch = batched || current.value.document.ids.length > 5;
    await api(
      useBatch
        ? `${root}/${current.value.resource_id}/batch-agents`
        : `${root}/agents`,
      "POST",
      {},
    );
    const value = await api<Branch>(
      `/projects/${props.project.id}/branches/${props.book.branch_id}`,
    );
    if (!disposed) {
      branch.value = value;
      assistant.value = true;
      batchAssistant.value = useBatch;
      namingAssistant.value = false;
      suggestion.value = null;
    }
  });
}
function assistNames() {
  void perform(async () => {
    if (!current.value?.document.naming) return;
    const catalog = await api<NameCatalog>(
      `${root}/name-catalog/open`,
      "POST",
      {},
    );
    if (disposed) return;
    current.value.document.naming.catalog = {
      resource_id: catalog.resource_id,
      revision: catalog.revision,
      hash: catalog.hash,
    };
    await saveCurrent();
    if (!current.value) return;
    await api(`${root}/naming-agents`, "POST", {});
    const value = await api<Branch>(
      `/projects/${props.project.id}/branches/${props.book.branch_id}`,
    );
    if (!disposed) {
      branch.value = value;
      namingAssistant.value = true;
      batchAssistant.value = false;
      assistant.value = true;
      suggestion.value = null;
    }
  });
}
function receiveSuggestion(value: CharacterDesignSuggestion) {
  if (
    current.value?.revision === value.draft.revision &&
    current.value.hash === value.draft.hash
  )
    suggestion.value = value;
}
function useSuggestion() {
  if (!suggestion.value || !current.value) return;
  current.value.document = suggestion.value.draft.document;
  assistant.value = false;
  assistantBlocking.value = false;
  notice.value = "候选已放入编辑区，请核对并保存；尚未加入角色库。";
  suggestion.value = null;
}
function apply() {
  if (!current.value || dirty.value) return;
  void perform(async () => {
    const d = current.value!;
    const payload = {
      revision: d.revision,
      hash: d.hash,
      ip_revision: d.ip_revision,
      selected: [...selected.value],
    };
    const old = applyRequest.value;
    if (
      !old ||
      old.revision !== payload.revision ||
      old.hash !== payload.hash ||
      old.ip_revision !== payload.ip_revision ||
      JSON.stringify(old.selected) !== JSON.stringify(payload.selected)
    )
      applyRequest.value = {
        ...payload,
        operation_id: newOperationID(),
      };
    const result = await api<{
      draft: CharacterDesignDraft;
      warnings: string[];
      replay: boolean;
    }>(`${root}/${d.resource_id}/apply`, "POST", applyRequest.value);
    if (disposed) return;
    const selection = [...selected.value];
    show(result.draft);
    selected.value = selection;
    notice.value = [
      result.replay
        ? "该操作此前已完成。"
        : "选中角色已加入本书设定草稿。请在角色库检查并确认正式采用。",
      ...result.warnings,
    ].join("\n");
    await loadExisting();
    await loadHistory();
  });
}
function unload(event: BeforeUnloadEvent) {
  if (dirty.value) {
    event.preventDefault();
    event.returnValue = "";
  }
}
onMounted(() => {
  window.addEventListener("beforeunload", unload);
  void perform(async () => {
    await Promise.all([loadHistory(), loadExisting()]);
  });
});
onBeforeUnmount(() => {
  disposed = true;
  window.removeEventListener("beforeunload", unload);
});
</script>

<template>
  <NovelMaterials
    v-if="materialsOpen"
    :book="book"
    :project="project"
    :actor="actor"
    return-label="返回角色创作"
    @close="closeMaterials"
    @expired="emit('expired')"
  />
  <section v-show="!materialsOpen" class="character-designer">
    <header class="section-heading">
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>批量角色与一句话完善</h1>
      </div>
      <button
        :disabled="busy || dirty || assistantBlocking"
        @click="emit('close')"
      >
        返回小说工作台
      </button>
    </header>
    <p role="alert" v-if="error">{{ error }}</p>
    <p role="status" class="notice" v-if="notice">{{ notice }}</p>
    <section class="panel">
      <h2>创作要求</h2>
      <fieldset :disabled="busy || dirty || assistant">
        <label
          >创作方式<select v-model="mode">
            <option value="create">批量创建新角色</option>
            <option value="enrich">完善已有角色</option>
          </select></label
        >
        <label v-if="mode === 'create'"
          >本批人数<input
            v-model.number="count"
            type="number"
            min="1"
            max="100"
        /></label>
        <div v-else>
          <p>选择要完善的角色</p>
          <label
            v-for="person in existing.entities.filter(
              (e) => e.kind === 'character',
            )"
            :key="person.id"
            ><input
              v-model="chosenExisting"
              type="checkbox"
              :value="person.id"
            />{{ person.name }} · {{ person.id }}</label
          >
        </div>
        <label
          >题材与人物风格<input
            v-model="genre"
            list="character-genres"
            maxlength="300"
        /></label>
        <datalist id="character-genres">
          <option>悬疑 · 动机、秘密与线索</option>
          <option>仙侠玄幻 · 阵营、能力与代价</option>
          <option>都市职业 · 专业背景与社会关系</option>
          <option>情感 · 目标、边界与成长</option>
          <option>历史 · 时代身份与生活细节</option>
        </datalist>
        <CharacterSourcePicker
          :book-id="book.id"
          v-model="chosenSources"
          :disabled="busy || dirty || assistant"
          :refresh-key="sourceRefresh"
          @materials="openMaterials"
          @expired="emit('expired')"
        />
        <label
          >用一句话或详细文字描述要求<textarea
            v-model="notes"
            rows="4"
            maxlength="10000"
            placeholder="例如：创建5名边城悬疑角色，有互相矛盾的动机，名字朴实，每人有秘密和可辨认的说话习惯。"
          />
        </label>
        <button
          :disabled="
            !notes.trim() || (mode === 'enrich' && !chosenExisting.length)
          "
          @click="create"
        >
          建立可编辑角色批次
        </button>
      </fieldset>
      <p>
        默认每批5人。先形成草稿，满意后才选中加入；不会自动发布。人数较多时，需按模型输出额度分批处理。
      </p>
    </section>
    <section class="panel">
      <h2>最近的角色批次</h2>
      <p v-if="!history.length">还没有保存的角色批次。</p>
      <button
        v-for="item in history"
        :key="item.resource_id"
        :disabled="busy || dirty || assistant"
        @click="open(item.resource_id)"
      >
        {{ item.document.notes.slice(0, 40) }} ·
        {{ item.document.ids.length }}人 · 版本{{ item.revision }}
      </button>
    </section>
    <section v-if="current" class="panel">
      <h2>编辑与选择候选</h2>
      <p>
        {{ dirty ? "有未保存修改" : "当前候选已保存" }} · 批次
        {{ current.resource_id }}
      </p>
      <div class="actions">
        <button :disabled="busy || !dirty || assistant" @click="save">
          保存角色候选</button
        ><button :disabled="busy || !dirty || assistant" @click="discard">
          撤销未保存修改</button
        ><button v-if="!assistant" :disabled="busy" @click="assist()">
          让助手生成或继续完善</button
        ><button
          v-else
          :disabled="busy || assistantBlocking"
          @click="
            assistant = false;
            suggestion = null;
          "
        >
          关闭助手，继续编辑
        </button>
        <button v-if="!assistant" :disabled="busy" @click="assist(true)">
          分段生成 / 恢复任务
        </button>
      </div>
      <CharacterSourcePicker
        :book-id="book.id"
        :model-value="current.document.source_ids ?? []"
        @update:model-value="current.document.source_ids = $event"
        :disabled="busy || assistant"
        :refresh-key="sourceRefresh"
        @materials="openMaterials"
        @expired="emit('expired')"
      />
      <template v-if="assistant && branch">
        <GenerationPanel
          v-if="!batchAssistant"
          :project="project"
          :actor="actor"
          :branch="branch"
          :characters="{
            root: `${root}/${current.resource_id}${namingAssistant ? '/naming' : ''}`,
            revision: current.revision,
            hash: current.hash,
            notes: current.document.notes,
          }"
          @expired="emit('expired')"
          @blocking="assistantBlocking = $event"
          @character-suggestion="receiveSuggestion"
        />
        <CharacterBatchPanel
          v-else
          :key="current.resource_id"
          :book="book"
          :project="project"
          :draft="current"
          @expired="emit('expired')"
          @blocking="assistantBlocking = $event"
          @suggestion="receiveSuggestion"
        />
        <section v-if="suggestion" aria-label="角色修改建议">
          <h3>助手建议</h3>
          <p>{{ suggestion.explanation }}</p>
          <p v-for="warning in suggestion.warnings" :key="warning">
            {{ warning }}
          </p>
          <ul
            v-if="namingAssistant && suggestion.draft.document.naming"
            aria-label="起名建议预览"
          >
            <li
              v-for="name in suggestion.draft.document.naming.candidates"
              :key="name.id"
            >
              <strong>{{ name.name }}</strong> · {{ name.pronunciation }} ·
              {{ name.rationale }}<span v-if="name.locked"> · 已锁定保留</span>
            </li>
          </ul>
          <CharacterLibrary
            v-else
            :snapshot="suggestion.draft.document.candidate"
            context-label="正在预览助手建议；放入编辑区并保存后，仍须选择加入本书设定草稿。"
            :visible-entity-ids="current.document.ids"
            :can-edit="false"
            :disabled="true"
            :locked-entity-ids="locked"
          /><button
            :disabled="busy || assistantBlocking"
            @click="useSuggestion"
          >
            将建议放入编辑区
          </button>
        </section>
      </template>
      <NameWorkbench
        v-if="!assistant"
        :draft="current"
        :root="root"
        :disabled="busy"
        @update="current.document = $event"
        @assist="assistNames"
        @expired="emit('expired')"
      />
      <CharacterLibrary
        v-if="!assistant"
        :snapshot="current.document.candidate"
        context-label="正在编辑私人角色候选；修改后请保存，再选择加入本书设定草稿。"
        :visible-entity-ids="current.document.ids"
        :can-edit="true"
        :disabled="busy"
        :locked-entity-ids="locked"
        @edit-assertion="fieldID = $event"
      />
      <fieldset v-if="field && !assistant" :disabled="busy || fieldLocked">
        <legend>编辑人物属性或关系</legend>
        <label>属性<input v-model="field.predicate" /></label
        ><label>内容<textarea v-model="field.value" /></label
        ><label
          >关联人物<select v-model="field.object_id">
            <option value="">无关联人物</option>
            <option
              v-for="person in current.document.candidate.entities"
              :key="person.id"
              :value="person.id"
            >
              {{ person.name }} · {{ person.id }}
            </option>
          </select></label
        ><label
          >类别<select v-model="field.kind">
            <option value="fact">已设定事实</option>
            <option value="intent">意图</option>
            <option value="hypothesis">推测</option>
            <option value="belief">信念</option>
            <option value="claim">陈述</option>
          </select></label
        ><label
          >意图、信念或陈述持有人<select v-model="field.holder_id">
            <option value="">无</option>
            <option
              v-for="person in current.document.candidate.entities"
              :key="person.id"
              :value="person.id"
            >
              {{ person.name }}
            </option>
          </select></label
        ><label
          >生效故事时间<input
            v-model.number="field.effective_from"
            type="number" /></label
        ><label
          ><input
            v-model="field.locked"
            type="checkbox"
          />锁定本属性，助手须原样保留</label
        ><button
          @click="
            current.document.candidate.assertions =
              current.document.candidate.assertions.filter(
                (a) => a.id !== fieldID,
              );
            fieldID = '';
          "
        >
          移除此候选属性
        </button>
      </fieldset>
      <fieldset :disabled="busy || dirty || assistant">
        <legend>选择加入本书的人物</legend>
        <label v-for="person in cards" :key="person.id"
          ><input v-model="selected" type="checkbox" :value="person.id" />{{
            person.name
          }}
          · {{ person.id }}</label
        ><button :disabled="!selected.length" @click="apply">
          将选中角色加入本书设定草稿
        </button>
      </fieldset>
      <p>
        关系所指的新角色须一并选择，或先编辑关系。已有角色被其他操作修改时会阻止覆盖，请重新打开核对。
      </p>
      <button
        :disabled="busy || dirty || assistantBlocking"
        @click="emit('library')"
      >
        打开本书角色库，检查与确认采用
      </button>
    </section>
  </section>
</template>

<style scoped>
.character-designer {
  display: grid;
  gap: 1rem;
  max-width: 1160px;
  margin: 0 auto;
  padding: 1rem;
}
fieldset {
  display: grid;
  gap: 0.75rem;
  border: 0;
  padding: 0.5rem 0;
}
label {
  display: grid;
  gap: 0.3rem;
}
label:has(input[type="checkbox"]) {
  display: flex;
  align-items: center;
  gap: 0.5rem;
}
input[type="checkbox"] {
  width: auto;
  flex: 0 0 auto;
}
textarea,
input:not([type="checkbox"]),
select {
  max-width: 100%;
  padding: 0.5rem;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.5rem;
}
.notice {
  white-space: pre-wrap;
}
</style>
