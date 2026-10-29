export interface ModelRoleSetup {
  role: string;
  code: string;
  connection_id?: string;
  bound_revision?: number;
  current_revision?: number;
  price_id?: string;
  expires_at?: string;
}

export interface ModelSetupReport {
  checked_at: string;
  available: boolean;
  network_disabled: boolean;
  budget: {
    currency: string;
    enabled: boolean;
    configured: boolean;
    available_micros: number;
    per_call_micros: number;
  };
  roles: ModelRoleSetup[];
}

export type SetupTarget = "connections" | "bindings" | "prices" | "budget";
const explanations: Record<string, { text: string; target?: SetupTarget }> = {
  BINDING_MISSING: { text: "尚未选择此用途的模型", target: "bindings" },
  CONNECTION_MISSING: { text: "绑定的模型配置不存在", target: "bindings" },
  CONNECTION_DISABLED: { text: "模型配置已停用", target: "connections" },
  KEY_MISSING: { text: "尚未保存可用于此绑定的 Key", target: "connections" },
  KEY_CHANGED: {
    text: "Key 已变更，请核对并重新绑定当前版本",
    target: "bindings",
  },
  BINDING_INVALID: { text: "绑定版本无法核验，请核对配置", target: "bindings" },
  IMAGE_UNSUPPORTED: {
    text: "固定与当前版本没有共同的图片生成或编辑能力",
    target: "connections",
  },
  PRICE_MISSING: { text: "此固定版本尚无适用价格", target: "prices" },
  PRICE_EXPIRED: { text: "适用价格已过期", target: "prices" },
  PRICE_INVALID: { text: "价格内容无效，请核对来源与有效期", target: "prices" },
  PRICE_UNVERIFIED: {
    text: "价格记录无法核验，请核对并保存新价格",
    target: "prices",
  },
  CURRENCY_MISMATCH: {
    text: "适用价格与工作室预算币种不一致",
    target: "prices",
  },
  CONFIGURED: { text: "配置项完整" },
};
export function setupExplanation(code: string) {
  return explanations[code] ?? { text: "配置状态暂无法识别，请刷新后核对" };
}
