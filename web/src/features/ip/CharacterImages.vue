<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { api, message, newOperationID } from "../../api";
import { characterViewSlots } from "./characterViews";
import CharacterImageGenerator from "./CharacterImageGenerator.vue";

const props = defineProps<{
  projectId: string;
  branchId: string;
  branchHead: string;
  characterId: string;
  canEdit: boolean;
  canPublish: boolean;
  disabled: boolean;
}>();
const emit = defineEmits<{
  blocking: [value: boolean];
  portrait: [url: string];
}>();
interface ImageMetadata {
  character_id: string;
  look: string;
  view: string;
  description: string;
  source: string;
  rights: string;
  mime: string;
  width: number;
  height: number;
}
interface ImageItem {
  id: string;
  name: string;
  hash: string;
  published: boolean;
  report: { character_image: ImageMetadata };
}
const views: Record<string, string> = {
  portrait: "头像",
  front: "正面",
  side: "侧面",
  back: "背面",
  three_view: "组合三视图",
  expression: "表情表",
  outfit: "服装",
  prop: "特色道具",
};
const root = computed(
  () =>
    `/projects/${props.projectId}/branches/${props.branchId}/character-images`,
);
const items = ref<ImageItem[]>([]);
const cursor = ref("");
const busy = ref(false);
const generatingInput = ref(false);
const error = ref("");
const notice = ref("");
const selectedLook = ref("");
const comparisonSelection = ref<Record<string, string>>({});
const comparisonSlots = computed(() =>
  characterViewSlots(items.value, props.characterId, selectedLook.value).map(
    (slot) => ({
      ...slot,
      selected:
        slot.images.find(
          (image) => image.id === comparisonSelection.value[slot.view],
        ) ?? slot.images[0],
    }),
  ),
);
watch(
  () => [
    props.projectId,
    props.branchId,
    props.characterId,
    selectedLook.value,
  ],
  () => {
    comparisonSelection.value = {};
  },
);
const look = ref("日常造型");
const view = ref("front");
const description = ref("");
const source = ref("");
const rights = ref("");
const file = ref<File>();
const fileInput = ref<HTMLInputElement>();
const lightbox = ref<ImageItem>();
const failed = ref<Record<string, number>>({});
const imageErrors = ref<Record<string, boolean>>({});
let generation = 0;
let alive = true;
let uploadAttempt: { body: FormData; operation: string } | undefined;
const candidateAttempts = new Map<
  string,
  { operation_id: string; expected_ip_head: string; character_source: unknown }
