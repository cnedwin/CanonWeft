<script setup lang="ts">
import { computed, onUnmounted, ref, watch } from "vue";
import { api, APIError, message, type Project } from "../../api";

interface Member {
  release_id: string;
  manifest_hash: string;
  branch_id: string;
}
interface HistoryItem {
  id: string;
  recovered: boolean;
  members: Member[];
}
interface ReleaseDetail {
  id: string;
  hash: string;
  manifest: { branch_id: string; ip_candidate_id?: string };
  contents?: { resource_id: string; text: string }[];
}
const props = defineProps<{ project: Project }>();
const emit = defineEmits<{ expired: [] }>();
const root = computed(() => `/projects/${props.project.id}`);
const opened = ref(false);
const loaded = ref(false);
const busy = ref(false);
const detailBusy = ref(false);
const items = ref<HistoryItem[]>([]);
const next = ref("");
const branches = ref<{ id: string; name: string }[]>([]);
const error = ref("");
const detailError = ref("");
const selected = ref<Member>();
const detail = ref<ReleaseDetail>();
let generation = 0;
let detailGeneration = 0;
function report(e: unknown, target: typeof error) {
  target.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function load(more = false) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  const ticket = ++generation;
  const path = root.value;
  try {
    const [page, names] = await Promise.all([
      api<{ items: HistoryItem[]; next: string }>(
        `${path}/joint-publications?after=${encodeURIComponent(more ? next.value : "")}`,
      ),
      api<{ items: { id: string; name: string }[] }>(`${path}/branches`),
    ]);
    if (ticket !== generation) return;
    items.value = more ? [...items.value, ...page.items] : page.items;
    next.value = page.next;
    branches.value = names.items;
    loaded.value = true;
  } catch (e) {
    if (ticket === generation) report(e, error);
  } finally {
    if (ticket === generation) busy.value = false;
  }
}
async function view(member: Member) {
  const ticket = ++detailGeneration;
  selected.value = member;
  detail.value = undefined;
  detailError.value = "";
  detailBusy.value = true;
  try {
    const value = await api<ReleaseDetail>(
      `${root.value}/releases/${member.release_id}`,
    );
    if (ticket !== detailGeneration) return;
    if (
      value.id !== member.release_id ||
      value.hash !== member.manifest_hash ||
      value.manifest.branch_id !== member.branch_id
    )
      throw new Error("历史版本与记录不一致，请刷新记录后重试。");
    detail.value = value;
  } catch (e) {
    if (ticket === detailGeneration) report(e, detailError);
  } finally {
    if (ticket === detailGeneration) detailBusy.value = false;
  }
}
function name(id: string) {
  return branches.value.find((branch) => branch.id === id)?.name ?? id;
}
function refresh() {
  if (opened.value) void load();
}
function toggle(event: Event) {
  opened.value = (event.target as HTMLDetailsElement).open;
  if (opened.value && !loaded.value) void load();
}
watch(
  () => props.project.id,
  () => {
    generation++;
    detailGeneration++;
    items.value = [];
    branches.value = [];
    next.value = "";
    selected.value = undefined;
    detail.value = undefined;
    error.value = "";
    detailError.value = "";
    busy.value = false;
    detailBusy.value = false;
    loaded.value = false;
    refresh();
  },
);
onUnmounted(() => {
  generation++;
  detailGeneration++;
});
defineExpose({ refresh });
</script>

<template>
  <details @toggle="toggle">
    <summary>联合发布历史</summary>
    <section aria-label="联合发布历史记录">
      <p>
        查看曾经一起发布的各分支版本。历史内容可能与当前正文不同，查看不会修改作品。
      </p>
      <button type="button" :disabled="busy" @click="load()">
        刷新历史记录
      </button>
      <p v-if="busy" role="status">正在读取历史记录…</p>
      <p v-if="error" role="alert">{{ error }}</p>
      <p v-if="loaded && !items.length && !error">暂无联合发布记录。</p>
      <article v-for="item in items" :key="item.id" class="history-card">
        <strong>{{ item.members.length }} 个分支的联合发布</strong>
        <p v-if="item.recovered" class="recovered">
          从原项目恢复的历史 · 恢复时没有重新发布
        </p>
        <details>
          <summary>记录编号</summary>
          <code>{{ item.id }}</code>
        </details>
        <ul>
          <li v-for="member in item.members" :key="member.release_id">
            <span>{{ name(member.branch_id) }} · {{ member.release_id }}</span>
            <button
              type="button"
              :disabled="detailBusy"
              :aria-label="`查看历史版本 ${member.release_id}`"
              @click="view(member)"
            >
              查看保存内容
            </button>
          </li>
        </ul>
      </article>
      <button v-if="next" type="button" :disabled="busy" @click="load(true)">
        加载更多历史
      </button>
      <small v-if="items.length">按记录编号分页，每次最多20组。</small>
      <section
        v-if="selected"
        aria-label="联合发布历史内容"
        class="history-content"
      >
        <h4>{{ name(selected.branch_id) }} · 历史版本</h4>
        <code>{{ selected.release_id }}</code>
        <p v-if="detailBusy" role="status">正在读取保存内容…</p>
        <p v-if="detailError" role="alert">
          {{ detailError }}
          <button type="button" :disabled="detailBusy" @click="view(selected)">
            重试读取内容
          </button>
        </p>
        <template v-if="detail">
          <p v-if="detail.manifest.ip_candidate_id">
            附带设定版本：{{ detail.manifest.ip_candidate_id }}
          </p>
          <article
            v-for="content in detail.contents ?? []"
            :key="content.resource_id"
          >
            <h5>资料 {{ content.resource_id }}</h5>
            <pre>{{ content.text }}</pre>
          </article>
          <p v-if="!detail.contents?.length">这份历史清单不包含正文资料。</p>
        </template>
      </section>
    </section>
  </details>
</template>

<style scoped>
.history-card,
.history-content {
  border: 1px solid var(--line, #d6dcd5);
  border-radius: 10px;
  padding: 1rem;
  margin-block: 1rem;
  overflow-wrap: anywhere;
}
.history-card li {
  margin-block: 0.8rem;
}
.history-card li button {
  margin-left: 0.6rem;
}
.recovered {
  color: #52654c;
}
pre {
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  font: inherit;
}
small {
  display: block;
  margin-block: 0.5rem;
}
</style>
