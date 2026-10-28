package novel

import (
	"fmt"
	"novelstudio/internal/domain"
	"slices"
	"strings"
)

// Editorial preferences are authoring advice, never a prediction of acceptance.
func EditorialGuidance(platform, audience, focus string) (string, error) {
	if !slices.Contains([]string{"general", "fanqie", "qidian", "yuewen-female"}, platform) || !slices.Contains([]string{"general", "male", "female"}, audience) || len([]rune(focus)) > 1000 {
		return "", domain.ErrValidation
	}
	shared := "保留已确认主线、主题、人物身份和高光铺垫。先完成具体场景，再检查因果、重复、视角和人物声音。自然文风不是随机换词、故意错字或检测规避；不用AI概率或过审保证。稿件须由作者核对原创、专业事实和投稿要求。"
	channel := map[string]string{
		"general":       "按作者选定题材与目标读者判断节奏，不机械套用黄金三章或固定爽点间隔。",
		"fanqie":        "番茄官方质量要求强调可读性与逻辑，反对低质拼凑和同质化量产。开篇让读者看懂人物目标、阻力和继续阅读的理由；章末可留下行动、情感或问题的推进，不强行制造同一种悬念。",
		"qidian":        "起点是本次目标站点。以下是编辑建议而非统一签约标准：检查核心设定能否持续产生冲突、能力与代价是否一致、成长或任务是否获得阶段回报。不要把番茄的签约条件、更新奖励或篇幅门槛套用到起点。",
		"yuewen-female": "阅文女频需以实际投稿站点当前要求为准。以下是编辑建议：人物有主动选择、关系变化有具体事件支撑、情绪推进可感，职业和成长线不沦为口号。女频不等于只有恋爱，题材要求优先。",
	}[platform]
	reader := map[string]string{
		"general": "读者偏好未细分；依题材和作者要求平衡情节、情感、成长与群像。",
		"male":    "男频偏好仅作可调整的编辑侧重：检查目标、行动、阶段回报、资源或能力的约束及长线期待；不要求主角无敌，不削弱感情和配角动机。",
		"female":  "女频偏好仅作可调整的编辑侧重：检查主体性、关系张力、选择的情感代价、人物边界及成长；不强制恋爱，不将所有女性角色写成同一声音。",
	}[audience]
	return fmt.Sprintf("编辑偏好版本 2026-10-27。%s\n%s\n%s\n作者题材重点：%s", shared, channel, reader, strings.TrimSpace(focus)), nil
}