>();
const releases = ref<Record<string, string>>({});
const pending = computed(
  () =>
    generatingInput.value ||
    busy.value ||
    !!file.value ||
    !!description.value ||
    !!source.value ||
    !!rights.value,
);
watch(pending, (value) => emit("blocking", value), { immediate: true });
const looks = computed(() => [
  ...new Set(items.value.map((item) => item.report.character_image.look)),
]);
const visible = computed(() =>
  items.value.filter(
    (item) =>
      !selectedLook.value ||
      item.report.character_image.look === selectedLook.value,
  ),
);
const missing = computed(() => {
  if (!selectedLook.value)
    return "选择一个造型，检查正面、侧面和背面是否齐全。";
  const approved = visible.value
    .filter((item) => item.published)
    .map((item) => item.report.character_image.view);
  if (approved.includes("three_view"))
    return "已有采用的组合三视图；角色、服装和道具是否一致仍需人工核对。";
  const missingViews = ["front", "side", "back"].filter(
    (slot) => !approved.includes(slot),
  );
  return missingViews.length
    ? `已采用三视图缺少：${missingViews.map((slot) => views[slot]).join("、")}`
    : "正侧背参考齐全；角色、服装和道具是否一致仍需人工核对。";
});
function url(item: ImageItem) {
  return `/api${root.value}/${item.id}/file?retry=${failed.value[item.id] ?? 0}`;
}
async function load(more = false) {
  const ticket = ++generation;
  error.value = "";
  try {
    const result = await api<{ items: ImageItem[]; next: string }>(
      `${root.value}?character=${encodeURIComponent(props.characterId)}&after=${encodeURIComponent(more ? cursor.value : "")}`,
    );
    if (!alive || ticket !== generation) return;
    items.value = more ? [...items.value, ...result.items] : result.items;
    cursor.value = result.next;
    if (!selectedLook.value || !looks.value.includes(selectedLook.value))
      selectedLook.value = looks.value[0] ?? "";
    const portrait = items.value.find(
      (item) =>
        item.published && item.report.character_image.view === "portrait",
    );
    if (portrait) emit("portrait", url(portrait));
  } catch (e) {
    if (alive && ticket === generation) error.value = message(e);
  }
}
watch(
  () => [props.projectId, props.branchId, props.characterId],
  () => {
    items.value = [];
    cursor.value = "";
    selectedLook.value = "";
    releases.value = {};
    candidateAttempts.clear();
    void load();
  },
  { immediate: true },
);
watch(
  [file, look, view, description, source, rights],
  () => {
    uploadAttempt = undefined;
  },
  { flush: "sync" },
);
onUnmounted(() => {
  alive = false;
  generation++;
  emit("blocking", false);
});
function clearInput() {
  file.value = undefined;
  description.value = "";
  source.value = "";
  rights.value = "";
  uploadAttempt = undefined;
  if (fileInput.value) fileInput.value.value = "";
}
async function upload() {
  if (busy.value || props.disabled || !props.canEdit || !file.value) return;
  if (!uploadAttempt) {
    const body = new FormData();
    body.append(
      "metadata",
      JSON.stringify({
        character_id: props.characterId,
        look: look.value,
        view: view.value,
        description: description.value,
        source: source.value,
        rights: rights.value,
      }),
    );
    body.append("file", file.value);
    uploadAttempt = { body, operation: newOperationID() };
  }
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    const response = await fetch(`/api${root.value}`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Idempotency-Key": uploadAttempt.operation },
      body: uploadAttempt.body,
    });
    const data = await response.json();
    if (!response.ok)
      throw new Error(data.error ?? "图片保存失败，可保留原输入重试");
    clearInput();
    await load();
    selectedLook.value = data.report.character_image.look;
    notice.value = "图片已保存为待采用参考。尚未改变正式角色或已确认造型。";
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}
async function prepare(item: ImageItem) {
  if (busy.value || props.disabled || !props.canEdit) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    let input = candidateAttempts.get(item.id);
    if (!input) {
      const catalog = await api<{
        items: {
          ref: {
            id: string;
            branch_id: string;
            revision: string;
            kind: string;
          };
        }[];
      }>(`/projects/${props.projectId}/dependencies/catalog`);
      const entity = catalog.items.find(
        ({ ref }) =>
          ref.id === props.characterId &&
          ref.branch_id === props.branchId &&
          ref.revision === props.branchHead &&
          ref.kind === "entity",
      );
      if (!entity)
        throw new Error(
          "请先确认并发布角色设定，再采用这张图片。已上传的图片会保留。",
        );
      input = {
        operation_id: `character-image-candidate-${item.id}-${props.branchHead}`,
        expected_ip_head: props.branchHead,
        character_source: entity.ref,
      };
      candidateAttempts.set(item.id, input);
    }
    const release = await api<{ id: string }>(
      `${root.value}/${item.id}/candidate`,
      "POST",
      input,
    );
    releases.value[item.id] = release.id;
    notice.value =
      "视觉候选已保存。核对图片、来源与角色设定后，可明确确认采用。";
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}
async function approve(item: ImageItem) {
  const release = releases.value[item.id];
  if (
    !release ||
    busy.value ||
    props.disabled ||
    !props.canEdit ||
    !props.canPublish
  )
    return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await api(
      `/projects/${props.projectId}/releases/${release}/adopt`,
      "POST",
      {},
    );
    await api(
      `/projects/${props.projectId}/releases/${release}/publish`,
      "POST",
      {},
    );
    await load();
    notice.value =
      "图片已确认采用，保留独立图片版本与角色来源版本；旧图片仍可查阅。";
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}
</script>

