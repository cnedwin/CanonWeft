<script setup lang="ts">
import { computed, onMounted, ref, watch } from "vue";
import {
  newOperationID,
  api,
  APIError,
  message,
  type Actor,
  type Project,
} from "../../api";
import IPPage from "../ip/IPPage.vue";
import ModelsPage from "../models/ModelsPage.vue";
import SkillsPage from "../skills/SkillsPage.vue";
import ProtectionPage from "../operations/ProtectionPage.vue";
import NovelSetupWizard from "../novel/NovelSetupWizard.vue";
import NovelWorkspace from "../novel/NovelWorkspace.vue";
import type { NovelBook } from "../novel/types";

const props = defineProps<{ actor: Actor }>();
const emit = defineEmits<{ expired: [] }>();
const projects = ref<Project[]>([]);
const novels = ref<NovelBook[]>([]);
const activeSection = ref<"novels" | "projects" | "settings">("novels");
const novelQuery = ref("");
const filteredNovels = computed(() => {
  const query = novelQuery.value.trim().toLocaleLowerCase();
  if (!query) return novels.value;
  return novels.value.filter((book) =>
    [book.setup.title, book.setup.genre, book.setup.idea].some((value) =>
      (value || "").toLocaleLowerCase().includes(query),
    ),
  );
});
const showNovelWizard = ref(false);
const selectedNovel = ref<NovelBook | null>(null);
const novelProject = ref<Project | null>(null);
const selected = ref<Project | null>(null);
const ipProject = ref<Project | null>(null);
const skillsProject = ref<Project | null>(null);
const showModels = ref(false);
const showProtection = ref(false);
const name = ref("");
const rename = ref("");
const deleting = ref(false);
const deleteName = ref("");
const deleteOperation = ref(newOperationID());
const operation = ref(newOperationID());
const busy = ref(false);
const error = ref("");
const notice = ref("");
const members = ref<Actor[]>([]);
const memberName = ref("");
const memberPassword = ref("");
const memberRole = ref("reader");
const selectedMember = ref("");
const projectRole = ref("reader");
const roleNames: Record<string, string> = {
  owner: "所有者",
  editor: "编辑",
  publisher: "发布维护",
  reader: "只读",
};
const canCreate = computed(() =>
  ["owner", "editor"].includes(props.actor.role),
);
const canEdit = computed(
  () =>
    selected.value &&
    ["owner", "editor"].includes(selected.value.role) &&
    canCreate.value,
);
watch(name, () => {
  operation.value = newOperationID();
});
watch(selected, () => {
  deleting.value = false;
  deleteName.value = "";
  deleteOperation.value = newOperationID();
});

