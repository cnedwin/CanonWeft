<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import CapabilitiesPanel from "./CapabilitiesPanel.vue";
import QualificationPanel from "./QualificationPanel.vue";
import DiscoveryPanel from "./DiscoveryPanel.vue";
interface FileInfo {
  path: string;
  hash: string;
  size: number;
  text: boolean;
}
interface Version {
  retention?: { state: "EXPIRED"; expired_at: string };
  id: string;
  parent_id: string;
  upstream_id: string;
  state: string;
  available: boolean;
  created_at: string;
  report: {
    name: string;
    description: string;
    hash: string;
    license_claim: string;
    license_files: string[];
    requested_tools: string[];
    script_files: string[];
    files: FileInfo[];
    findings: string[];
    qualification: string;
  };
  source: {
    kind: string;
    repository?: string;
    commit?: string;
    path?: string;
    url?: string;
  };
}
interface SourceFile {
  path: string;
  hash: string;
  text: string;
  editable: boolean;
}
interface Preset {
  issue?: string;
  name: string;
  report: Version["report"];
  source: Version["source"];
}
interface Conflict {
  path: string;
  base: string | null;
  local: string | null;
  upstream: string | null;
  base_exists: boolean;
  upstream_exists: boolean;
  local_exists: boolean;
}
const props = defineProps<{ actor: Actor; project: Project }>();
const emit = defineEmits<{ expired: []; close: [] }>();
const root = `/projects/${props.project.id}/skills`;
const items = ref<Version[]>([]);
const presets = ref<Preset[]>([]);
const selected = ref<Version | null>(null);
const file = ref<SourceFile | null>(null);
const text = ref("");
const busy = ref(false);
const error = ref("");
const notice = ref("");
const mode = ref<"browse" | "create">("browse");
const markdown = ref("");
const operation = ref(newOperationID());
const repo = ref("anthropics/skills");
const revision = ref("main");
const directory = ref("skills/skill-creator");
const reason = ref("");
const baseID = ref("");
const upstreamID = ref("");
const conflicts = ref<Conflict[] | null>(null);
const choices = ref<Record<string, string>>({});
const canEdit = computed(
  () =>
    ["owner", "editor"].includes(props.actor.role) &&
    ["owner", "editor"].includes(props.project.role),
);
const canPublish = computed(
  () =>
    ["owner", "publisher"].includes(props.actor.role) &&
    ["owner", "publisher"].includes(props.project.role),
);
const canRevoke = computed(
  () => props.actor.role === "owner" && props.project.role === "owner",
);
const capabilityBlocking = ref(false);
const qualificationBlocking = ref(false);
const discoveryBlocking = ref(false);
type TaskAttention = { unknown: number; review: number; active: number };
const section = ref("capabilities");
const packageQuery = ref("");
const packageState = ref("");
const taskAttention = ref<Record<string, TaskAttention>>({});
const sections = [
  { id: "capabilities", name: "能力与工作流" },
  { id: "packages", name: "方法包与版本" },
  { id: "discovery", name: "来源发现" },
  { id: "qualification", name: "独立评测与绑定" },
];
const taskAlerts = computed(() =>
  sections.filter((item) => {
    const value = taskAttention.value[item.id];
    return value && (value.unknown || value.review || value.active);
  }),
);
const filteredPackages = computed(() => {
  const query = packageQuery.value.trim().toLocaleLowerCase();
  return items.value.filter(
    (item) =>
      (!packageState.value || item.state === packageState.value) &&
      (!query ||
        [
          item.report.name,
          item.report.description,
          item.id,
          item.source.repository ?? "",
        ].some((value) => value.toLocaleLowerCase().includes(query))),
  );
});
const dirty = computed(
  () =>
    capabilityBlocking.value ||
    qualificationBlocking.value ||
    discoveryBlocking.value ||
    (mode.value === "create"
      ? markdown.value.length > 0
      : !!file.value && text.value !== file.value.text),
);
const stateNames: Record<string, string> = {
  CANDIDATE: "待检查候选",
  REFERENCE: "已保存为方法参考",
  REVOKED: "已撤销",
};
let disposed = false;
watch([text, markdown, repo, revision, directory], () => {
  operation.value = newOperationID();
});
watch([baseID, upstreamID], () => {
  conflicts.value = null;
  choices.value = {};
});
function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function perform(work: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    if (!disposed) fail(e);
  } finally {
    busy.value = false;
  }
}
async function refresh() {
  const [data, defaults] = await Promise.all([
    api<{ items: Version[] }>(root),
    api<{ items: Preset[] }>(`${root}/presets`),
  ]);
  if (!disposed) {
    items.value = data.items;
    presets.value = defaults.items;
  }
}
async function copyPreset(name: string) {
  if (!mayLeave()) return;
  await perform(async () => {
    const version = await api<Version>(
      `${root}/presets/${encodeURIComponent(name)}`,
      "POST",
      {
        operation_id: newOperationID(),
      },
    );
    await refresh();
    await loadVersion(version);
    notice.value = "完整预置包已复制为本项目候选，可检查和编辑；不会调用模型。";
  });
}
function mayLeave() {
  if (dirty.value) {
    error.value = "当前修改尚未保存。请保存为新版本，或先放弃当前编辑。";
    return false;
  }
  return !busy.value;
}
async function open(version: Version) {
  if (version.retention) return;
  if (!mayLeave()) return;
  await perform(() => loadVersion(version));
}
async function loadVersion(version: Version) {
  const [next, content] = await Promise.all([
    api<Version>(`${root}/${version.id}`),
    api<SourceFile>(`${root}/${version.id}/files?path=SKILL.md`),
  ]);
  if (disposed) return;
  selected.value = next;
  file.value = content;
  text.value = content.text;
  mode.value = "browse";
  section.value = "packages";
  conflicts.value = null;
  choices.value = {};
  reason.value = "";
}
async function openDiscovered(id: string, previous: string) {
  if (!mayLeave()) return;
  await perform(async () => {
    const version = await api<Version>(`${root}/${id}`);
    await refresh();
    await loadVersion(version);
    if (disposed) return;
    baseID.value = previous;
    upstreamID.value = previous ? id : "";
    if (previous)
      notice.value =
        "新旧来源已填入三方比较。请选择你保留的本地派生版本，再预览合并；不会自动覆盖手改。";
  });
}
async function openFile(path: string) {
  if (!selected.value || !mayLeave()) return;
  await perform(async () => {
    const content = await api<SourceFile>(
      `${root}/${selected.value!.id}/files?path=${encodeURIComponent(path)}`,
    );
    if (!disposed) {
      file.value = content;
      text.value = content.text;
    }
  });
}
function newMethod() {
  if (mayLeave()) {
    section.value = "packages";
    mode.value = "create";
    markdown.value = "";
    file.value = null;
    selected.value = null;
  }
}
function discard() {
  markdown.value = "";
  text.value = file.value?.text ?? "";
  mode.value = "browse";
}
async function create() {
  await perform(async () => {
    const v = await api<Version>(root, "POST", {
      operation_id: operation.value,
      markdown: markdown.value,
    });
    markdown.value = "";
    mode.value = "browse";
    await refresh();
    notice.value = `方法候选 ${v.id.slice(0, 8)} 已保存；未激活生产能力。`;
  });
}
async function save() {
  if (!selected.value || !file.value) return;
  await perform(async () => {
    const v = await api<Version>(`${root}/${selected.value!.id}/edit`, "POST", {
      operation_id: operation.value,
      expected_hash: selected.value!.report.hash,
      changes: [{ path: file.value!.path, text: text.value, delete: false }],
    });
    if (disposed) return;
    const content = await api<SourceFile>(
      `${root}/${v.id}/files?path=${encodeURIComponent(file.value!.path)}`,
    );
    selected.value = v;
    file.value = content;
    text.value = content.text;
    await refresh();
    notice.value = "新版本已保存，原始包及上一个版本保持不变。";
  });
}
async function fetchSource() {
  if (!mayLeave()) return;
  await perform(async () => {
    await api<Version>(`${root}/fetch`, "POST", {
      operation_id: operation.value,
      source: {
        repository: repo.value,
        ref: revision.value,
        path: directory.value,
      },
    });
    await refresh();
    notice.value = "已按固定提交抓取完整目录并进行静态分析，未运行包内脚本。";
  });
}
async function upload(event: Event) {
  const input = event.target as HTMLInputElement;
  const uploadFile = input.files?.[0];
  if (!uploadFile) return;
  if (!mayLeave()) {
    input.value = "";
    return;
  }
  await perform(async () => {
    const form = new FormData();
    form.append("operation_id", newOperationID());
    form.append("file", uploadFile);
    const response = await fetch(`/api${root}/import`, {
      method: "POST",
      body: form,
      credentials: "same-origin",
    });
    const data = (await response.json()) as { error?: string };
    if (!response.ok)
      throw new APIError(data.error ?? "技能包未导入", response.status);
    await refresh();
    notice.value = "ZIP 原包与全部文件已保存，静态分析结果可查看。";
  });
  input.value = "";
}
async function reference() {
  if (!selected.value || !mayLeave()) return;
  await perform(async () => {
    selected.value = await api<Version>(
      `${root}/${selected.value!.id}/reference`,
      "POST",
      {},
    );
    await refresh();
    notice.value = "该版本已保存为方法参考；这不代表模型效果或执行资格已通过。";
  });
}
async function revoke() {
  if (!selected.value || !mayLeave()) return;
  await perform(async () => {
    await api(`${root}/${selected.value!.id}/revoke`, "POST", {
      reason: reason.value,
    });
    selected.value = await api<Version>(`${root}/${selected.value!.id}`);
    await refresh();
    notice.value =
      "该版本已撤销，所有派生版本的使用资格同时受限；历史内容保留。";
  });
}
function mergeInput() {
  return {
    operation_id: operation.value,
    base_id: baseID.value,
    upstream_id: upstreamID.value,
    local_id: selected.value?.id ?? "",
    choices: choices.value,
  };
}
async function previewMerge() {
  if (!mayLeave()) return;
  await perform(async () => {
    const result = await api<{ conflicts: Conflict[] }>(
      `${root}/merge-preview`,
      "POST",
      mergeInput(),
    );
    conflicts.value = result.conflicts;
    notice.value = result.conflicts.length
      ? "存在同一文件的双向修改，请逐项选择。"
      : "文件合并检查完成，可保存新候选。";
  });
}
async function merge() {
  if (!mayLeave()) return;
  await perform(async () => {
    const v = await api<Version>(`${root}/merge`, "POST", mergeInput());
    selected.value = v;
    file.value = null;
    text.value = "";
    conflicts.value = null;
    await refresh();
    notice.value = "合并已形成新候选，未覆盖旧方法。";
  });
}
function decode(value: string | null, exists: boolean) {
  if (!exists) return "（此文件不存在）";
  if (!value) return "（空文件）";
  return new TextDecoder().decode(
    Uint8Array.from(atob(value), (c) => c.charCodeAt(0)),
  );
}
function close() {
  if (mayLeave()) emit("close");
}
function unload(e: BeforeUnloadEvent) {
  if (dirty.value || busy.value) {
    e.preventDefault();
    e.returnValue = "";
  }
}
onMounted(() => {
  window.addEventListener("beforeunload", unload);
  void perform(refresh);
});
onBeforeUnmount(() => {
  disposed = true;
  window.removeEventListener("beforeunload", unload);
});
</script>
<template>
  <main class="skills-page author-subpage">
    <header class="author-page-header">
      <div>
        <p class="eyebrow">{{ project.name }} / 方法与能力</p>
        <h1>Skill 研究与编辑</h1>
      </div>
      <button class="quiet" :disabled="busy" @click="close">
        返回项目书架
      </button>
    </header>
    <p class="author-page-description">
      先选择能力或完整方法包，编辑后保留为新版本，再按任务核对试用、独立评测与用途绑定。来源原件、旧版本与历史记录持续保留。
    </p>
    <nav class="author-page-nav" aria-label="方法与能力分区">
      <button
        v-for="item in sections"
        :key="item.id"
        class="quiet"
        :aria-pressed="section === item.id"
        :disabled="busy"
        @click="section = item.id"
      >
        {{ item.name }}
      </button>
    </nav>
    <aside v-if="dirty" class="skills-draft-notice">
      有未保存输入或操作正在进行。切换分区会保留本页内容；返回前请在相应分区保存为新版本或放弃编辑。
    </aside>
    <aside
      v-if="taskAlerts.length"
      class="skills-task-notice"
      aria-label="已读取的能力任务状态"
    >
      <strong>已读取记录中的待处理任务</strong>
      <div v-for="item in taskAlerts" :key="item.id">
        <span
          >{{ item.name }}：结果未知 {{ taskAttention[item.id]?.unknown }} 项 ·
          等待维护或交接确认 {{ taskAttention[item.id]?.review }} 项 · 进行中
          {{ taskAttention[item.id]?.active }} 项</span
        >
        <button class="quiet" :disabled="busy" @click="section = item.id">
          查看{{ item.name }}
        </button>
      </div>
      <p>
        仅反映已读取的任务；未读取记录不在统计内。结果未知请先核对回执与费用，任务记录不会因切换分区而重新执行。
      </p>
    </aside>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <DiscoveryPanel
      v-show="section === 'discovery'"
      :actor="actor"
      :project="project"
      @expired="emit('expired')"
      @blocking="discoveryBlocking = $event"
      @open="openDiscovered"
      @attention="taskAttention.discovery = $event"
    />
    <CapabilitiesPanel
      v-show="section === 'capabilities'"
      :actor="actor"
      :project="project"
      @expired="emit('expired')"
      @blocking="capabilityBlocking = $event"
      @installed="perform(refresh)"
      @attention="taskAttention.capabilities = $event"
    />
    <QualificationPanel
      v-show="section === 'qualification'"
      :key="project.id"
      :actor="actor"
      :project="project"
      @expired="emit('expired')"
      @blocking="qualificationBlocking = $event"
      @attention="taskAttention.qualification = $event"
    />
    <section v-show="section === 'packages'" aria-label="方法包与版本工作区">
      <section class="panel" v-if="canEdit">
        <details>
          <summary>随系统提供的完整方法包</summary>
          <article v-for="preset in presets" :key="preset.name">
            <h3>{{ preset.report.name }}</h3>
            <p>{{ preset.report.description }}</p>
            <p>
              包含
              {{ preset.report.files.length }}
              个文件；各模型成品资格单独验证。
            </p>
            <p v-if="preset.source.kind === 'github'">
              外部来源：{{ preset.source.repository }} · 版本
              {{
                preset.source.commit?.slice(0, 12)
              }}。保留完整原件、许可和脚本；脚本尚未获准运行。
            </p>
            <p v-else>本系统方法，包含输入输出契约和例子。</p>
            <p v-if="preset.issue" role="alert">{{ preset.issue }}</p>
            <button
              :disabled="busy || dirty || !!preset.issue"
              @click="copyPreset(preset.name)"
            >
              复制到本项目后编辑
            </button>
          </article>
        </details>
        <h2>新增方法</h2>
        <div class="actions">
          <button :disabled="busy || dirty" @click="newMethod">
            手工编写 Skill</button
          ><label
            >导入完整技能 ZIP<input
              type="file"
              accept=".zip"
              :disabled="busy || dirty"
              @change="upload"
          /></label>
        </div>
        <details>
          <summary>从公开 GitHub 来源抓取并分析</summary>
          <p class="muted">
            读取完整技能目录及仓库许可，固定提交与文件哈希。不会使用账户凭据，也不会执行下载内容。
          </p>
          <form @submit.prevent="fetchSource">
            <fieldset :disabled="busy || dirty">
              <label
                >公开仓库（owner/repo）<input v-model="repo" required /></label
              ><label
                >分支、标签或提交<input v-model="revision" required /></label
              ><label
                >技能所在目录<input
                  v-model="directory"
                  placeholder="例如 skills/skill-creator" /></label
              ><button>抓取并分析技能包</button>
            </fieldset>
          </form>
        </details>
        <form v-if="mode === 'create'" @submit.prevent="create">
          <label
            >新方法 SKILL.md<textarea
              v-model="markdown"
              rows="14"
              required
              placeholder="包含 name、description 的 YAML 头部，以及方法步骤、例子和边界。"
            /></label
          ><button :disabled="busy">保存方法候选</button
          ><button
            type="button"
            class="quiet"
            :disabled="busy"
            @click="discard"
          >
            放弃当前编辑
          </button>
        </form>
      </section>
      <div class="skill-layout">
        <aside class="panel">
          <h2>版本库</h2>
          <label
            >搜索方法包<input
              v-model="packageQuery"
              type="search"
              aria-label="搜索方法包版本"
              placeholder="名称、来源或版本编号"
          /></label>
          <label
            >版本状态<select
              v-model="packageState"
              aria-label="筛选方法包版本状态"
            >
              <option value="">全部状态</option>
              <option
                v-for="(name, value) in stateNames"
                :key="value"
                :value="value"
              >
                {{ name }}
              </option>
            </select></label
          >
          <p class="muted">
            当前显示 {{ filteredPackages.length }} /
            {{ items.length }} 个已读取版本
          </p>
          <p v-if="!items.length" class="muted">
            尚无项目方法。导入完整技能包或编写自己的方法，分析结果会随版本保存。
          </p>
          <button
            v-for="item in filteredPackages"
            :key="item.id"
            class="version-card"
            :aria-pressed="selected?.id === item.id"
            :disabled="busy || !!item.retention"
            @click="open(item)"
          >
            <strong
              >{{ item.retention ? "已过期的方法" : item.report.name }} ·
              {{ item.id.slice(0, 8) }}</strong
            ><small
              >{{ stateNames[item.state] }} ·
              {{
                item.retention
                  ? "原材料已清理，可重新导入或创建"
                  : item.available
                    ? "来源链未撤销"
                    : "来源链已受限"
              }}</small
            >
          </button>
        </aside>
        <section class="panel" v-if="selected">
          <h2>{{ selected.report.name }}</h2>
          <p>{{ selected.report.description }}</p>
          <p>
            {{ stateNames[selected.state] }} ·
            {{ selected.available ? "来源链未撤销" : "本版本或上游已撤销" }}
          </p>
          <p class="muted">
            本包分析不授予全局资格。具体版本、任务和模型或工具配置的资格，请查看独立评测与用途绑定记录。
          </p>
          <p>
            来源：{{ selected.source.kind }}
            <a
              v-if="selected.source.url"
              :href="selected.source.url"
              target="_blank"
              rel="noopener noreferrer"
              >核对固定来源</a
            >
          </p>
          <p v-if="selected.source.commit">
            固定提交：{{ selected.source.commit }}
          </p>
          <p>
            许可声明：{{ selected.report.license_claim || "未声明" }} · 许可文件
            {{ selected.report.license_files.length }} 份
          </p>
          <p>
            上一个版本：{{ selected.parent_id || "原始包" }} · 合并上游：{{
              selected.upstream_id || "无"
            }}
          </p>
          <ul>
            <li v-for="finding in selected.report.findings" :key="finding">
              {{ finding }}
            </li>
          </ul>
          <details>
            <summary>
              文件与哈希 · {{ selected.report.files.length }} 个
            </summary>
            <ul>
              <li v-for="entry in selected.report.files" :key="entry.path">
                <button
                  class="quiet"
                  :disabled="busy"
                  @click="openFile(entry.path)"
                >
                  {{ entry.path }}</button
                ><small>{{ entry.size }} 字节 · {{ entry.hash }}</small>
              </li>
            </ul>
          </details>
          <div v-if="file">
            <h3>{{ file.path }}</h3>
            <p v-if="!file.editable" class="muted">
              该文件为二进制或超出文本编辑大小，完整原件仍保留在包内。
            </p>
            <template v-else
              ><label
                >文件内容<textarea
                  v-model="text"
                  rows="18"
                  :readonly="!canEdit || !selected.available"
                  :disabled="busy"
                />
              </label>
              <details v-if="dirty">
                <summary>本次编辑前的原文</summary>
                <pre>{{ file.text }}</pre>
              </details>
              <div class="actions" v-if="canEdit">
                <button
                  :disabled="busy || !dirty || !selected.available"
                  @click="save"
                >
                  保存为新技能版本</button
                ><button
                  class="quiet"
                  :disabled="busy || !dirty"
                  @click="discard"
                >
                  放弃当前编辑
                </button>
              </div></template
            >
          </div>
          <button
            v-if="
              canPublish && selected.available && selected.state === 'CANDIDATE'
            "
            :disabled="busy || dirty"
            @click="reference"
          >
            保存为方法参考版本
          </button>
          <details v-if="canEdit && selected.available">
            <summary>三方合并上游更新</summary>
            <label
              >共同基础版本<select v-model="baseID" aria-label="共同基础版本">
                <option value="">选择基础</option>
                <option
                  v-for="v in items.filter(
                    (i) => i.report.name === selected?.report.name,
                  )"
                  :key="v.id"
                  :value="v.id"
                >
                  {{ v.id.slice(0, 8) }} · {{ stateNames[v.state] }}
                </option>
              </select></label
            ><label
              >上游新版本<select v-model="upstreamID" aria-label="上游新版本">
                <option value="">选择上游</option>
                <option
                  v-for="v in items.filter(
                    (i) => i.report.name === selected?.report.name,
                  )"
                  :key="v.id"
                  :value="v.id"
                >
                  {{ v.id.slice(0, 8) }} · {{ stateNames[v.state] }}
                </option>
              </select></label
            ><button
              :disabled="busy || dirty || !baseID || !upstreamID"
              @click="previewMerge"
            >
              检查合并差异
            </button>
            <article v-for="conflict in conflicts" :key="conflict.path">
              <h4>{{ conflict.path }}</h4>
              <details>
                <summary>基础 / 上游 / 本地原文</summary>
                <h5>基础</h5>
                <pre>{{ decode(conflict.base, conflict.base_exists) }}</pre>
                <h5>上游</h5>
                <pre>{{
                  decode(conflict.upstream, conflict.upstream_exists)
                }}</pre>
                <h5>本地</h5>
                <pre>{{ decode(conflict.local, conflict.local_exists) }}</pre>
              </details>
              <label
                >冲突选择<select v-model="choices[conflict.path]">
                  <option value="">请选择</option>
                  <option value="local">保留本地</option>
                  <option value="upstream">采用上游</option>
                  <option value="delete">删除此文件</option>
                </select></label
              >
            </article>
            <button
              v-if="conflicts !== null"
              :disabled="
                busy || dirty || conflicts.some((c) => !choices[c.path])
              "
              @click="merge"
            >
              保存合并候选
            </button>
          </details>
          <details v-if="canRevoke && selected.available">
            <summary>撤销此来源版本</summary>
            <p>
              该版本及沿其派生、合并的版本将不能再保存为可用参考；原文与历史保留。
            </p>
            <label>撤销原因<input v-model="reason" maxlength="1000" /></label
            ><button
              :disabled="busy || dirty || !reason.trim()"
              @click="revoke"
            >
              撤销此版本及派生引用
            </button>
          </details>
        </section>
      </div>
    </section>
  </main>