<template>
  <section class="character-images" aria-label="角色视觉资料">
    <h4>角色形象与三视图</h4>
    <p class="muted">
      按造型分别管理参考图。上传后先预览，确认采用才成为正式参考；人物仍使用当前角色
      ID。
    </p>
    <p v-if="error" role="alert">
      {{ error }}
      <button type="button" :disabled="busy" @click="load()">
        重新加载图片
      </button>
    </p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <CharacterImageGenerator
      v-if="canEdit"
      :key="`${projectId}:${branchId}:${characterId}`"
      :project-id="projectId"
      :branch-id="branchId"
      :branch-head="branchHead"
      :character-id="characterId"
      :disabled="disabled || busy"
      :images="items"
      @saved="load()"
      @blocking="generatingInput = $event"
    />
    <label
      >查看造型<select v-model="selectedLook" aria-label="查看造型">
        <option value="">全部造型</option>
        <option v-for="name in looks" :key="name">{{ name }}</option>
      </select></label
    >
    <p role="status">{{ missing }}</p>
    <section v-if="selectedLook" aria-label="三视图并排对照">
      <h5>{{ selectedLook }} · 已采用参考对照</h5>
      <p class="muted">
        可选择各视角的参考版本并放大查看。并排展示不代表服装、道具和外貌已确认一致；组合三视图在下方图库查看。
      </p>
      <p v-if="cursor" role="status">
        以下仅包含已加载图片；其他参考可能尚未加载，请使用下方“加载更多图片”。
      </p>
      <div class="view-comparison">
        <article
          v-for="slot in comparisonSlots"
          :key="slot.view"
          :aria-label="`${slot.label}参考对照`"
          class="image-card"
        >
          <strong>{{ slot.label }}</strong>
          <template v-if="slot.selected">
            <label v-if="slot.images.length > 1"
              >{{ slot.label }}参考版本
              <select
                :value="slot.selected.id"
                @change="
                  comparisonSelection[slot.view] = (
                    $event.target as HTMLSelectElement
                  ).value
                "
              >
                <option
                  v-for="item in slot.images"
                  :key="item.id"
                  :value="item.id"
                >
                  {{ item.name }} · {{ item.id }}
                </option>
              </select>
            </label>
            <button
              v-if="!imageErrors[slot.selected.id]"
              type="button"
              class="image-preview"
              :aria-label="`对照放大${slot.label} ${slot.selected.name}`"
              @click="lightbox = slot.selected"
            >
              <img
                :src="url(slot.selected)"
                :alt="`${selectedLook} ${slot.label}对照`"
                loading="lazy"
                @error="imageErrors[slot.selected.id] = true"
              />
            </button>
            <button
              v-else
              type="button"
              @click="
                failed[slot.selected.id] = (failed[slot.selected.id] ?? 0) + 1;
                imageErrors[slot.selected.id] = false;
              "
            >
              重试{{ slot.label }}参考
            </button>
            <small>{{ slot.selected.name }}</small>
          </template>
          <p v-else class="missing-view">{{ slot.label }}参考待补充</p>
        </article>
      </div>
    </section>
    <p v-if="!items.length" class="muted">
      尚无图片，可以先上传参考图。未填的视角不会用其他图片冒充。
    </p>
    <div class="image-grid">
      <article v-for="item in visible" :key="item.id" class="image-card">
        <button
          v-if="!imageErrors[item.id]"
          class="image-preview"
          type="button"
          :aria-label="`放大${views[item.report.character_image.view]} ${item.name}`"
          @click="lightbox = item"
        >
          <img
            :src="url(item)"
            :alt="`${item.report.character_image.look} · ${views[item.report.character_image.view]}`"
            loading="lazy"
            @error="imageErrors[item.id] = true"
          />
        </button>
        <button
          v-else
          type="button"
          @click="
            failed[item.id] = (failed[item.id] ?? 0) + 1;
            imageErrors[item.id] = false;
          "
        >
          图片加载失败，点击重试
        </button>
        <strong
          >{{ views[item.report.character_image.view] }} ·
          {{ item.report.character_image.look }}</strong
        >
        <span class="pill">{{ item.published ? "已采用参考" : "待采用" }}</span>
        <p>{{ item.report.character_image.description || "尚无造型说明" }}</p>
        <small
          >{{ item.report.character_image.width }} ×
          {{ item.report.character_image.height }} · {{ item.name }}</small
        >
        <details>
          <summary>来源与版本</summary>
          <p>来源：{{ item.report.character_image.source }}</p>
          <p>使用权限：{{ item.report.character_image.rights }}</p>
          <p>图片 ID：{{ item.id }}</p>
          <code>{{ item.hash }}</code>
        </details>
        <template v-if="canEdit && !item.published">
          <button
            v-if="!releases[item.id]"
            type="button"
            :disabled="busy || disabled"
            @click="prepare(item)"
          >
            保存视觉候选
          </button>
          <button
            v-else-if="canPublish"
            type="button"
            :disabled="busy || disabled"
            @click="approve(item)"
          >
            确认采用这张图片
          </button>
          <p v-else>视觉候选已保存，等待有发布权限的用户确认。</p>
        </template>
      </article>
    </div>
    <button v-if="cursor" type="button" :disabled="busy" @click="load(true)">
      加载更多图片
    </button>
    <form v-if="canEdit" @submit.prevent="upload">
      <fieldset :disabled="busy || disabled">
        <legend>上传角色参考图</legend>
        <label
          >造型名称<input
            v-model="look"
            required
            maxlength="120"
            placeholder="例如：日常青衣、雨夜战损"
        /></label>
        <label
          >图片用途<select v-model="view">
            <option v-for="(name, key) in views" :key="key" :value="key">
              {{ name }}
            </option>
          </select></label
        >
        <label
          >图片文件<input
            ref="fileInput"
            type="file"
            accept="image/png,image/jpeg,image/webp"
            required
            @change="file = ($event.target as HTMLInputElement).files?.[0]"
        /></label>
        <small
          >PNG/JPEG/WebP，最大16MB、2400万像素。正面、侧面、背面可分别上传，也可上传组合三视图。</small
        >
        <label
          >造型说明<textarea
            v-model="description"
            maxlength="4000"
            placeholder="服装、发型、配色、标志及特色道具"
          />
        </label>
        <label
          >图片来源<textarea
            v-model="source"
            required
            maxlength="1000"
            placeholder="本人绘制、生成工具及提示词，或图片来源"
          />
        </label>
        <label
          >使用权限说明<textarea
            v-model="rights"
            required
            maxlength="1000"
            placeholder="如：本人原创；未知时填写待核实"
          />
        </label>
        <div class="image-actions">
          <button type="submit" :disabled="!file">
            {{ busy ? "处理中…" : "上传并保存待采用参考" }}</button
          ><button type="button" @click="clearInput">清空上传输入</button>
        </div>
      </fieldset>
    </form>
    <dialog
      :open="!!lightbox"
      class="image-lightbox"
      aria-label="图片大图"
      @keydown.esc="lightbox = undefined"
    >
      <template v-if="lightbox"
        ><button type="button" autofocus @click="lightbox = undefined">
          关闭大图</button
        ><img
          :src="url(lightbox)"
          :alt="lightbox.report.character_image.description || lightbox.name"
      /></template>
    </dialog>
  </section>
