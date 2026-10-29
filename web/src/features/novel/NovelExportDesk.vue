<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import { api, APIError, message, newOperationID } from "../../api";
import type { NovelBook } from "./types";
import type { VisualImageChoice } from "../ip/visualTypes";
import type { ManuscriptPreview, SavedEdition } from "./manuscriptTypes";
import NovelEditionCompare from "./NovelEditionCompare.vue";
const props = defineProps<{ book: NovelBook; canSaveEdition?: boolean }>();
const emit = defineEmits<{ close: []; expired: []; edit: [id: string] }>();
const source = ref("draft"),
  editionID = ref(""),
  coverImage = ref(""),
  volume = ref(""),
  author = ref(""),
  language = ref(
    props.book.setup.language.includes("繁") ? "zh-Hant" : "zh-Hans",
  ),
  description = ref(props.book.setup.idea);
const preview = ref<ManuscriptPreview | null>(null),
  catalog = ref<{ id: string; title: string }[]>([]);
const incomplete = ref(false),
  busy = ref(false),
  error = ref(""),
  notice = ref("");
const needsConsent = computed(
  () =>
    !!preview.value &&
    (preview.value.missing > 0 || preview.value.warnings > 0),
);
const ready = computed(
  () =>
    preview.value &&
    preview.value.included > 0 &&
    (source.value === "edition" || author.value.trim()) &&
    (!needsConsent.value || incomplete.value),
);
const root = `/novels/${props.book.id}/export`;
interface Edition extends SavedEdition {
  hash: string;
  author: string;
  included: number;
  missing: number;
  created_at: string;
}
const editions = ref<Edition[]>([]),
  editionCursor = ref(""),
  editionsBusy = ref(false),
  editionsError = ref("");
const editionName = ref(""),
  editionOperation = ref("");
