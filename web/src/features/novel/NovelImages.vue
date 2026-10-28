<script setup lang="ts">
import { computed, onMounted, onBeforeUnmount, ref, watch } from "vue";
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
import CharacterImageGenerator from "../ip/CharacterImageGenerator.vue";
import {
  visualLabel,
  type NovelImageTarget,
  type VisualImageChoice,
} from "../ip/visualTypes";
const props = defineProps<{
  book: NovelBook;
  project: Project;
  actor: Actor;
}>();
const emit = defineEmits<{ close: []; expired: [] }>();
interface ImageItem extends VisualImageChoice {
  name: string;
  published: boolean;
  needs_review?: string;
}
const root = `/novels/${props.book.id}/images`;
const projectRoot = `/projects/${props.project.id}`;
const branch = ref<Branch>();
const snapshot = ref<Snapshot>();
const resources = ref<{ id: string; name: string; head: string }[]>([]);
const items = ref<ImageItem[]>([]),
  cursor = ref("");
const kind = ref<"cover" | "scene">("cover"),
  name = ref("主封面");
const sourceIDs = ref<string[]>([]),
  characterIDs = ref<string[]>([]);
const busy = ref(false),
  generatorBlocked = ref(false),
  error = ref(""),
  notice = ref("");
const selected = ref<NovelImageTarget>();
const releases = ref<Record<string, string>>({});
const description = ref(""),
  source = ref(""),
  rights = ref("");
const file = ref<File>(),
  fileInput = ref<HTMLInputElement>();
