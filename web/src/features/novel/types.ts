export interface InitialCharacter {
  name: string;
  identity: string;
  goal: string;
  background: string;
  personality: string;
}
export interface NovelSetup {
  preset_id?: string;
  preset_revision?: string;
  title: string;
  genre: string;
  language: string;
  source_mode: string;
  source_text: string;
  ip_mode: string;
  project_id: string;
  branch_id: string;
  base_head: string;
  idea: string;
  conflict: string;
  audience: string;
  world: string;
  theme: string;
  include: string;
  avoid: string;
  perspective: string;
  style: string;
  target_words: number;
  volume_count: number;
  chapter_min: number;
  chapter_max: number;
  character_mode: string;
  characters: InitialCharacter[];
  creation_mode: string;
  model_choice: string;
  connection_id: string;
  protection: string;
}
export interface NovelSetupPreset {
  id: string;
  revision: string;
  name: string;
  audience: string;
  description: string;
  example_idea: string;
  reader_promise: string;
  skill_names: string[];
}
export interface NovelSetupPresetPreview {
  preset_id: string;
  preset_revision: string;
  setup: NovelSetup;
  reader_promise: string;
  skill_names: string[];
  notes: string[];
}
export interface NovelDraft {
  id: string;
  revision: number;
  step: number;
  setup: NovelSetup;
  confirmed_novel_id: string;
}
export interface NovelBook {
  id: string;
  project_id: string;
  branch_id: string;
  settings_revision: number;
  stage: string;
  setup: NovelSetup;
}

export interface NovelSettingsDraft {
  revision: number;
  base_revision: number;
  setup: NovelSetup;
}
export interface NovelSettingsPreview {
  hash: string;
  draft_revision: number;
  base_revision: number;
  planning_changed: boolean;
  changes: {
    field: string;
    label: string;
    scope: string;
    before: string | number;
    after: string | number;
  }[];
  review_resources: { id: string; name: string; head: string }[];
}
export interface NovelSettingsVersion {
  revision: number;
  setup: NovelSetup;
}
