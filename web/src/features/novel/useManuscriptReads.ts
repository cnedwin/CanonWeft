import { onBeforeUnmount } from "vue";
import { APIError } from "../../api";
import type {
  ManuscriptPreview,
  ManuscriptPreviewInput,
} from "./manuscriptTypes";

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function validPreview(value: unknown): value is ManuscriptPreview {
  if (!record(value)) return false;
  if (
    !["hash", "title", "source"].every(
      (key) => typeof value[key] === "string",
    ) ||
    !["included", "missing", "warnings", "characters"].every(
      (key) => Number.isSafeInteger(value[key]) && Number(value[key]) >= 0,
    ) ||
    !Array.isArray(value.chapters)
  )
    return false;
  const ids = new Set<string>();
  for (const chapter of value.chapters) {
    if (
      !record(chapter) ||
      ![
        "id",
        "resource_id",
        "volume_id",
        "volume",
        "title",
        "hash",
        "head",
        "warning",
      ].every((key) => typeof chapter[key] === "string") ||
      !chapter.id ||
      ids.has(String(chapter.id)) ||
      typeof chapter.included !== "boolean" ||
      !["revision", "characters"].every(
        (key) =>
          Number.isSafeInteger(chapter[key]) && Number(chapter[key]) >= 0,
      )
    )
      return false;
    ids.add(String(chapter.id));
  }
  if (value.edition_id !== undefined && typeof value.edition_id !== "string")
    return false;
  const metadata = value.edition_metadata;
  if (
    metadata !== undefined &&
    (!record(metadata) ||
      !["author", "language", "description"].every(
        (key) => typeof metadata[key] === "string",
      ))
  )
    return false;
  if (value.cover !== undefined) {
    const cover = value.cover;
    if (
      !record(cover) ||
      !["image_id", "release_id", "hash", "name", "mime"].every(
        (key) => typeof cover[key] === "string",
      ) ||
      !["width", "height"].every(
        (key) => Number.isSafeInteger(cover[key]) && Number(cover[key]) > 0,
      ) ||
      (cover.warning !== undefined && typeof cover.warning !== "string")
    )
      return false;
  }
  return true;
}

// This POST only reads the existing export snapshot. Its deadline must never
// be reused for task creation, publication, or any other write endpoint.
export function useManuscriptReads(bookId: string) {
  let disposed = false;
  const pending = new Set<AbortController>();
  function cancel() {
    for (const controller of pending) controller.abort();
  }
  onBeforeUnmount(() => {
    disposed = true;
    cancel();
  });

  async function preview(
    input: ManuscriptPreviewInput,
  ): Promise<ManuscriptPreview> {
    if (disposed) throw new APIError("已离开稿件页面，请重新打开。", 0);
    const controller = new AbortController();
    pending.add(controller);
    let timedOut = false;
    let status = 0;
    function interruption() {
      if (status === 401) return new APIError("登录已失效，请重新登录。", 401);
      if (status === 403)
        return new APIError("当前账号无权读取本书稿件。", 403);
      return new APIError(
        timedOut
          ? "读取稿件目录超时，原稿保持原样，可重新读取。"
          : "已停止读取稿件目录。",
        0,
      );
    }
    let abort: () => void = () => {};
    const interrupted = new Promise<never>((_, reject) => {
      abort = () => reject(interruption());
      controller.signal.addEventListener("abort", abort, { once: true });
    });
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, 15000);
    const read = async () => {
      let response: Response;
      try {
        response = await fetch(
          `/api/novels/${encodeURIComponent(bookId)}/export/preview`,
          {
            method: "POST",
            credentials: "same-origin",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(input),
            signal: controller.signal,
          },
        );
      } catch {
        if (controller.signal.aborted) throw interruption();
        throw new APIError("暂时无法读取稿件目录，可重新读取。", 0);
      }
      status = response.status;
      // A known denial is authoritative even if its body never arrives.
      if (status === 401 || status === 403) {
        controller.abort();
        throw interruption();
      }
      let data: unknown;
      try {
        data = await response.json();
      } catch {
        if (controller.signal.aborted) throw interruption();
        throw new APIError(
          response.ok
            ? "稿件目录数据暂不可读取，可重新读取。"
            : "稿件目录暂不可读取。",
          response.ok ? 0 : status,
        );
      }
      if (!response.ok)
        throw new APIError(
          record(data) && typeof data.error === "string"
            ? data.error
            : "稿件目录暂不可读取。",
          status,
        );
      if (!validPreview(data))
        throw new APIError("稿件目录数据不完整，请重新读取。", 0);
      if (
        data.source !== input.source ||
        (input.source === "edition" && data.edition_id !== input.edition_id) ||
        (input.source !== "edition" && !!data.edition_id)
      )
        throw new APIError("读取的稿件来源与所选内容不一致，请重新读取。", 0);
      return data;
    };
    try {
      return await Promise.race([read(), interrupted]);
    } finally {
      clearTimeout(timer);
      controller.signal.removeEventListener("abort", abort);
      pending.delete(controller);
    }
  }

  return { preview, active: () => !disposed, cancel };
}