async function loadEditions(more = false) {
  if (editionsBusy.value) return;
  editionsBusy.value = true;
  editionsError.value = "";
  try {
    const value = await api<{ items: Edition[]; next: string }>(
      `${root}/editions${more && editionCursor.value ? `?after=${encodeURIComponent(editionCursor.value)}` : ""}`,
    );
    if (!active) return;
    editions.value = (
      more ? [...editions.value, ...value.items] : value.items
    ).sort((a, b) => b.created_at.localeCompare(a.created_at));
    editionCursor.value = value.next;
  } catch (e) {
    if (active) {
      editionsError.value = message(e);
      if (e instanceof APIError && [401, 403].includes(e.status)) {
        editions.value = [];
        editionCursor.value = "";
      }
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (active) editionsBusy.value = false;
  }
}
async function saveEdition(resume?: Edition) {
  if (
    busy.value ||
    !props.canSaveEdition ||
    (!resume &&
      (!ready.value ||
        source.value !== "published" ||
        !editionName.value.trim()))
  )
    return;
  busy.value = true;
  error.value = "";
  try {
    if (!resume && !editionOperation.value)
      editionOperation.value = newOperationID();
    const value = resume
      ? await api<Edition>(`${root}/editions/${resume.id}/confirm`, "POST", {
          hash: resume.hash,
          accept: true,
        })
      : await api<Edition>(`${root}/editions`, "POST", {
          name: editionName.value,
          operation_id: editionOperation.value,
          accept: true,
          export: {
            ...input(),
            preview_hash: preview.value!.hash,
            allow_incomplete: incomplete.value,
          },
        });
    if (!active) return;
    editionID.value = value.id;
    source.value = "edition";
    await loadEditions();
    await inspect();
    if (active)
      notice.value =
        "成书版本已保存。以后改稿不会改变这个版本，可从历史列表重新导出。";
  } catch (e) {
    fail(e);
    if (active) void loadEditions();
  } finally {
    if (active) busy.value = false;
  }
}
watch(editionName, () => {
  editionOperation.value = "";
});
interface CoverChoice extends VisualImageChoice {
  published: boolean;
  needs_review?: string;
}
const covers = ref<CoverChoice[]>([]),
  coverCursor = ref(""),
  coversBusy = ref(false),
  coversError = ref("");
const coverFailed = ref(false),
  coverRetry = ref(0);
async function loadCovers(more = false) {
  if (coversBusy.value) return;
  coversBusy.value = true;
  coversError.value = "";
  try {
    const data = await api<{ items: CoverChoice[]; next: string }>(
      `/novels/${props.book.id}/images${more && coverCursor.value ? `?after=${encodeURIComponent(coverCursor.value)}` : ""}`,
    );
    if (!active) return;
    const eligible = data.items.filter(
      (i) =>
        i.published &&
        i.report.novel_image?.kind === "cover" &&
        i.report.novel_image.book_id === props.book.id,
    );
    covers.value = more ? [...covers.value, ...eligible] : eligible;
    coverCursor.value = data.next;
  } catch (e) {
    if (active) {
      coversError.value = message(e);
      if (e instanceof APIError && e.status === 401) emit("expired");
    }
  } finally {
    if (active) coversBusy.value = false;
  }
}
const controller = new AbortController();
let active = true;
onBeforeUnmount(() => {
  active = false;
  controller.abort();
});
watch(
  [source, volume, author, language, description, coverImage, editionID],
  () => {
    preview.value = null;
    incomplete.value = false;
    notice.value = "";
    coverFailed.value = false;
    editionOperation.value = "";
  },
  { flush: "sync" },
);
function input() {
  if (source.value === "edition")
    return { source: "edition", edition_id: editionID.value };
  return {
    source: source.value,
    volume_id: volume.value,
    author: author.value,
    language: language.value,
    description: description.value,
    cover_image_id: coverImage.value,
  };
}
function fail(e: unknown) {
  if (!active) return;
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function inspect() {
  if (source.value === "edition" && !editionID.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    const value = await api<ManuscriptPreview>(
      `${root}/preview`,
      "POST",
      input(),
    );
    if (!active) return;
    preview.value = value;
    incomplete.value = false;
    if (!volume.value)
      catalog.value = Array.from(
        new Map(
          value.chapters.map((c) => [
            c.volume_id,
            { id: c.volume_id, title: c.volume },
          ]),
        ).values(),
      );
  } catch (e) {
    fail(e);
  } finally {
    if (active) busy.value = false;
  }
}
async function download(format: "epub" | "txt" | "pdf" | "md") {
  if (!preview.value || !ready.value || busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    const response = await fetch(`/api${root}/download`, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...input(),
        format,
        preview_hash: preview.value.hash,
        allow_incomplete: incomplete.value,
      }),
      signal: controller.signal,
    });
    if (!response.ok) {
      const body = await response.json();
      if (response.status === 409) preview.value = null;
      throw new APIError(
        body.error ?? body.message ?? "导出失败，请重新预览",
        response.status,
      );
    }
    const blob = await response.blob();
    if (!active) return;
    const url = URL.createObjectURL(blob),
      a = document.createElement("a");
    a.href = url;
    const suffix =
      source.value === "edition"
        ? `-${editions.value.find((e) => e.id === editionID.value)?.name ?? "历史稿本"}`
        : volume.value
          ? "-分卷"
          : "";
    a.download = `${(preview.value.title + suffix).replace(/[<>:"/\\|?*\u0000-\u001f]/g, "_")}.${format}`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 10000);
    notice.value = "已生成下载文件。原稿和正式版本保持原样。";
  } catch (e) {
    fail(e);
  } finally {
    if (active) busy.value = false;
  }
}
onMounted(() => {
  void inspect();
  void loadCovers();
  void loadEditions();
});
</script>