function fail(e: unknown) {
  error.value = message(e);
  if (e instanceof APIError && e.status === 401) emit("expired");
}
async function refresh() {
  projects.value = (await api<{ items: Project[] }>("/projects")).items;
  novels.value = (await api<{ items: NovelBook[] }>("/novels")).items;
  if (props.actor.role === "owner")
    members.value = (await api<{ items: Actor[] }>("/members")).items.filter(
      (m) => m.role !== "owner",
    );
}
async function openNovel(book: NovelBook) {
  await perform(async () => {
    const current = await api<NovelBook>(`/novels/${book.id}`);
    const project = await api<Project>(`/projects/${current.project_id}`);
    selectedNovel.value = current;
    novelProject.value = project;
    showNovelWizard.value = false;
    await refresh();
  });
}
async function openRecovered(project: Project) {
  showProtection.value = false;
  activeSection.value = "projects";
  selected.value = project;
  notice.value = "恢复副本已就绪，可以打开设定库继续创作。";
  try {
    await refresh();
  } catch (cause) {
    fail(cause);
  }
}
async function perform(work: () => Promise<void>) {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  notice.value = "";
  try {
    await work();
  } catch (e) {
    fail(e);
  } finally {
    busy.value = false;
  }
}
async function create() {
  await perform(async () => {
    const project = await api<Project>("/projects", "POST", {
      name: name.value,
      operation_id: operation.value,
    });
    name.value = "";
    selected.value = project;
    rename.value = project.name;
    await refresh();
    notice.value = "项目已保存";
  });
}
async function choose(project: Project) {
  await perform(async () => {
    selected.value = await api<Project>(`/projects/${project.id}`);
    rename.value = selected.value.name;
  });
}
async function saveName() {
  if (!selected.value) return;
  const base = selected.value;
  await perform(async () => {
    selected.value = await api<Project>(`/projects/${base.id}`, "PATCH", {
      name: rename.value,
      revision: base.revision,
    });
    await refresh();
    notice.value = "名称已保存";
  });
}
async function addMember() {
  await perform(async () => {
    await api("/members", "POST", {
      username: memberName.value,
      password: memberPassword.value,
      role: memberRole.value,
    });
    memberPassword.value = "";
    memberName.value = "";
    await refresh();
    notice.value = "成员已创建，尚未分配其他项目";
  });
}
async function deleteProject() {
  const project = selected.value;
  if (!project || deleteName.value !== project.name) return;
  await perform(async () => {
    await api(`/projects/${project.id}`, "DELETE", {
      confirm_name: deleteName.value,
      revision: project.revision,
      operation_id: deleteOperation.value,
    });
    selected.value = null;
    ipProject.value = null;
    skillsProject.value = null;
    await refresh();
    notice.value = "项目访问已撤销，历史与文件保留待后续清理。";
  });
}
async function grant(remove = false) {
  if (!selected.value || !selectedMember.value) return;
  const path = `/projects/${selected.value.id}/members/${selectedMember.value}`;
  await perform(async () => {
    await api(
      path,
      remove ? "DELETE" : "PUT",
      remove ? undefined : { role: projectRole.value },
    );
    notice.value = remove ? "该成员的项目访问权已撤销" : "项目权限已保存";
  });
}
onMounted(() => perform(refresh));
</script>

