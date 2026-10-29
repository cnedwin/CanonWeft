<script setup lang="ts">
import { computed, ref, watch } from "vue";
import { compareProse, type ProseDifferenceBlock } from "./proseComparison";

const props = withDefaults(
  defineProps<{
    left: string;
    right: string;
    leftLabel?: string;
    rightLabel?: string;
  }>(),
  { leftLabel: "原正文", rightLabel: "新正文" },
);
const pageCharacters = 12_000;
const blockCharacters = 6_000;
const blocksPerPage = 24;
const leftPage = ref(0);
const rightPage = ref(0);
const differencePage = ref(0);
const showWhitespace = ref(true);
const comparison = computed(() => compareProse(props.left, props.right));

// Chunk text nodes, never characters. A boundary must not break a surrogate
// pair or CRLF, including in a single very long paragraph or fallback block.
function textChunks(text: string, limit: number): string[] {
  const chunks: string[] = [];
  let start = 0;
  while (start < text.length) {
    let end = Math.min(start + limit, text.length);
    const previous = text.charCodeAt(end - 1);
    const next = text.charCodeAt(end);
    if (
      end < text.length &&
      ((previous >= 0xd800 &&
        previous <= 0xdbff &&
        next >= 0xdc00 &&
        next <= 0xdfff) ||
        (previous === 13 && next === 10))
    ) {
      end--;
    }
    chunks.push(text.slice(start, end));
    start = end;
  }
  return chunks.length ? chunks : [""];
}

interface DifferenceFragment extends ProseDifferenceBlock {
  key: string;
  continued: boolean;
}

function differencePages(
  blocks: ProseDifferenceBlock[],
): DifferenceFragment[][] {
  const pages: DifferenceFragment[][] = [];
  let page: DifferenceFragment[] = [];
  let characters = 0;
  blocks.forEach((block, index) => {
    if (block.kind === "same") return;
    textChunks(block.text, blockCharacters).forEach((text, part) => {
      if (
        page.length &&
        (characters + text.length > pageCharacters ||
          page.length >= blocksPerPage)
      ) {
        pages.push(page);
        page = [];
        characters = 0;
      }
      page.push({
        ...block,
        text,
        key: `${index}-${part}`,
        continued: part > 0,
      });
      characters += text.length;
    });
  });
  if (page.length) pages.push(page);
  return pages;
}

const leftPages = computed(() => textChunks(props.left, pageCharacters));
const rightPages = computed(() => textChunks(props.right, pageCharacters));
const differences = computed(() => differencePages(comparison.value.blocks));
const sides = computed(() => [
  {
    key: "left" as const,
    label: props.leftLabel,
    pages: leftPages.value,
    page: leftPage.value,
    empty: !props.left,
  },
  {
    key: "right" as const,
    label: props.rightLabel,
    pages: rightPages.value,
    page: rightPage.value,
    empty: !props.right,
  },
]);

function changeOriginalPage(side: "left" | "right", next: number) {
  const page = side === "left" ? leftPage : rightPage;
  const pages = side === "left" ? leftPages : rightPages;
  page.value = Math.max(0, Math.min(pages.value.length - 1, next));
}

function selectOriginalPage(side: "left" | "right", event: Event) {
  const input = event.target as HTMLInputElement;
  const value = Number(input.value);
  if (Number.isInteger(value)) changeOriginalPage(side, value - 1);
}

function selectDifferencePage(event: Event) {
  const input = event.target as HTMLInputElement;
  const value = Number(input.value);
  if (Number.isInteger(value)) {
    differencePage.value = Math.max(
      0,
      Math.min(differences.value.length - 1, value - 1),
    );
  }
}

function displayedDifference(text: string): string {
  if (!showWhitespace.value) return text;
  const markers: Record<string, string> = {
    " ": "·",
    "\t": "⇥",
    "\u00a0": "⍽",
    "\r\n": "⟦CRLF⟧\n",
    "\r": "⟦CR⟧\n",
    "\n": "⟦LF⟧\n",
    "\u2028": "⟦LS⟧\n",
    "\u2029": "⟦PS⟧\n",
  };
  return text.replace(
    /\r\n|[ \t\u00a0\r\n\u2028\u2029]/g,
    (value) => markers[value]!,
  );
}

watch(
  () => [props.left, props.right],
  () => {
    leftPage.value = 0;
    rightPage.value = 0;
    differencePage.value = 0;
  },
  { flush: "sync" },
);
</script>

