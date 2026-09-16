// Embedded into the existing authenticated UI at build time. All remote text
// goes through DOM text nodes; this module never stores keys or starts polling.
let gbData = { groups: [], plans: [], labels: [], audit: [], cursor: null };
function clearGroupBilling() {
  gbData = { groups: [], plans: [], labels: [], audit: [], cursor: null };
  for (const id of ["gb-groups", "gb-plans", "gb-key", "gb-usage", "gb-labels", "gb-audit", "gb-editor", "account-group-usage"]) $(id).replaceChildren();
  $("gb-dialog").close();
}
const gbButton = (text, action) => el("button", { type: "button", text, onclick: () => guard(action) });
const gbField = (label, node) => el("label", {}, el("span", { text: label }), node);
const gbText = (value = "", maxLength = 80) => el("input", { type: "text", value, maxLength });
const gbCheck = (label, checked) => {
  const input = el("input", { type: "checkbox", checked });
  return { input, node: el("label", { class: "gb-check" }, input, label) };
};
function gbSelect(items, selected, multiple = false) {
  const input = el("select", { multiple });
  for (const [value, text] of items) input.append(el("option", { value, text, selected: multiple ? selected.includes(value) : selected === value }));
  return input;
}
const gbSelected = (node) => Array.from(node.selectedOptions, (option) => option.value);
function gbEditor(title, fields, save, buttonText = "保存") {
  const dialog = $("gb-dialog"), generation = sessionGeneration;
  const error = el("p", { role: "alert", class: "bad" });
  const cancel = gbButton("取消", () => dialog.close());
  const button = gbButton(buttonText, async () => {
    if (generation !== sessionGeneration || currentRole !== "admin") return dialog.close();
    button.disabled = true; cancel.disabled = true; error.textContent = "";
    try {
      await save();
      if (generation !== sessionGeneration) return;
      await loadGroupBilling(); dialog.close(); notify("操作已保存");
    } catch (err) { if (generation === sessionGeneration) error.textContent = err.message || String(err); }
    finally { button.disabled = false; cancel.disabled = false; }
  });
  $("gb-editor").replaceChildren(el("h2", { text: title }), ...fields, error, el("div", { class: "row spread" }, cancel, button));
  if (!dialog.open) dialog.showModal();
}
function gbRequire(input, name) { const value = input.value.trim(); if (!value) throw new Error("请填写" + name); return value; }
async function loadGroupBilling() {
  const generation = sessionGeneration;
  $("gb-status").textContent = "正在加载真实配置…";
  try {
    await loadDataGroup(false, "configuration", true);
    const [groups, plans, labels, status, audit] = await Promise.all([
      plugin("GET", "/v1/groups"), plugin("GET", "/v1/plans"), plugin("GET", "/v1/shared-labels"),
      plugin("GET", "/v1/integrations/keeper/status"), plugin("GET", "/v1/audit")
    ]);
    if (generation !== sessionGeneration || currentRole !== "admin") return;
    gbData = { groups: groups.items, plans: plans.items, labels: labels.items, status, labelsStale: labels.stale, audit: audit.items, cursor: audit.next_cursor };
    gbRender(); await gbLoadUsage();
    $("gb-status").textContent = "配置已加载；新绑定于北京时间次日零点生效。";
  } catch (error) {
    if (generation === sessionGeneration) $("gb-status").textContent = "加载失败，请刷新重试：" + error.message;
    throw error;
  }
}
function gbRender() {
  $("gb-groups").replaceChildren(...gbData.groups.map((g) => {
    const actions = el("div", { class: "row", style: "flex-wrap:wrap;gap:8px" }, gbButton("编辑", () => gbEditGroup(g)));
    if (g.status === "draft") actions.append(gbButton("发布", () => gbPublish(g, false)));
    if (g.status === "published") actions.append(gbButton("归档", () => gbPublish(g, true)));
    return el("div", { class: "card" }, el("h3", { text: g.name }),
      el("p", { class: "muted", text: ({ draft: "草稿", published: "已发布", archived: "已归档" }[g.status]) + (g.enabled ? " · 启用" : " · 停用") + " · r" + g.revision }),
      el("p", { text: g.models.join(" · ") }),
      el("p", { class: "muted", text: g.pool.mode === "inherit" ? "继承 CPA 候选池，再应用排除项" : "指定凭证 / 供应商池，与 Key 原权限取交集" }), actions);
  }));
  if (!gbData.groups.length) $("gb-groups").textContent = "尚无资源组。请从 CPA 模型目录选择或输入精确模型 ID。";
  $("gb-plans").replaceChildren(...gbData.plans.map((p) => el("div", { class: "card" }, el("h3", { text: p.name }),
    ...p.policies.map((v) => el("p", { text: (gbData.groups.find((g) => g.id === v.group_id)?.name || v.group_id) + "：" + (v.enabled ? "$" + (v.daily_limit_usd ?? "不限") + " / 日，并发 " + (v.concurrency_limit ?? "不限") : "未开放") })),
    el("p", { class: "muted", text: p.bound_key_count + " 个当前绑定 Key · r" + p.revision }), gbButton("编辑计划", () => gbEditPlan(p)))));
  if (!gbData.plans.length) $("gb-plans").textContent = "发布资源组后创建计划；未列入计划的组默认拒绝访问。";
  const selected = $("gb-key").value;
  $("gb-key").replaceChildren(...(resources.admin.keys.value || []).filter((k) => !k.deleted_at).map((k) => el("option", { value: k.scope, text: apiKeyIdentity(k).text, selected: selected === k.scope })));
  $("gb-keeper-status").textContent = !gbData.status.enabled
    ? "未启用：在插件配置设置 keeper_url 与 keeper_password_env。密码只放服务器环境变量。"
    : (gbData.labelsStale ? "备注缓存可能过期，需同步核对。" : "共享备注已同步。") + (gbData.status.error || "") + " Keeper 没有原子版本锁，同时编辑时最后写入者生效。";
  $("gb-labels").replaceChildren(...gbData.labels.map((l) => {
    const key = (resources.admin.keys.value || []).find((k) => k.scope === l.subject_id);
    const credential = (resources.admin.credentials.value || []).find((c) => c.ref === l.subject_id);
    const name = key ? apiKeyIdentity(key).text : credential?.display_name || l.subject_id;
    return el("div", { class: "card" }, el("h3", { text: (l.subject_kind === "downstream_key" ? "Key · " : "凭证 · ") + name }),
      el("p", { text: l.value || "（空备注）" }),
      ...(l.source_note ? [el("p", { class: "muted", text: "CPA 原始 note（只读）：" + l.source_note })] : []), gbButton("修改共享备注", () => gbEditLabel(l)));
  }));
  gbRenderAudit();
}
function gbEditGroup(group) {
  const name = gbText(group?.name), description = gbText(group?.description, 1000), reason = gbText("", 500);
  const enabled = gbCheck("启用此组", group?.enabled ?? true);
  const models = el("textarea", { value: (group?.models || []).join("\n"), disabled: !!group && group.status !== "draft", placeholder: "每行一个精确模型 ID（支持别名，不猜测前缀）" });
  models.value = (group?.models || []).join("\n");
  const catalog = gbSelect((resources.admin.models.value || []).map((id) => [id, id]), [], true);
  const append = gbButton("加入已选模型", () => { models.value = [...new Set([...models.value.split("\n").filter(Boolean), ...gbSelected(catalog)])].join("\n"); });
  append.disabled = models.disabled;
  const pool = group?.pool || { mode: "inherit" };
  const mode = gbSelect([["inherit", "继承候选池"], ["selected", "指定允许池"]], pool.mode);
  const credentials = resources.admin.credentials.value || [];
  const options = credentials.map((c) => [c.ref, c.provider + " · " + c.display_name]);
  const classes = [...new Map(credentials.map((c) => [JSON.stringify({ source: c.source, provider: c.provider }), c])).entries()]
    .map(([id, c]) => [id, (c.source === "auth-files" ? "凭证管理" : "AI 供应商") + " · " + c.provider]);
  for (const selector of [...(pool.allow_classes || []), ...(pool.deny_classes || [])]) {
    const id = JSON.stringify(selector); if (!classes.some(([value]) => value === id)) classes.push([id, "暂不可用 · " + selector.provider]);
  }
  for (const ref of [...(pool.allow_refs || []), ...(pool.deny_refs || [])]) if (!options.some(([value]) => value === ref)) options.push([ref, "暂不可用 · " + ref]);
  const allow = gbSelect(options, pool.allow_refs || [], true), deny = gbSelect(options, pool.deny_refs || [], true);
  const allowClass = gbSelect(classes, (pool.allow_classes || []).map((v) => JSON.stringify(v)), true);
  const denyClass = gbSelect(classes, (pool.deny_classes || []).map((v) => JSON.stringify(v)), true);
  const sync = () => { allow.disabled = allowClass.disabled = mode.value !== "selected"; }; mode.onchange = sync; sync();
  gbEditor(group ? "编辑资源组" : "新建资源组", [gbField("组名", name), gbField("描述", description), enabled.node,
    gbField("CPA 模型目录（多选）", catalog), append, gbField("组内模型：发布后成员冻结", models), gbField("凭证池模式", mode),
    gbField("允许供应商 / 凭证类别（包含未来新增凭证）", allowClass), gbField("允许指定凭证", allow),
    gbField("排除供应商 / 类别", denyClass), gbField("排除指定凭证", deny),
    el("p", { class: "muted", text: "多选可按 Ctrl / Cmd；手机使用系统选择器。排除优先，不会跨组回退。" }),
    ...(group ? [gbField("修改理由", reason)] : [])], async () => {
    const body = { name: gbRequire(name, "组名"), description: description.value, pool: {
      mode: mode.value, allow_refs: mode.value === "selected" ? gbSelected(allow) : [],
      allow_classes: mode.value === "selected" ? gbSelected(allowClass).map((v) => JSON.parse(v)) : [],
      deny_refs: gbSelected(deny), deny_classes: gbSelected(denyClass).map((v) => JSON.parse(v))
    } };
    if (!group || group.status === "draft") body.models = models.value.split("\n").map((v) => v.trim()).filter(Boolean);
    if (group) Object.assign(body, { group_id: group.id, expected_revision: group.revision, enabled: enabled.input.checked, reason: gbRequire(reason, "修改理由") });
    await plugin(group ? "PATCH" : "POST", "/v1/groups", body);
  });
}
function gbPublish(group, archive) {
  const reason = gbText("", 500);
  gbEditor(archive ? "归档资源组" : "发布资源组", [el("p", { text: group.name + "：" + group.models.join("、") }),
    el("p", { class: "muted", text: archive ? "归档后拒绝新请求，历史账本和模型归属仍保留。" : "发布后模型归属不可直接修改；请确认 ID 与 CPA 客户端请求别名一致。" }),
    gbField("操作理由", reason)], () => plugin("POST", "/v1/groups/" + (archive ? "archive" : "publish"),
    { group_id: group.id, expected_revision: group.revision, reason: gbRequire(reason, "操作理由") }), archive ? "确认归档" : "确认发布");
}
function gbEditPlan(plan) {
  const name = gbText(plan?.name), reason = gbText("", 500), rows = [];
  const fields = [gbField("计划名称", name), el("p", { class: "muted", text: "纳入计划后配置每组政策。不限必须明确勾选；修改计划立即影响所有绑定 Key，但不会清空今日用量。" })];
  for (const group of gbData.groups.filter((g) => g.status === "published" || plan?.policies.some((p) => p.group_id === g.id))) {
    const policy = plan?.policies.find((p) => p.group_id === group.id);
    const include = gbCheck("纳入 " + group.name, !!policy), enabled = gbCheck("开放访问", policy?.enabled ?? true);
    const daily = gbText(policy?.daily_limit_usd ?? "", 30);
    const concurrency = el("input", { type: "number", min: 1, max: 10000, step: 1, value: policy?.concurrency_limit ?? "" });
    const noMoney = gbCheck("日额度不限", !!policy && policy.daily_limit_usd === null);
    const noSlots = gbCheck("并发不限", !!policy && policy.concurrency_limit === null);
    const sync = () => { daily.disabled = !include.input.checked || noMoney.input.checked; concurrency.disabled = !include.input.checked || noSlots.input.checked; };
    for (const checkbox of [include, noMoney, noSlots]) checkbox.input.onchange = sync; sync();
    fields.push(el("div", { class: "card" }, include.node, enabled.node, gbField("每人每日 USD（十进制）", daily), noMoney.node, gbField("每人同时并发", concurrency), noSlots.node));
    rows.push({ group, include, enabled, daily, concurrency, noMoney, noSlots });
  }
  if (plan) fields.push(gbField("修改理由", reason));
  gbEditor(plan ? "编辑分组计划" : "创建分组计划", fields, async () => {
    const policies = rows.filter((r) => r.include.input.checked).map((r) => {
      const amount = r.noMoney.input.checked ? null : gbRequire(r.daily, r.group.name + "日额度");
      const slots = r.noSlots.input.checked ? null : Number(gbRequire(r.concurrency, r.group.name + "并发"));
      if (slots !== null && (!Number.isInteger(slots) || slots < 1 || slots > 10000)) throw new Error("并发必须为 1–10000 的整数");
      return { group_id: r.group.id, enabled: r.enabled.input.checked, daily_limit_usd: amount, concurrency_limit: slots };
    });
    if (!policies.length) throw new Error("请至少纳入一个已发布资源组");
    await plugin(plan ? "PUT" : "POST", "/v1/plans", { name: gbRequire(name, "计划名称"), policies,
      ...(plan ? { plan_id: plan.id, expected_revision: plan.revision, reason: gbRequire(reason, "修改理由") } : {}) });
  });
}
async function gbBind() {
  const scope = $("gb-key").value; if (!scope) throw new Error("先选择一个已同步的 Key");
  const binding = await plugin("GET", "/v1/key-plan-bindings?scope=" + encodeURIComponent(scope));
  const plan = gbSelect([["", "恢复旧版计费模式"], ...gbData.plans.map((p) => [p.id, p.name])], binding.pending?.plan_id || binding.active?.plan_id || "");
  const reason = gbText("", 500), preview = el("p", { class: "muted" });
  const body = () => ({ scope, mode: plan.value ? "grouped" : "legacy", plan_id: plan.value || null, expected_revision: binding.revision, effective: "next_day", reason: gbRequire(reason, "修改理由") });
  const button = gbButton("预览变更", async () => { const value = await plugin("POST", "/v1/key-plan-bindings/preview", body()); preview.textContent = "生效时间：" + new Date(value.binding.effective_from).toLocaleString() + "；" + value.warnings.join("；"); });
  gbEditor("安排 Key 计划变更", [el("p", { text: $("gb-key").selectedOptions[0]?.textContent }), gbField("目标计划", plan),
    gbField("修改理由", reason), button, preview,
    el("p", { class: "muted", text: "北京时间次日零点生效。分组模式替代旧套餐额度和旧全局并发，保留原路由权限。" })],
    () => plugin("PUT", "/v1/key-plan-bindings", body()), "安排次日生效");
}
function groupUsageCard(v, admin) {
  const warning = !v.accounting_complete ? "核算不完整，请核对缺价 / 未归类记录" : v.admission;
  const card = el("div", { class: "card" }, el("h3", { text: v.group_name }),
    el("p", { text: "$" + v.used_usd + " / " + (v.daily_limit_usd === null ? "不限" : "$" + v.daily_limit_usd) + " · " + v.business_date }),
    el("p", { text: "当前并发 " + v.active_requests + " / " + (v.concurrency_limit ?? "不限") }),
    el("p", { class: "muted", text: v.requests + " 请求 · " + v.tokens + " tokens · " + warning }),
    el("p", { class: "muted", text: "北京时间次日零点重置；在途请求可能超额" }));
  if (admin) card.append(gbButton("重置本组今日额度", () => {
    const reason = gbText("", 500), scope = $("gb-key").value;
    gbEditor("重置 " + v.group_name, [el("p", { text: "以当前原始费用作为抵扣基线，保留明细；重置前启动的延迟 usage 不消耗新额度。" }), gbField("重置理由", reason)],
      () => plugin("POST", "/v1/keys/group-reset", { scope, group_id: v.group_id, reason: gbRequire(reason, "重置理由") }));
  }));
  return card;
}
async function gbLoadUsage() {
  const scope = $("gb-key").value, generation = sessionGeneration;
  $("gb-usage").replaceChildren(); $("gb-binding").textContent = "";
  if (!scope) return;
  const [usage, binding] = await Promise.all([plugin("GET", "/v1/keys/group-usage?scope=" + encodeURIComponent(scope)), plugin("GET", "/v1/key-plan-bindings?scope=" + encodeURIComponent(scope))]);
  if (generation !== sessionGeneration || scope !== $("gb-key").value) return;
  $("gb-binding").textContent = "当前：" + (binding.active?.mode === "grouped" ? "分组计划 " + binding.active.plan_id : "旧版模式")
    + (binding.pending ? "；待生效：" + (binding.pending.plan_id || "旧版模式") + " @ " + new Date(binding.pending.effective_from).toLocaleString() : "")
    + (usage.unclassified_records ? "；未归类记录 " + usage.unclassified_records : "");
  $("gb-usage").replaceChildren(...usage.groups.map((v) => groupUsageCard(v, true)));
}
function gbEditLabel(label) {
  const input = gbText(label.value, label.subject_kind === "downstream_key" ? 128 : 50);
  gbEditor("修改 Keeper 共享备注", [gbField("备注（留空清除）", input), el("p", { class: "muted", text: "先读取 Keeper 检查旧值；同时编辑仍可能被后写入者覆盖。" })],
    () => plugin("PATCH", "/v1/shared-labels", { subject_kind: label.subject_kind, subject_id: label.subject_id, value: input.value, expected_value: label.value }));
}
function gbRenderAudit() {
  $("gb-audit").replaceChildren(...gbData.audit.map((entry) => el("details", {},
    el("summary", { text: new Date(entry.at).toLocaleString() + " · " + entry.action + " · " + entry.object_type + " · " + (entry.reason || "创建") }),
    el("pre", { text: JSON.stringify({ before: entry.before, after: entry.after }, null, 2) }))));
  $("gb-more-audit").classList.toggle("hidden", !gbData.cursor);
}
$("gb-new-group").onclick = () => gbEditGroup();
$("gb-new-plan").onclick = () => gbEditPlan();
$("gb-bind").onclick = () => guard(gbBind);
$("gb-key").onchange = () => guard(gbLoadUsage);
$("gb-refresh-labels").onclick = () => guard(async () => { await plugin("POST", "/v1/integrations/keeper/refresh", {}); await loadGroupBilling(); });
$("gb-more-audit").onclick = () => guard(async () => {
  const generation = sessionGeneration;
  const data = await plugin("GET", "/v1/audit?cursor=" + encodeURIComponent(gbData.cursor));
  if (generation !== sessionGeneration) return;
  gbData.audit.push(...data.items); gbData.cursor = data.next_cursor; gbRenderAudit();
});
