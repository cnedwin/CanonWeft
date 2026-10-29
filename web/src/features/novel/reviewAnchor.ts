export interface QuotePosition {
  start: number;
  end: number;
  context: string;
}

// Textarea selections use UTF-16 offsets. Keep these separate from the
// code-point positions used in saved narrative evidence.
export function reviewQuotePositions(text: string, quote: string) {
  const positions: QuotePosition[] = [];
  let total = 0;
  if (!quote) return { positions, total };
  for (
    let start = text.indexOf(quote);
    start !== -1;
    start = text.indexOf(quote, start + 1)
  ) {
    total++;
    if (positions.length < 50) {
      const prefix = Array.from(text.slice(0, start)),
        suffix = Array.from(text.slice(start + quote.length));
      positions.push({
        start,
        end: start + quote.length,
        context: `${prefix.length > 35 ? "…" : ""}${prefix.slice(-35).join("")}【${quote}】${suffix.slice(0, 35).join("")}${suffix.length > 35 ? "…" : ""}`,
      });
    }
  }
  return { positions, total };
}