</template>

<style scoped>
.character-images {
  border-top: 1px solid var(--line, #d6dcd5);
  margin-top: 1.5rem;
  padding-top: 1rem;
}
.image-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 1rem;
  margin-block: 1rem;
}
.view-comparison {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0.8rem;
}
.view-comparison select {
  max-width: 100%;
}
.missing-view {
  display: grid;
  place-items: center;
  min-height: 220px;
  background: #edf0e8;
  color: #56675a;
  margin: 0;
}
@media (max-width: 640px) {
  .view-comparison {
    grid-template-columns: 1fr;
  }
}
.image-card {
  display: flex;
  flex-direction: column;
  gap: 0.5rem;
  border: 1px solid var(--line, #d6dcd5);
  padding: 0.8rem;
  border-radius: 12px;
  overflow-wrap: anywhere;
}
.image-preview {
  padding: 0;
  background: #edf0e8;
  border: 0;
}
.image-preview img {
  width: 100%;
  height: 220px;
  object-fit: contain;
  display: block;
}
fieldset {
  display: grid;
  gap: 0.7rem;
}
.image-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.7rem;
}
.image-lightbox[open] {
  position: fixed;
  inset: 2vh 2vw;
  margin: auto;
  z-index: 100;
  max-width: 94vw;
  max-height: 94vh;
  overflow: auto;
  background: #fff;
  box-shadow: 0 0 0 100vmax #0008;
  border: 0;
}
.image-lightbox img {
  display: block;
  max-width: 85vw;
  max-height: 80vh;
  object-fit: contain;
}
</style>