<template>
  <section class="prose-diff" aria-label="正文差异对照">
    <header>
      <h3>正文差异对照</h3>
      <p v-if="comparison.equal" role="status">
        正文完全相同，包含空白与换行。
      </p>
      <p v-else role="status">
        正文有变化：新增块共 {{ comparison.added.toLocaleString() }} 字符，
        移除块共 {{ comparison.removed.toLocaleString() }} 字符。
      </p>
      <p v-if="comparison.coarse" class="comparison-note">
        粗粒度对照：变化中段超过行数或计算上限，保留共同首尾后整块列出移除与新增内容。不能视为逐字差异。
      </p>
      <p v-else class="comparison-note">
        行级对照：按原始行匹配，统计变化行块中的 Unicode
        字符，包含空白与换行；不表示最少改动字数。
      </p>
    </header>

    <div class="original-columns" aria-label="两侧原始正文">
      <article v-for="side in sides" :key="side.key" :aria-label="side.label">
        <h4>{{ side.label }}</h4>
        <p v-if="side.empty" class="comparison-note">空正文</p>
        <pre class="original-text" :aria-label="`${side.label}原文`">{{
          side.pages[side.page]
        }}</pre>
        <nav v-if="side.pages.length > 1" :aria-label="`${side.label}正文分页`">
          <button
            class="quiet"
            :disabled="side.page === 0"
            @click="changeOriginalPage(side.key, side.page - 1)"
          >
            上一页
          </button>
          <label>
            第
            <input
              type="number"
              min="1"
              :max="side.pages.length"
              :value="side.page + 1"
              :aria-label="`${side.label}正文页码`"
              @change="selectOriginalPage(side.key, $event)"
            />
            / {{ side.pages.length }} 页
          </label>
          <button
            class="quiet"
            :disabled="side.page >= side.pages.length - 1"
            @click="changeOriginalPage(side.key, side.page + 1)"
          >
            下一页
          </button>
        </nav>
      </article>
    </div>
    <p class="comparison-note">
      两侧展示原始正文，长文按片段分页；可分别切换页码阅读完整内容。
    </p>

    <section v-if="!comparison.equal" aria-label="变化正文片段">
      <h4>变化行块</h4>
      <p class="comparison-note">
        - 为原正文移除块，+ 为新正文新增块。这里只列变化行块，上下文见两侧原文。
      </p>
      <label class="whitespace-toggle">
        <input v-model="showWhitespace" type="checkbox" />
        显示空白标记（空格 ·、制表符 ⇥、换行标记）
      </label>
      <div class="difference-blocks">
        <article
          v-for="block in differences[differencePage]"
          :key="block.key"
          class="difference-block"
          :class="block.kind"
          :aria-label="block.kind === 'removed' ? '移除正文块' : '新增正文块'"
        >
          <strong class="block-label">
            {{ block.kind === "removed" ? "- 移除" : "+ 新增" }}
            {{ block.continued ? "（续）" : "" }}
          </strong>
          <pre>{{ displayedDifference(block.text) }}</pre>
        </article>
      </div>
      <nav v-if="differences.length > 1" aria-label="差异片段分页">
        <button
          class="quiet"
          :disabled="differencePage === 0"
          @click="differencePage--"
        >
          上一页差异
        </button>
        <label>
          第
          <input
            type="number"
            min="1"
            :max="differences.length"
            :value="differencePage + 1"
            aria-label="差异片段页码"
            @change="selectDifferencePage"
          />
          / {{ differences.length }} 页
        </label>
        <button
          class="quiet"
          :disabled="differencePage >= differences.length - 1"
          @click="differencePage++"
        >
          下一页差异
        </button>
      </nav>
    </section>
  </section>
</template>

<style scoped>
.prose-diff {
  display: grid;
  gap: 0.75rem;
  min-width: 0;
}
h3,
h4,
p {
  margin: 0.4rem 0;
}
.comparison-note {
  color: #52665d;
  font-size: 0.875rem;
}
.original-columns {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1rem;
}
.original-columns > article,
.difference-block {
  min-width: 0;
  padding: 0.75rem;
  border: 1px solid #d7ded5;
  border-radius: 0.4rem;
}
pre {
  white-space: pre-wrap;
  white-space: break-spaces;
  overflow-wrap: anywhere;
  tab-size: 4;
  font: inherit;
  line-height: 1.8;
  max-height: 26rem;
  overflow: auto;
  margin: 0.5rem 0;
}
.original-text {
  min-height: 6rem;
}
.difference-blocks {
  display: grid;
  gap: 0.75rem;
  margin: 0.75rem 0;
}
.removed {
  background: #fff2ee;
  border-color: #dbb0a2;
}
.added {
  background: #edf7ef;
  border-color: #a8c9ae;
}
.block-label {
  font-size: 0.875rem;
}
nav,
nav label,
.whitespace-toggle {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 0.5rem;
}
nav input {
  width: 5rem;
  padding: 0.45rem;
}
nav button {
  padding: 0.5rem 0.75rem;
}
.whitespace-toggle input {
  width: auto;
}
@media (max-width: 760px) {
  .original-columns {
    grid-template-columns: 1fr;
  }
}
</style>
