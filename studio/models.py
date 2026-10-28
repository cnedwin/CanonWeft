from typing import Literal
from pydantic import BaseModel, ConfigDict, Field


class StrictModel(BaseModel):
    model_config = ConfigDict(extra='forbid')


class Entity(StrictModel):
    id: str
    name: str
    aliases: list[str]
    description: str


class Rule(StrictModel):
    id: str
    text: str
    known_by: list[str]


class Lore(StrictModel):
    brief: str
    entities: list[Entity]
    rules: list[Rule]


class Plan(StrictModel):
    title: str
    pov: str
    story_time: str
    goal: str
    conflict: str
    beats: list[str]
    ending_hook: str
    target_chars: int


class Draft(StrictModel):
    title: str
    body: str


class Entry(StrictModel):
    kind: Literal['fact', 'belief', 'statement', 'intention', 'hypothesis', 'resource']
    subject: str
    predicate: str
    value: str
    story_time: str
    known_by: list[str]
    quote: str
    start: int


class Thread(StrictModel):
    name: str
    status: Literal['open', 'resolved']
    quote: str
    start: int


class Ledger(StrictModel):
    entries: list[Entry]
    threads: list[Thread]
    summary: str


class Issue(StrictModel):
    severity: Literal['blocker', 'warning', 'info']
    category: str
    message: str
    quote: str
    suggestion: str


class Critique(StrictModel):
    issues: list[Issue]
    assessment: str


OUTPUTS = {'architect': Lore, 'planner': Plan, 'writer': Draft, 'bookkeeper': Ledger, 'critic': Critique}