</template>
<style scoped>
.skills-page {
  max-width: 1250px;
  margin: 2rem auto;
  padding: 0 1.5rem;
}
.skills-draft-notice,
.skills-task-notice {
  padding: 16px 20px;
  margin-bottom: 20px;
  border: 1px solid #d8d1b7;
  border-radius: 10px;
  background: #f6f2e7;
  font-size: 13px;
  line-height: 1.8;
}
.skills-task-notice > div {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-top: 12px;
}
.skills-task-notice p {
  margin-bottom: 0;
}
.version-card[aria-pressed="true"] {
  outline: 2px solid #91ad98;
  outline-offset: 2px;
}
.skill-layout {
  display: grid;
  grid-template-columns: minmax(240px, 1fr) minmax(0, 3fr);
  gap: 1rem;
  margin-top: 1rem;
}
.version-card {
  display: grid;
  gap: 0.4rem;
  width: 100%;
  text-align: left;
  margin: 0.6rem 0;
  overflow-wrap: anywhere;
}
.actions {
  display: flex;
  flex-wrap: wrap;
  gap: 0.75rem;
  align-items: center;
}
label {
  margin: 0.8rem 0;
}
details {
  margin: 1rem 0;
}
pre,
small,
p {
  overflow-wrap: anywhere;
}
pre {
  white-space: pre-wrap;
  max-height: 24rem;
  overflow: auto;
}
textarea {
  font-family: ui-monospace, monospace;
}
@media (max-width: 800px) {
  .skill-layout {
    grid-template-columns: 1fr;
  }
}
</style>
