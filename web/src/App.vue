<script setup lang="ts">
import { onMounted, ref } from "vue";
import { api, APIError, message, type Actor } from "./api";
import WorkspacePage from "./features/workspace/WorkspacePage.vue";

const actor = ref<Actor | null>(null);
const loading = ref(true);
const setup = ref(false);
const busy = ref(false);
const error = ref("");
const username = ref("");
const password = ref("");
const workspace = ref("我的创作工作室");

async function load() {
  loading.value = true;
  error.value = "";
  try {
    setup.value = (await api<{ required: boolean }>("/setup")).required;
    if (!setup.value) {
      try {
        actor.value = await api<Actor>("/me");
      } catch (e) {
        if (!(e instanceof APIError) || e.status !== 401) throw e;
      }
    }
  } catch (e) {
    error.value = message(e);
  } finally {
    loading.value = false;
  }
}

async function login() {
  if (busy.value) return;
  busy.value = true;
  error.value = "";
  try {
    if (setup.value) {
      await api("/setup", "POST", {
        username: username.value,
        password: password.value,
        workspace: workspace.value,
      });
      setup.value = false;
    }
    await api("/login", "POST", {
      username: username.value,
      password: password.value,
    });
    password.value = "";
    actor.value = await api<Actor>("/me");
  } catch (e) {
    error.value = message(e);
  } finally {
    busy.value = false;
  }
}

async function logout() {
  try {
    await api("/logout", "POST");
    actor.value = null;
  } catch (e) {
    error.value = message(e);
  }
}
onMounted(load);
</script>

<template>
  <header class="topbar">
    <div class="brand">
      <span class="seal">砚</span>
      <div><strong>砚台</strong><small>让故事，有自己的世界</small></div>
    </div>
    <div v-if="actor" class="account">
      <span>{{ actor.username }}</span
      ><button class="quiet" @click="logout">退出登录</button>
    </div>
    <span v-else class="pill">本机私有工作室</span>
  </header>
  <main v-if="loading" class="login"><p>正在连接工作室…</p></main>
  <WorkspacePage v-else-if="actor" :actor="actor" @expired="actor = null" />
  <main v-else class="login">
    <p class="eyebrow">YOUR CREATIVE SPACE</p>
    <h1>{{ setup ? "开启你的创作工作室" : "回到你的创作现场" }}</h1>
    <p class="muted">
      {{
        setup
          ? "创建本机管理员，作品与资料将保存在这台电脑。"
          : "登录后继续管理你的创作项目。"
      }}
    </p>
    <form class="panel login-form" @submit.prevent="login">
      <label v-if="setup"
        >工作室名称<input v-model="workspace" required maxlength="120"
      /></label>
      <label
        >用户名<input
          v-model="username"
          autocomplete="username"
          required
          maxlength="80"
      /></label>
      <label
        >密码<input
          v-model="password"
          type="password"
          :autocomplete="setup ? 'new-password' : 'current-password'"
          required
          :minlength="setup ? 12 : 1"
          maxlength="72"
      /></label>
      <small v-if="setup" class="muted"
        >密码至少 12 个字符。无需模型 Key，也不会产生模型费用。</small
      >
      <p v-if="error" class="error" role="alert">{{ error }}</p>
      <button :disabled="busy">
        {{ busy ? "正在处理…" : setup ? "创建工作室并登录" : "登录" }}
      </button>
    </form>
    <p class="build-note">
      创建工作室后，即可新建小说、规划故事、管理角色和编辑正文。AI
      生成需要先配置模型与预算。
    </p>
  </main>
</template>