<template>
  <NovelSetupWizard
    v-if="showNovelWizard"
    :projects="projects"
    @close="showNovelWizard = false"
    @created="openNovel"
    @expired="emit('expired')"
  />
  <NovelWorkspace
    v-else-if="selectedNovel && novelProject"
    :key="selectedNovel.id"
    :book="selectedNovel"
    :project="novelProject"
    :actor="actor"
    @close="
      selectedNovel = null;
      novelProject = null;
    "
    @updated="
      (book) => {
        selectedNovel = book;
        novels = novels.map((item) => (item.id === book.id ? book : item));
      }
    "
    @expired="emit('expired')"
  />
  <SkillsPage
    v-else-if="skillsProject"
    :key="skillsProject.id"
    :project="skillsProject"
    :actor="actor"
    @close="skillsProject = null"
    @expired="emit('expired')"
  />
  <IPPage
    v-else-if="ipProject"
    :key="ipProject.id"
    :project="ipProject"
    :actor="actor"
    @close="ipProject = null"
    @expired="emit('expired')"
  />
  <ModelsPage
    v-else-if="showModels"
    @close="showModels = false"
    @expired="emit('expired')"
  />
  <ProtectionPage
    v-else-if="showProtection"
    :actor="actor"
    :projects="projects"
    @recovered="openRecovered"
    @close="showProtection = false"
    @expired="emit('expired')"
  />
  <main v-else class="workspace">
    <section class="intro workspace-intro">
      <div>
        <p class="eyebrow">WORKSPACE / 我的工作室</p>
        <h1>
          {{
            activeSection === "novels"
              ? "我的小说"
              : activeSection === "projects"
                ? "IP项目"
                : "工作室设置"
          }}
        </h1>
        <p v-if="activeSection === 'novels'" class="muted">
          从一个故事开始，回到书架就能继续创作。
        </p>
        <p v-else-if="activeSection === 'projects'" class="muted">
          管理故事共用的设定资料、项目名称与访问权限。
        </p>
        <p v-else class="muted">管理工作室成员、模型用途与作品备份。</p>
      </div>
      <div class="workspace-intro-actions">
        <span class="pill">{{ roleNames[actor.role] }}</span>
        <button
          v-if="canCreate"
          v-show="activeSection === 'novels'"
          :disabled="busy"
          @click="showNovelWizard = true"
        >
          新建小说
        </button>
      </div>
    </section>
    <nav class="workspace-navigation" aria-label="工作室分区">
      <button
        class="quiet"
        :aria-pressed="activeSection === 'novels'"
        @click="activeSection = 'novels'"
      >
        我的小说
      </button>
      <button
        class="quiet"
        :aria-pressed="activeSection === 'projects'"
        @click="activeSection = 'projects'"
      >
        IP项目
      </button>
      <button
        v-if="actor.role === 'owner'"
        class="quiet"
        :aria-pressed="activeSection === 'settings'"
        @click="activeSection = 'settings'"
      >
        工作室设置
      </button>
    </nav>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <section
      v-show="activeSection === 'novels'"
      class="workspace-area novel-shelf"
      aria-label="我的小说书架"
    >
      <div class="shelf-heading">
        <h2>
          小说书架 <span class="count">{{ novels.length }} 部</span>
        </h2>
        <label v-if="novels.length" class="novel-search">
          搜索小说
          <input
            v-model="novelQuery"
            type="search"
            placeholder="书名、题材或故事简介"
            maxlength="300"
          />
        </label>
      </div>
      <p v-if="novels.length === 100" class="muted">
        显示最近100部可访问小说。
      </p>
      <p v-if="novelQuery.trim() && novels.length" class="muted" role="status">
        当前书架找到 {{ filteredNovels.length }} 部小说。
      </p>
      <ul v-if="filteredNovels.length" class="novel-grid">
        <li v-for="book in filteredNovels" :key="book.id" class="novel-card">
          <div class="novel-card-meta">
            <span>{{ book.setup.genre || "题材待设定" }}</span>
            <span>
              {{
                book.setup.target_words
                  ? `目标 ${book.setup.target_words.toLocaleString()} 字`
                  : "目标篇幅待设定"
              }}
            </span>
          </div>
          <h3>{{ book.setup.title }}</h3>
          <p class="novel-card-idea">
            {{ book.setup.idea || "打开小说，继续记录你的故事。" }}
          </p>
          <button
            :disabled="busy"
            :aria-label="`进入创作 · ${book.setup.title}`"
            @click="openNovel(book)"
          >
            进入创作
          </button>
        </li>
      </ul>
      <div v-else-if="novels.length" class="panel shelf-empty">
        <h3>没有找到匹配的小说</h3>
        <p>试试其他书名、题材或简介中的关键词。</p>
        <button class="quiet" @click="novelQuery = ''">清空搜索</button>
      </div>
      <div v-else class="panel shelf-empty">
        <p class="eyebrow">你的故事，从这里开始</p>
        <h2>书架等待你的第一部小说</h2>
        <p v-if="canCreate">
          点击“新建小说”，写下故事灵感与创作约定。已有开书草稿也可以接着填写。
        </p>
        <p v-else>暂时没有可访问的小说，请联系工作室所有者分配项目权限。</p>
      </div>
      <div v-if="canCreate" class="shelf-draft-entry">
        <p class="muted">上次开书还没完成？</p>
        <button class="quiet" :disabled="busy" @click="showNovelWizard = true">
          继续开书草稿
        </button>
      </div>
    </section>
    <section
      v-show="activeSection === 'projects'"
      class="workspace-area"
      aria-label="IP项目管理"
    >
      <div class="layout">
        <section class="panel projects">
          <h2>
            项目书架 <span class="count">{{ projects.length }}</span>
          </h2>
          <form v-if="canCreate" class="create" @submit.prevent="create">
            <label
              >项目名称<input
                :disabled="busy"
                v-model="name"
                placeholder="为新的世界命名"
                required
                maxlength="120" /></label
            ><button :disabled="busy">创建项目</button>
          </form>
          <p v-if="!projects.length" class="empty">
            {{
              canCreate
                ? "还没有项目。创建后即可管理项目名称和访问权限。"
                : "暂时没有可访问的项目，请联系工作室所有者。"
            }}
          </p>
          <ul class="project-list">
            <li v-for="project in projects" :key="project.id">
              <button
                class="project-card"
                :aria-label="project.name"
                :class="{ active: selected?.id === project.id }"
                :disabled="busy"
                @click="choose(project)"
              >
                <span class="book-mark" aria-hidden="true">文</span
                ><span>{{ project.name }}</span
                ><small>{{ roleNames[project.role] }}</small>
              </button>
            </li>
          </ul>
        </section>
        <section class="panel details">
          <template v-if="selected"
            ><p class="eyebrow">PROJECT DETAILS</p>
            <h2>{{ selected.name }}</h2>
            <button :disabled="busy" @click="ipProject = selected">
              打开设定库
            </button>
            <button
              class="secondary"
              :disabled="busy"
              @click="skillsProject = selected"
            >
              Skill 研究与编辑
            </button>
            <p class="muted">
              版本 {{ selected.revision }} · {{ roleNames[selected.role] }}
            </p>
            <form v-if="canEdit" @submit.prevent="saveName">
              <label
                >修改项目名称<input
                  :disabled="busy"
                  v-model="rename"
                  required
                  maxlength="120" /></label
              ><button :disabled="busy">保存名称</button>
            </form>
            <section v-if="actor.role === 'owner'" class="access">
              <h3>项目访问权限</h3>
              <p class="muted">只有明确分配的成员能够看到此项目。</p>
              <label
                >选择成员<select v-model="selectedMember">
                  <option value="">请选择</option>
                  <option
                    v-for="member in members"
                    :key="member.id"
                    :value="member.id"
                  >
                    {{ member.username }}
                  </option>
                </select></label
              ><label
                >项目角色<select v-model="projectRole">
                  <option value="reader">只读</option>
                  <option value="editor">编辑</option>
                  <option value="publisher">发布维护</option>
                </select></label
              >
              <div class="actions">
                <button
                  :disabled="busy || !selectedMember"
                  @click="grant(false)"
                >
                  保存权限</button
                ><button
                  class="quiet"
                  :disabled="busy || !selectedMember"
                  @click="grant(true)"
                >
                  撤销访问
                </button>
              </div>
            </section>
            <section
              v-if="selected.role === 'owner' && canCreate"
              class="access"
            >
              <button
                v-if="!deleting"
                class="quiet"
                :disabled="busy"
                @click="deleting = true"
              >
                删除项目
              </button>
              <form v-else @submit.prevent="deleteProject">
                <h3>删除项目并立即撤销访问</h3>
                <p>
                  立即撤销所有成员、后台任务和文件的访问；历史与文件仍保留，尚未执行物理清理。
                </p>
                <p>
                  已删除项目不能通过旧备份重新开放；恢复为新项目需另行处理。
                </p>
                <label
                  >输入完整项目名称确认删除<input
                    v-model="deleteName"
                    :disabled="busy"
                    autocomplete="off"
                    maxlength="120"
                /></label>
                <div class="actions">
                  <button :disabled="busy || deleteName !== selected.name">
                    确认删除并撤销访问
                  </button>
                  <button
                    type="button"
                    class="secondary"
                    :disabled="busy"
                    @click="deleting = false"
                  >
                    取消
                  </button>
                </div>
              </form>
            </section>
          </template>
          <div v-else class="empty">
            <h2>选择一个项目</h2>
            <p>查看项目资料与访问权限。</p>
          </div>
        </section>
      </div>
    </section>
    <section
      v-if="actor.role === 'owner'"
      v-show="activeSection === 'settings'"
      class="workspace-area workspace-settings"
      aria-label="工作室设置"
    >
      <div class="workspace-settings-grid">
        <section class="panel workspace-setting-card">
          <h2>模型与用途</h2>
          <p class="muted">配置模型连接，安排各项创作任务使用的模型。</p>
          <button class="quiet" :disabled="busy" @click="showModels = true">
            模型与用途设置
          </button>
        </section>
        <section class="panel workspace-setting-card">
          <h2>作品备份</h2>
          <p class="muted">查看备份保护、导入备份与恢复作品副本。</p>
          <button class="quiet" :disabled="busy" @click="showProtection = true">
            备份保护
          </button>
        </section>
      </div>
      <details class="panel member-panel">
        <summary>工作室成员管理</summary>
        <form class="member-form" @submit.prevent="addMember">
          <label
            >新成员用户名<input
              :disabled="busy"
              v-model="memberName"
              required
              maxlength="80"
              autocomplete="off" /></label
          ><label
            >初始密码<input
              :disabled="busy"
              v-model="memberPassword"
              type="password"
              required
              minlength="12"
              maxlength="72"
              autocomplete="new-password" /></label
          ><label
            >工作室角色<select v-model="memberRole">
              <option value="reader">只读</option>
              <option value="editor">编辑</option>
              <option value="publisher">发布维护</option>
            </select></label
          ><button :disabled="busy">创建成员</button>
        </form>
      </details>
    </section>
    <footer class="build-note">从新建小说开始，完善设定、规划和正文。</footer>
  </main>