<template>
  <section class="novel-export" aria-label="整书与分卷导出">
    <header class="section-heading">
      <div>
        <p class="eyebrow">{{ book.setup.title }}</p>
        <h1>整书与分卷导出</h1>
      </div>
      <button :disabled="busy" @click="emit('close')">返回小说工作台</button>
    </header>
    <p>
      按已确认的大纲顺序整理正文。先保存章节修改，再检查导出范围。导出不会替你发布作品。
    </p>
    <fieldset class="panel export-options" :disabled="busy">
      <legend>导出内容与书籍信息</legend>
      <label
        >正文来源<select v-model="source" aria-label="导出正文来源">
          <option value="draft">我的私人稿（无私人稿时用已采用稿）</option>
          <option value="published">仅已正式采用稿</option>
          <option value="edition">已保存的成书版本</option>
        </select></label
      >
      <template v-if="source === 'edition'">
        <label class="full"
          >历史成书版本<select
            v-model="editionID"
            aria-label="历史成书版本"
            :disabled="editionsBusy"
          >
            <option value="">请选择版本</option>
            <option
              v-for="e in editions.filter((v) => v.published)"
              :key="e.id"
              :value="e.id"
            >
              {{ e.name }} · {{ e.title }} · {{ e.included }}章 ·
              {{ e.created_at.slice(0, 10) }}
            </option>
          </select></label
        >
        <p class="muted full">
          使用保存时的目录、正文、署名、简介和封面，不跟随当前改稿。历史版本保持原样；新修改请回到当前稿另存版本。
        </p>
        <p v-if="editionsError" class="full" role="alert">
          {{ editionsError }}
        </p>
        <button :disabled="editionsBusy" @click="loadEditions()">
          刷新历史版本
        </button>
        <button
          v-if="editionCursor"
          :disabled="editionsBusy"
          @click="loadEditions(true)"
        >
          加载更多成书版本
        </button>
        <div
          v-for="e in editions.filter((v) => !v.published)"
          :key="e.id"
          class="full"
        >
          {{ e.name }} · 上次确认的稿本尚未保存完成
          <button v-if="canSaveEdition" @click="saveEdition(e)">
            继续保存此稿本
          </button>
        </div>
      </template>
      <template v-else>
        <label
          >范围<select v-model="volume" aria-label="导出范围">
            <option value="">整书</option>
            <option v-for="v in catalog" :key="v.id" :value="v.id">
              {{ v.title }}
            </option>
          </select></label
        >
        <label
          >作者署名<input
            v-model="author"
            aria-label="导出作者署名"
            maxlength="120"
            placeholder="填写作者名或笔名"
        /></label>
        <label
          >电子书语言<select v-model="language" aria-label="电子书语言">
            <option value="zh-Hans">简体中文</option>
            <option value="zh-Hant">繁体中文</option>
            <option value="en">英语</option>
            <option value="ja">日语</option>
            <option value="ko">韩语</option>
          </select></label
        >
        <label class="full"
          >简介<textarea
            v-model="description"
            aria-label="导出简介"
            maxlength="4000"
            rows="3"
          />
        </label>
        <p class="muted full">
          署名、语言和简介仅用于本次导出。文件不包含角色秘密、剧情账本或模型提示词。
        </p>
        <label class="full"
          >封面版本
          <select
            v-model="coverImage"
            aria-label="导出封面版本"
            :disabled="coversBusy"
          >
            <option value="">不带封面图片</option>
            <option v-for="c in covers" :key="c.id" :value="c.id">
              {{ c.report.novel_image?.name }} · {{ c.id.slice(0, 8)
              }}{{ c.needs_review ? " · 待复核" : "" }}
            </option>
          </select>
        </label>
        <p class="muted full">
          仅列出本书已采用的封面。可在“封面与场景图”中生成或上传并确认采用；TXT
          和 Markdown 为文字稿，不嵌入图片。
        </p>
        <p v-if="coversError" class="full" role="alert">
          封面列表：{{ coversError }}
        </p>
        <button
          v-if="coversError"
          :disabled="coversBusy"
          @click="loadCovers(!!coverCursor)"
        >
          重试读取封面
        </button>
        <button
          v-if="coverCursor"
          :disabled="coversBusy"
          @click="loadCovers(true)"
        >
          继续查找更多已采用封面
        </button>
      </template>
      <button :disabled="source === 'edition' && !editionID" @click="inspect">
        检查导出内容
      </button>
    </fieldset>
    <p v-if="error" role="alert">{{ error }}</p>
    <p v-if="notice" role="status">{{ notice }}</p>
    <section v-if="preview" class="panel" aria-label="导出内容预览">
      <div v-if="preview.edition_metadata" aria-label="历史书籍信息">
        <p>
          {{ preview.title }} · {{ preview.edition_metadata.author }} ·
          {{ preview.edition_metadata.language }}
        </p>
        <p class="edition-description">
          {{ preview.edition_metadata.description }}
        </p>
      </div>
      <figure v-if="preview.cover" class="export-cover">
        <img
          :key="`${preview.cover.image_id}-${coverRetry}`"
          :src="
            preview.edition_id
              ? `/api${root}/editions/${preview.edition_id}/cover`
              : `/api/novels/${props.book.id}/images/${preview.cover.image_id}/file`
          "
          alt="本次导出的已采用封面"
          @error="coverFailed = true"
          @load="coverFailed = false"
        />
        <figcaption>
          {{ preview.cover.name }} · {{ preview.cover.width }} ×
          {{ preview.cover.height }} · 已采用
        </figcaption>
        <details>
          <summary>查看封面版本详情</summary>
          <p>
            采用版本 {{ preview.cover.release_id }}<br />原图校验
            {{ preview.cover.hash }}
          </p>
        </details>
        <p v-if="preview.cover.warning" role="status">
          {{ preview.cover.warning }}
        </p>
        <button v-if="coverFailed" :disabled="busy" @click="coverRetry++">
          重试显示封面
        </button>
      </figure>
      <h2>
        {{ preview.edition_id ? `${preview.title} · 历史目录` : "本次目录" }}
      </h2>
      <p>
        可导出 {{ preview.included }} 章，共
        {{ preview.characters.toLocaleString() }} 个字符；缺少
        {{ preview.missing }} 章，{{ preview.warnings }} 项提示。
      </p>
      <p class="muted">
        这是正文与版本检查，不能代替跨章逻辑、人物发展和伏笔回收的整卷审稿。
      </p>
      <ol class="export-chapters">
        <li v-for="c in preview.chapters" :key="c.id">
          <span
            >{{ c.volume }} / {{ c.title }} ·
            {{ c.included ? `${c.characters} 字符` : "未纳入" }}</span
          ><strong v-if="c.warning">{{ c.warning }}</strong
          ><button
            v-if="c.resource_id && !preview.edition_id"
            :disabled="busy"
            @click="emit('edit', c.id)"
          >
            查看本章
          </button>
        </li>
      </ol>
      <label v-if="needsConsent"
        ><input
          v-model="incomplete"
          type="checkbox"
          :disabled="busy"
        />我已核对缺章与提示，导出当前可用稿，并在书籍信息中保留说明</label
      >
      <p v-if="source !== 'edition' && !author.trim()">
        下载前请填写作者署名。
      </p>
      <section
        v-if="source === 'published'"
        class="edition-save"
        aria-label="保存成书版本"
      >
        <p>
          保存这个已采用稿本后，可在日后按同一目录、正文和封面重新导出；不会重新发布章节或更改正史。
        </p>
        <template v-if="canSaveEdition">
          <label
            >版本名称<input
              v-model="editionName"
              aria-label="成书版本名称"
              maxlength="80"
              :disabled="busy"
              placeholder="例如：第一卷投稿版"
          /></label>
          <button
            :disabled="busy || !ready || !editionName.trim()"
            @click="saveEdition()"
          >
            确认保存成书版本
          </button>
        </template>
        <p v-else>保存成书版本需要作品发布权限；仍可下载当前稿。</p>
      </section>
      <div class="export-actions">
        <button :disabled="busy || !ready" @click="download('pdf')">
          下载 PDF 排版稿
        </button>
        <button :disabled="busy || !ready" @click="download('epub')">
          下载 EPUB 电子书</button
        ><button :disabled="busy || !ready" @click="download('txt')">
          下载整合 TXT
        </button>
        <button :disabled="busy || !ready" @click="download('md')">
          下载 Markdown 文稿
        </button>
      </div>
    </section>
    <NovelEditionCompare
      :key="book.id"
      :book-id="book.id"
      :editions="editions"
      :disabled="busy"
      :loading="editionsBusy"
      :has-more="!!editionCursor"
      :load-error="editionsError"
      @more="loadEditions(true)"
      @refresh="loadEditions()"
      @expired="emit('expired')"
    />
  </section>
