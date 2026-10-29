export interface ManuscriptChapter {
  id: string;
  resource_id: string;
  volume_id: string;
  volume: string;
  title: string;
  hash: string;
  head: string;
  revision: number;
  characters: number;
  included: boolean;
  warning: string;
}

export interface ManuscriptPreview {
  hash: string;
  title: string;
  source: string;
  chapters: ManuscriptChapter[];
  included: number;
  missing: number;
  warnings: number;
  characters: number;
  edition_id?: string;
  edition_metadata?: { author: string; language: string; description: string };
  cover?: {
    image_id: string;
    release_id: string;
    hash: string;
    name: string;
    mime: string;
    width: number;
    height: number;
    warning?: string;
  };
}

export interface SavedEdition {
  id: string;
  name: string;
  title: string;
  published: boolean;
}

export type ManuscriptPreviewInput =
  | {
      source: "draft" | "published";
      language: string;
      author?: string;
      volume_id?: string;
    }
  | { source: "edition"; edition_id: string };
