import re
from .models import Ledger, Lore


def issue(message, quote='', category='一致性', severity='blocker', suggestion='修改正文或账本后重新审查。'):
    return dict(severity=severity, category=category, message=message, quote=quote, suggestion=suggestion)


def validate_lore(lore):
    value = Lore.model_validate(lore).model_dump()
    ids = [x['id'] for x in value['entities']]
    if len(ids) != len(set(ids)) or any(not i.strip() for i in ids):
        raise ValueError('角色 ID 必须唯一且非空')
    for rule in value['rules']:
        if any(i not in ids for i in rule['known_by']):
            raise ValueError('设定的知情者必须使用已有角色 ID')
    return value


def check_candidate(body, ledger, lore, reviewed, critic_issues=(), plan=None):
    problems = list(critic_issues)
    if not reviewed:
        problems.append(issue('正文尚未完成独立审查和记账。'))
    if not body.strip():
        problems.append(issue('正文不能为空。'))
    if plan:
        target = plan.get('target_chars', 0)
        if isinstance(target, int) and target > 0 and len(re.sub(r'\s', '', body)) < target * 0.5:
            problems.append(issue('正文不足章卡目标字数的一半，可能被截断或只生成了片段。', category='完成度', suggestion='补齐完整章节，或明确调整章卡后重新审查。'))
        if plan.get('pov') not in {e['id'] for e in lore['entities']}:
            problems.append(issue('章卡视角引用了未登记人物。', category='视角'))
    try:
        ledger = Ledger.model_validate(ledger).model_dump()
    except Exception:
        return problems + [issue('账本格式不完整，不能进入正史。')], ledger
    ids = {e['id'] for e in lore['entities']}
    spans = [(m.start(), m.end()) for m in re.finditer('“[^”]*”|「[^」]*」|"[^"\n]*"', body)]
    for entry in ledger['entries']:
        if entry['subject'] not in ids or any(k not in ids for k in entry['known_by']):
            problems.append(issue('账本引用了未登记角色。', entry['quote']))
        quote = entry['quote']
        at = entry['start']
        if quote and body[at:at + len(quote)] != quote and body.count(quote) == 1:
            at = entry['start'] = body.index(quote)
        if not quote or at < 0 or body[at:at + len(quote)] != quote:
            problems.append(issue('账本证据无法定位到正文。', quote))
        elif entry['kind'] in ('fact', 'resource') and (any(a < at + len(quote) and at < b for a, b in spans) or re.search('谎称|撒谎|声称|猜测|以为', quote)):
            problems.append(issue('事实证据包含对白或主观判断；请选取引号外的独立叙述证据，或记为陈述、认知、推测。', quote))
        entry['source_anchor'] = {'start': at, 'end': at + len(quote), 'quote': quote}
    for thread in ledger['threads']:
        q = thread['quote']
        at = thread['start']
        if q and body[at:at + len(q)] != q and body.count(q) == 1:
            at = thread['start'] = body.index(q)
        if not q or at < 0 or body[at:at + len(q)] != q:
            problems.append(issue('伏笔缺少有效正文证据。', q))
    return problems, ledger


def raw_ledger(ledger):
    """Strip computed anchors before model validation; anchors are derived locally."""
    return {**ledger, 'entries': [{k: v for k, v in e.items() if k != 'source_anchor'} for e in ledger.get('entries', [])]}