</template>

<style scoped>
.novel-export {
  max-width: 1100px;
  margin: 24px auto;
  padding: 20px;
}
.section-heading {
  display: flex;
  justify-content: space-between;
  gap: 16px;
  align-items: center;
}
.export-options {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 16px;
  margin: 20px 0;
}
.export-options label {
  display: grid;
  gap: 6px;
}
.full {
  grid-column: 1/-1;
}
.export-chapters {
  padding-left: 24px;
  max-height: 420px;
  overflow: auto;
}
.export-chapters li {
  padding: 12px 0;
  border-bottom: 1px solid #ddd;
}
.export-chapters strong {
  display: block;
  margin: 8px 0;
}
.export-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 12px;
  margin-top: 20px;
}
.export-cover {
  margin: 0 0 20px;
}
.edition-description {
  white-space: pre-wrap;
}
.export-cover img {
  display: block;
  max-width: 100%;
  width: 180px;
  max-height: 270px;
  object-fit: contain;
}
.export-cover figcaption,
.export-cover details {
  overflow-wrap: anywhere;
  margin-top: 8px;
  font-size: 0.85em;
}
textarea,
input,
select {
  max-width: 100%;
}
@media (max-width: 650px) {
  .export-options {
    grid-template-columns: 1fr;
  }
  .section-heading {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