const uploadDirty = computed(
  () => !!file.value || !!description.value || !!source.value || !!rights.value,
);
const blocked = computed(
  () => busy.value || generatorBlocked.value || uploadDirty.value,
);
const canPublish = computed(
  () =>
    ["owner", "publisher"].includes(props.actor.role) &&
    ["owner", "publisher"].includes(props.project.role),
);
const references = computed(() =>
  items.value.filter(
    (i) =>
      !!i.report.novel_image ||
      (!!i.report.character_image &&
        (selected.value?.character_ids ?? characterIDs.value).includes(
          i.report.character_image.character_id ?? "",
        )),
  ),
);
const gallery = computed(() =>
  items.value.filter((i) => !!i.report.novel_image),
);
const characters = computed(
  () => snapshot.value?.entities.filter((e) => e.kind === "character") ?? [],
);
let alive = true;
let uploadAttempt: { body: FormData; operation: string } | undefined;
watch([file, description, source, rights, kind, name], () => {
  uploadAttempt = undefined;
});
function showError(e: unknown) {
  if (!alive) return;
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
watch([kind, name, sourceIDs, characterIDs], () => {
  selected.value = undefined;
});
async function load(more = false) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    const [w, images, sources, draft] = await Promise.all([
      api<{ branch: Branch }>(`${root}/workspace`),
      api<{ items: ImageItem[]; next: string }>(
        `${root}?after=${more ? cursor.value : ""}`,
      ),
      api<{ items: { id: string; name: string; head: string }[] }>(
        `${projectRoot}/resources?branch=${props.book.branch_id}`,
      ),
      api<{ revision: number; snapshot: Snapshot }>(
        `${projectRoot}/branches/${props.book.branch_id}/draft`,
      ),
    ]);
    if (!alive) return;
    branch.value = w.branch;
    snapshot.value =
      draft.revision > 0
        ? draft.snapshot
        : (await api<Branch>(`${projectRoot}/branches/${props.book.branch_id}`))
            .snapshot;
    if (!alive) return;
    resources.value = sources.items.filter((r) => !!r.head);
    items.value = more
      ? [
          ...new Map(
            [...items.value, ...images.items].map((i) => [i.id, i]),
          ).values(),
        ]
      : images.items;
    cursor.value = images.next;
  } catch (e) {
    showError(e);
  } finally {
    if (alive) busy.value = false;
  }
}
function openScheme() {
  if (blocked.value) return;
  if (
    !name.value.trim() ||
    sourceIDs.value.length > 12 ||
    characterIDs.value.length > 12
  ) {
    error.value = "请填写方案名称，角色与资料分别最多选择12项。";
    return;
  }
  selected.value = {
    book_id: props.book.id,
    kind: kind.value,
    name: name.value.trim(),
    source_ids: [...sourceIDs.value],
    character_ids: [...characterIDs.value],
  };
}
function discardUpload() {
  file.value = undefined;
  description.value = "";
  source.value = "";
  rights.value = "";
  if (fileInput.value) fileInput.value.value = "";
}
async function upload() {
  if (busy.value || !file.value) return;
  busy.value = true;
  error.value = "";
  try {
    if (!uploadAttempt) {
      const body = new FormData();
      body.append(
        "metadata",
        JSON.stringify({
          book_id: props.book.id,
          kind: kind.value,
          name: name.value.trim(),
          description: description.value,
          source: source.value,
          rights: rights.value,
        }),
      );
      body.append("file", file.value);
      uploadAttempt = { body, operation: newOperationID() };
    }
    const response = await fetch(`/api${root}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Idempotency-Key": uploadAttempt.operation },
      body: uploadAttempt.body,
    });
    const result = await response.json();
    if (!response.ok)
      throw new APIError(
        result.error ?? "图片上传未完成，输入已保留。",
        response.status,
      );
    if (!alive) return;
    discardUpload();
    notice.value =
      "已保存为参考图片，可继续用文字或Agent完善；尚未采用为正式参考。";
  } catch (e) {
    showError(e);
  } finally {
    if (alive) busy.value = false;
  }
  if (!error.value && alive) await load();
}
async function candidate(item: ImageItem) {
  if (busy.value || !branch.value) return;
  busy.value = true;
  error.value = "";
  try {
    const value = await api<{ id: string }>(
      `${root}/${item.id}/candidate`,
      "POST",
      {
        expected_ip_head: branch.value.head,
        operation_id: `novel-image-candidate-${item.id}-${branch.value.head}`,
      },
    );
    if (alive) releases.value[item.id] = value.id;
  } catch (e) {
    showError(e);
  } finally {
    if (alive) busy.value = false;
  }
}
async function adopt(item: ImageItem) {
  const release = releases.value[item.id];
  if (busy.value || !release || !canPublish.value) return;
  busy.value = true;
  error.value = "";
  try {
    await api(`${projectRoot}/releases/${release}/adopt`, "POST", {});
    await api(`${projectRoot}/releases/${release}/publish`, "POST", {});
    if (!alive) return;
    delete releases.value[item.id];
    notice.value =
      "已确认采用，图片与来源版本保留；小说正文和正史事实未被修改。";
  } catch (e) {
    showError(e);
  } finally {
    if (alive) busy.value = false;
  }
  if (!error.value && alive) await load();
}
onMounted(() => void load());
onBeforeUnmount(() => {
  alive = false;
});
</script>

<template>
  <section class="novel-images">
    <header>
      <div>
        <p>{{ book.setup.title }}</p>
        <h1>封面与场景图</h1>
      </div>
      <button :disabled="blocked" @click="emit('close')">返回小说工作台</button>
    </header>
    <p>
      先选择图片用途、角色与参考资料，再完善视觉要求。已确认的世界观和全书规划自动作为依据，候选需你确认后采用。
    </p>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <button :disabled="blocked" @click="load()">刷新本书图片与资料</button>
    <fieldset :disabled="busy || generatorBlocked || uploadDirty">
      <legend>图片方案</legend>
      <label
        >用途<select v-model="kind" aria-label="用途">
          <option value="cover">小说封面</option>
          <option value="scene">场景参考图</option>
        </select></label
      >
      <label
        >方案名称<input
          v-model="name"
          maxlength="120"
          placeholder="例如：雨夜钟楼"
      /></label>
      <label
        >出场角色（最多12位）<select
          v-model="characterIDs"
          multiple
          aria-label="出场角色（最多12位）"
        >
          <option v-for="c in characters" :key="c.id" :value="c.id">
            {{ c.name }} · {{ c.id.slice(0, 8) }}
          </option>
        </select></label
      >
      <label
        >参考资料（最多12项）<select
          v-model="sourceIDs"
          multiple
          aria-label="参考资料（最多12项）"
        >
          <option v-for="r in resources" :key="r.id" :value="r.id">
            {{ r.name }}
          </option>
        </select></label
      >
      <button @click="openScheme">进入图片方案</button>
    </fieldset>
    <CharacterImageGenerator
      v-if="selected && branch"
      :project-id="project.id"
      :branch-id="book.branch_id"
      :branch-head="branch.head"
      character-id=""
      :novel="selected"
      :images="references"
      :disabled="busy || uploadDirty"
      @blocking="generatorBlocked = $event"
      @saved="load()"
    />
    <form @submit.prevent="upload">
      <fieldset :disabled="busy || generatorBlocked">
        <legend>上传本方案参考图</legend>
        <label
          >PNG / JPEG / WebP（16MB以内）<input
            ref="fileInput"
            type="file"
            accept="image/png,image/jpeg,image/webp"
            required
            @change="file = ($event.target as HTMLInputElement).files?.[0]"
        /></label>
        <label
          >画面说明<textarea v-model="description" required maxlength="4000" />
        </label>
        <label>来源<input v-model="source" required maxlength="1000" /></label>
        <label
          >使用许可<input v-model="rights" required maxlength="1000"
        /></label>
        <button type="submit" :disabled="!file">保存参考图</button
        ><button type="button" @click="discardUpload">清空上传输入</button>
      </fieldset>
    </form>
    <h2>本书图片库</h2>
    <p>已采用版本保留；新的候选不会覆盖旧图。角色参考图在角色卡中维护。</p>
    <div class="gallery">
      <article v-for="item in gallery" :key="item.id">
        <h3>{{ visualLabel(item) }}</h3>
        <a :href="`/api${root}/${item.id}/file`" target="_blank" rel="noopener"
          ><img
            :src="`/api${root}/${item.id}/file`"
            :alt="visualLabel(item)"
            loading="lazy"
        /></a>
        <p>{{ item.published ? "已采用参考" : "待采用" }}</p>
        <p v-if="item.needs_review" role="status">{{ item.needs_review }}</p>
        <template v-if="!item.published"
          ><button
            v-if="!releases[item.id]"
            :disabled="blocked"
            @click="candidate(item)"
          >
            准备采用这张图片</button
          ><template v-else
            ><p>已准备候选，请核对图片与方案名称后确认。</p>
            <button :disabled="blocked || !canPublish" @click="adopt(item)">
              确认采用图片
            </button>
            <p v-if="!canPublish">需要项目所有者确认发布。</p></template
          ></template
        >
      </article>
    </div>
    <p v-if="!gallery.length && !busy">
      还没有封面或场景图。可以上传参考或准备AI生成任务。
    </p>
    <button v-if="cursor" :disabled="blocked" @click="load(true)">
      加载更多图片
    </button>
  </section>
</template>

<style scoped>
.novel-images {
  display: grid;
  gap: 1rem;
}
header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 1rem;
}
fieldset {
  display: grid;
  gap: 1rem;
  border: 1px solid #cad3df;
  padding: 1rem;
}
label {
  display: grid;
  gap: 0.4rem;
}
select[multiple] {
  min-height: 8rem;
}
.gallery {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
  gap: 1rem;
}
article {
  border: 1px solid #cad3df;
  border-radius: 8px;
  padding: 1rem;
}
img {
  width: 100%;
  max-height: 320px;
  object-fit: contain;
}
.error {
  color: #a32121;
}
</style>