</template>

<style scoped>
.workspace-intro {
  align-items: center;
  gap: 1.5rem;
}
.workspace-intro h1 {
  margin-bottom: 0.5rem;
}
.workspace-intro p:last-child {
  margin-bottom: 0;
}
.workspace-intro-actions,
.workspace-navigation,
.shelf-draft-entry {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 0.75rem;
}
.workspace-intro-actions {
  justify-content: flex-end;
}
.workspace-navigation {
  margin-bottom: 1.75rem;
  padding-bottom: 1rem;
  border-bottom: 1px solid #dce3d6;
}
.workspace-navigation button {
  border-color: transparent;
}
.workspace-navigation button[aria-pressed="true"] {
  background: #e6eee2;
  border-color: #a7b99b;
  color: #285c4d;
}
.workspace-area {
  min-width: 0;
}
.shelf-heading {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 1rem;
  margin-bottom: 1.25rem;
}
.shelf-heading h2 {
  margin: 0;
}
.novel-search {
  width: min(100%, 22rem);
}
.novel-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 1.25rem;
  margin: 0;
  padding: 0;
  list-style: none;
}
.novel-card {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  min-width: 0;
  padding: 1.5rem;
  border: 1px solid #dce3d6;
  border-left: 4px solid #8ca079;
  border-radius: 10px;
  background: #fdfdf9;
}
.novel-card h3 {
  margin: 1rem 0 0.75rem;
  font-family: "SimSun", serif;
  font-size: 1.375rem;
  line-height: 1.5;
  overflow-wrap: anywhere;
}
.novel-card-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 0.375rem 0.75rem;
  color: #687b65;
  font-size: 0.75rem;
}
.novel-card-idea {
  display: -webkit-box;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 3;
  overflow: hidden;
  margin: 0 0 1.25rem;
  color: #657364;
  font-size: 0.875rem;
  overflow-wrap: anywhere;
}
.novel-card button {
  margin-top: auto;
}
.shelf-empty {
  padding: 3rem 2rem;
  text-align: center;
}
.shelf-empty h2 {
  margin-top: 0.75rem;
}
.shelf-empty p {
  color: #657364;
}
.shelf-draft-entry {
  justify-content: center;
  margin-top: 1.5rem;
}
.shelf-draft-entry p {
  margin: 0;
}
.workspace-settings {
  display: grid;
  gap: 1.5rem;
}
.workspace-settings-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 1.25rem;
}
.workspace-setting-card h2 {
  margin-bottom: 0.75rem;
}
.workspace-settings .member-panel {
  margin: 0;
}
.workspace-settings .member-panel summary {
  cursor: pointer;
}
.workspace-navigation button:focus-visible,
.workspace-settings .member-panel summary:focus-visible {
  outline: 2px solid #93b7a4;
  outline-offset: 3px;
}
@media (max-width: 1000px) {
  .novel-grid {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}
@media (max-width: 760px) {
  .workspace-intro,
  .shelf-heading {
    align-items: flex-start;
    flex-direction: column;
  }
  .workspace-intro-actions {
    width: 100%;
    justify-content: space-between;
  }
  .workspace-navigation {
    gap: 0.25rem;
  }
  .workspace-navigation button {
    flex: 1;
    padding-inline: 0.5rem;
  }
  .novel-search {
    width: 100%;
  }
  .novel-grid,
  .workspace-settings-grid {
    grid-template-columns: minmax(0, 1fr);
  }
  .shelf-empty {
    padding: 2rem 1.25rem;
  }
}
</style>
