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
    <section class="intro">
      <div>
        <p class="eyebrow">WORKSPACE / 我的工作室</p>
        <h1>我的创作项目</h1>
        <p class="muted">每个故事都有独立的资料空间。从这里管理你的世界。</p>
      </div>
      <span class="pill">{{ roleNames[actor.role] }}</span>
      <button v-if="canCreate" :disabled="busy" @click="showNovelWizard = true">
        新建小说
      </button>
      <button
        v-if="actor.role === 'owner'"
        :disabled="busy"
        @click="showModels = true"
      >
        模型与用途设置
      </button>
      <button
        v-if="actor.role === 'owner'"
        :disabled="busy"
        @click="showProtection = true"
      >
        备份保护
      </button>
    </section>
    <p v-if="error" class="error" role="alert">{{ error }}</p>
    <p v-if="notice" class="notice" role="status">{{ notice }}</p>
    <section v-if="novels.length" class="panel">
      <h2>继续创作小说</h2>
      <p v-if="novels.length === 100">显示最近100部可访问小说。</p>
      <ul class="project-list">
        <li v-for="book in novels" :key="book.id">
          <button :disabled="busy" @click="openNovel(book)">
            {{ book.setup.title }} · 继续完善世界与故事
          </button>
        </li>
      </ul>
    </section>
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
              <button :disabled="busy || !selectedMember" @click="grant(false)">
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
          <section v-if="selected.role === 'owner' && canCreate" class="access">
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
              <p>已删除项目不能通过旧备份重新开放；恢复为新项目需另行处理。</p>
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
    <details v-if="actor.role === 'owner'" class="panel member-panel">
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
    <footer class="build-note">
      从新建小说开始，完善设定、规划和正文。AI 生成前需配置模型并确认预算。
    </footer>
  </main>
</template>
