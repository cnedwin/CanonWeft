import { createApp, h } from "vue";
import NovelWorkspace from "../../src/features/novel/NovelWorkspace.vue";
import NovelChapterEditor from "../../src/features/novel/NovelChapterEditor.vue";
import "../../src/style.css";

const book = {
  id: "book",
  project_id: "project",
  branch_id: "branch",
  settings_revision: 1,
  stage: "WORLD_PLANNING",
  setup: {
    title: "雾城药师",
    genre: "悬疑",
    language: "简体中文",
    idea: "医者调查雾城旧案",
    conflict: "救人还是揭开真相",
    target_words: 300000,
    volume_count: 3,
    chapter_min: 2000,
    chapter_max: 4000,
    characters: [],
  },
};
const props = {
  book,
  project: { id: "project", name: "雾城药师", role: "owner", revision: 1 },
  actor: {
    id: "owner",
    workspace_id: "workspace",
    username: "owner",
    role: "owner",
  },
};
const editor = new URLSearchParams(location.search).has("editor");
createApp({
  render: () =>
    h(editor ? NovelChapterEditor : NovelWorkspace, {
      ...props,
      chapterId: "chapter",
    } as never),
}).mount("#app");
