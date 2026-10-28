import { test, expect } from "@playwright/test";
import {
  characterFacts,
  characterMatches,
  characterRelations,
} from "../src/features/ip/characterLibrary";
import type { Assertion, Snapshot } from "../src/features/ip/types";

test("角色属性与关系不混入未来、过期、撤回、争议和非事实记录", () => {
  const fact: Assertion = {
    id: "edge",
    subject: "a",
    object_id: "b",
    predicate: "盟友",
    value: "同行",
    kind: "fact",
    effective_from: 3,
    effective_until: 5,
    known_from: 3,
    known_by: [],
    visibility: "public",
    recorded_at: "",
    locked: false,
    source: "作者",
  };
  const snapshot: Snapshot = {
    entities: [
      {
        id: "a",
        kind: "character",
        name: "林舟",
        aliases: ["青衣客"],
        description: "医师",
      },
      {
        id: "b",
        kind: "character",
        name: "林舟",
        aliases: [],
        description: "另一身份",
      },
      {
        id: "c",
        kind: "faction",
        name: "药师会",
        aliases: [],
        description: "",
      },
    ],
    assertions: [
      fact,
      { ...fact, id: "rumor", context: "rumor" },
      { ...fact, id: "belief", kind: "belief" },
      { ...fact, id: "retracted", status: "retracted" },
      { ...fact, id: "disputed", status: "disputed" },
      { ...fact, id: "faction", object_id: "c" },
      { ...fact, id: "dangling", object_id: "missing" },
    ],
  };
  expect(characterFacts(snapshot, 2)).toHaveLength(0);
  expect(characterFacts(snapshot, 5)).toHaveLength(0);
  expect(characterFacts(snapshot, 3).map((a) => a.id)).toEqual([
    "edge",
    "faction",
    "dangling",
  ]);
  expect(characterRelations(snapshot, 3, "a").map((a) => a.id)).toEqual([
    "edge",
  ]);
  expect(characterRelations(snapshot, 3, "b")[0]?.subject).toBe("a");
  expect(characterMatches(snapshot.entities[0]!, [], "青衣客 医师")).toBe(true);
  expect(characterMatches(snapshot.entities[1]!, [], "青衣客")).toBe(false);
});
