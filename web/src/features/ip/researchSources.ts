export const researchSources = [
  {
    id: "wikipedia",
    label: "维基百科 · 术语线索",
    languages: ["zh", "en", "ja"],
  },
  { id: "europe_pmc", label: "Europe PMC · 开放论文", languages: ["en"] },
  {
    id: "official_reference",
    label: "官方原文链接 · 有限摘录",
    languages: ["zh", "en"],
  },
  {
    id: "wikidata_names",
    label: "Wikidata · 姓名素材",
    languages: ["zh", "en", "ja", "fr", "de"],
  },
];
export function researchSourceLabel(id?: string) {
  return (
    researchSources.find((source) => source.id === (id || "wikipedia"))
      ?.label ?? "未知来源"
  );
}
export function safeResearchLink(url: string) {
  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" &&
      !parsed.username &&
      !parsed.password &&
      [
        "zh.wikipedia.org",
        "en.wikipedia.org",
        "ja.wikipedia.org",
        "europepmc.org",
        "www.wikidata.org",
        "www.gov.cn",
        "www.npc.gov.cn",
        "www.court.gov.cn",
        "www.stats.gov.cn",
        "www.nist.gov",
        "www.archives.gov",
        "www.loc.gov",
      ].includes(parsed.host)
      ? parsed.href
      : undefined;
  } catch {
    return undefined;
  }
}
