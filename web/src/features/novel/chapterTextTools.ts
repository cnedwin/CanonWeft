export const CHAPTER_MATCH_LIMIT = 5000;
const CHAPTER_CHARACTER_LIMIT = 100000;

export interface ChapterTextMatch {
  start: number;
  end: number;
}
export interface ChapterTextMatches {
  total: number;
  matches: ChapterTextMatch[];
  truncated: boolean;
}
export interface ChapterReplacement {
  sourceText: string;
  text: string;
  query: string;
  replacement: string;
  mode: "one" | "all";
  changes: ChapterTextMatch[];
}
export interface ChapterTextChange {
  context: string;
  sourceText: string;
  text: string;
  action: "replace" | "undo";
}

function requireUnicode(value: string) {
  for (let index = 0; index < value.length; index++) {
    const code = value.charCodeAt(index);
    if (code >= 0xd800 && code <= 0xdbff) {
      const next = value.charCodeAt(++index);
      if (!(next >= 0xdc00 && next <= 0xdfff))
        throw new Error("文字含有不完整字符，请重新输入或粘贴。");
    } else if (code >= 0xdc00 && code <= 0xdfff)
      throw new Error("文字含有不完整字符，请重新输入或粘贴。");
  }
}

function requireSelection(text: string, start: number, end: number) {
  if (
    !Number.isInteger(start) ||
    !Number.isInteger(end) ||
    start < 0 ||
    end < start ||
    end > text.length
  )
    throw new Error("选区位置已失效，请重新定位。");
  for (const offset of [start, end]) {
    const code = text.charCodeAt(offset);
    if (code >= 0xdc00 && code <= 0xdfff)
      throw new Error("选区不能拆开完整字符。");
  }
}

export function chapterTextareaSelection(
  text: string,
  start: number,
  end: number,
) {
  requireUnicode(text);
  requireSelection(text, start, end);
  let visibleStart = text.slice(0, start).replace(/\r\n?/g, "\n").length;
  if (text[start] === "\n" && text[start - 1] === "\r") visibleStart--;
  return {
    text: text.replace(/\r\n?/g, "\n"),
    start: visibleStart,
    end:
      start === end
        ? visibleStart
        : text.slice(0, end).replace(/\r\n?/g, "\n").length,
  };
}

export function chapterSourceSelection(
  text: string,
  start: number,
  end: number,
) {
  requireUnicode(text);
  requireSelection(text.replace(/\r\n?/g, "\n"), start, end);
  function rawOffset(target: number) {
    let raw = 0;
    for (let visible = 0; visible < target; visible++) {
      raw += text[raw] === "\r" && text[raw + 1] === "\n" ? 2 : 1;
    }
    return raw;
  }
  return { start: rawOffset(start), end: rawOffset(end) };
}

// Offsets match textarea selectionStart/End (UTF-16), with no normalization.
export function findChapterMatches(
  text: string,
  query: string,
): ChapterTextMatches {
  requireUnicode(text);
  requireUnicode(query);
  const matches: ChapterTextMatch[] = [];
  let total = 0;
  if (query) {
    let from = 0;
    while (from <= text.length) {
      const start = text.indexOf(query, from);
      if (start < 0) break;
      const end = start + query.length;
      total++;
      if (matches.length < CHAPTER_MATCH_LIMIT) matches.push({ start, end });
      from = end;
    }
  }
  return { total, matches, truncated: total > matches.length };
}

export function previewChapterReplacement(
  text: string,
  query: string,
  replacement: string,
  mode: "one" | "all",
  index = 0,
): ChapterReplacement {
  requireUnicode(replacement);
  if (!query) throw new Error("请先填写要查找的原文。");
  if (query === replacement)
    throw new Error("替换内容与原文相同，没有需要修改的文字。");
  const result = findChapterMatches(text, query);
  if (!result.total) throw new Error("当前正文没有匹配内容，请重新查找。");
  if (mode !== "one" && mode !== "all") throw new Error("替换范围无效。");
  if (mode === "all" && result.truncated)
    throw new Error("命中超过定位上限，请缩小查找范围后再替换全部。");
  if (!Number.isInteger(index) || index < 0 || index >= result.matches.length)
    throw new Error("当前匹配位置已失效，请重新选择。");
  const changes = mode === "all" ? result.matches : [result.matches[index]!];
  const nextLength =
    [...text].length +
    changes.length * ([...replacement].length - [...query].length);
  if (nextLength > CHAPTER_CHARACTER_LIMIT)
    throw new Error("替换后的正文超过本章 100000 字上限，请缩小范围。");
  const pieces: string[] = [];
  let from = 0;
  for (const match of changes) {
    pieces.push(text.slice(from, match.start), replacement);
    from = match.end;
  }
  pieces.push(text.slice(from));
  const next = pieces.join("");
  return { sourceText: text, text: next, query, replacement, mode, changes };
}
