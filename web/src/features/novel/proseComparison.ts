export type ProseDifferenceKind = "same" | "removed" | "added";

export interface ProseDifferenceBlock {
  kind: ProseDifferenceKind;
  text: string;
}

export interface ProseComparison {
  equal: boolean;
  coarse: boolean;
  added: number;
  removed: number;
  blocks: ProseDifferenceBlock[];
}

// Bounds apply after common leading/trailing lines are removed. Full-book
// input is scanned linearly; only this small middle may allocate an LCS table.
const maxMiddleLines = 1000;
const maxMatrixCells = 200_000;

function originalLines(text: string): string[] {
  const lines: string[] = [];
  let start = 0;
  for (let index = 0; index < text.length; index++) {
    const character = text[index];
    if (!"\r\n\u2028\u2029".includes(character!)) continue;
    if (character === "\r" && text[index + 1] === "\n") index++;
    lines.push(text.slice(start, index + 1));
    start = index + 1;
  }
  if (start < text.length) lines.push(text.slice(start));
  return lines;
}

function appendBlock(
  blocks: ProseDifferenceBlock[],
  kind: ProseDifferenceKind,
  text: string,
) {
  if (!text) return;
  const last = blocks.at(-1);
  if (last?.kind === kind) last.text += text;
  else blocks.push({ kind, text });
}

function appendLineDifferences(
  blocks: ProseDifferenceBlock[],
  left: string[],
  right: string[],
) {
  const width = right.length + 1;
  const lengths = new Uint16Array((left.length + 1) * width);
  for (let i = left.length - 1; i >= 0; i--) {
    for (let j = right.length - 1; j >= 0; j--) {
      lengths[i * width + j] =
        left[i] === right[j]
          ? 1 + lengths[(i + 1) * width + j + 1]!
          : Math.max(
              lengths[(i + 1) * width + j]!,
              lengths[i * width + j + 1]!,
            );
    }
  }
  let i = 0;
  let j = 0;
  while (i < left.length || j < right.length) {
    if (i < left.length && j < right.length && left[i] === right[j]) {
      appendBlock(blocks, "same", left[i++]!);
      j++;
    } else if (
      i < left.length &&
      (j === right.length ||
        lengths[(i + 1) * width + j]! >= lengths[i * width + j + 1]!)
    ) {
      appendBlock(blocks, "removed", left[i++]!);
    } else {
      appendBlock(blocks, "added", right[j++]!);
    }
  }
}

function codePoints(text: string): number {
  let count = 0;
  // Iteration counts a surrogate pair once without building a character array.
  for (const character of text) {
    if (character) count++;
  }
  return count;
}

/**
 * Compare exact, unnormalised text at line granularity, with no IO or mutation
 * of either input. Counts cover changed line blocks (including whitespace and
 * line endings), not a minimal character edit distance. Each side can always
 * be reconstructed by concatenating same + removed or same + added blocks.
 */
export function compareProse(left: string, right: string): ProseComparison {
  if (left === right) {
    return {
      equal: true,
      coarse: false,
      added: 0,
      removed: 0,
      blocks: [{ kind: "same", text: left }],
    };
  }
  const before = originalLines(left);
  const after = originalLines(right);
  let prefix = 0;
  while (
    prefix < before.length &&
    prefix < after.length &&
    before[prefix] === after[prefix]
  ) {
    prefix++;
  }
  let leftEnd = before.length;
  let rightEnd = after.length;
  while (
    leftEnd > prefix &&
    rightEnd > prefix &&
    before[leftEnd - 1] === after[rightEnd - 1]
  ) {
    leftEnd--;
    rightEnd--;
  }
  const middleLeft = before.slice(prefix, leftEnd);
  const middleRight = after.slice(prefix, rightEnd);
  const coarse =
    middleLeft.length > maxMiddleLines ||
    middleRight.length > maxMiddleLines ||
    (middleLeft.length + 1) * (middleRight.length + 1) > maxMatrixCells;
  const blocks: ProseDifferenceBlock[] = [];
  appendBlock(blocks, "same", before.slice(0, prefix).join(""));
  if (coarse) {
    appendBlock(blocks, "removed", middleLeft.join(""));
    appendBlock(blocks, "added", middleRight.join(""));
  } else {
    appendLineDifferences(blocks, middleLeft, middleRight);
  }
  appendBlock(blocks, "same", before.slice(leftEnd).join(""));
  return {
    equal: false,
    coarse,
    added: blocks.reduce(
      (total, block) =>
        total + (block.kind === "added" ? codePoints(block.text) : 0),
      0,
    ),
    removed: blocks.reduce(
      (total, block) =>
        total + (block.kind === "removed" ? codePoints(block.text) : 0),
      0,
    ),
    blocks,
  };
}
