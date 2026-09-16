# 已实现接口 · 1.3.13-group.1

以下是本二开包的实际接口，不是上游原版接口。静态路径匹配 CPA 的插件注册机制，不使用 `/groups/:id`。

管理前缀 `M = /v0/management/plugins/cpa-key-billing/v1`；由 CPA 验证管理密钥 `Authorization: Bearer <管理密钥>`。请求/响应均 JSON。普通 Key 不能访问管理接口。

## 分组与计划

| 方法 / 相对 M 路径 | 输入 | 返回 |
| --- | --- | --- |
| GET `/groups` | 可选 query `status=draft/published/archived` | `{items: Group[]}` |
| POST `/groups` | `{name, description?, models: string[], pool}` | 新 `Group`，默认草稿、启用 |
| PATCH `/groups` | `{group_id, expected_revision, reason, name?, description?, enabled?, models?, pool?}` | 更新后的 `Group` |
| POST `/groups/publish` | `{group_id, expected_revision, reason}` | 发布后的 `Group` |
| POST `/groups/archive` | 同上 | 归档后的 `Group` |
| POST `/groups/preview` | `{group: {name,description?,models,pool}, scope?}` | `valid/conflicts/missing_credentials/candidate_count/dynamic_pool/warnings` |
| GET `/plans` | 无 | `{items: GroupPlan[]}` |
| POST `/plans` | `{name, policies: GroupPolicy[]}` | 新 `GroupPlan` |
| PUT `/plans` | `{plan_id,expected_revision,reason,name,policies}` | 更新后的完整 `GroupPlan` |

`Group` 包含 `id,name,description,status,enabled,models,pool,revision,created_at,updated_at,published_at`。创建时不允许指定 ID。已发布组的模型成员不能修改；归档保留历史归属，不能重新分配模型给另一组。

`pool`：

```json
{
  "mode": "selected",
  "allow_refs": [],
  "deny_refs": [],
  "allow_classes": [{"source": "ai-providers", "provider": "实际 CPA provider ID"}],
  "deny_classes": []
}
```

`mode` 为 `inherit` 或 `selected`。inherit 不允许允许列表；selected 至少有一个允许 ref/类别。`source` 为 `auth-files`（凭证管理）或 `ai-providers`。provider 不内置，使用 CPA 返回值；refs 是旧 `/credentials` 返回的 SHA256 指纹。排除优先，与 Key 原路由权限取交集。

预览仅检查配置、模型归属冲突、凭证目录及交集；`unknown_models` 为空不代表模型存在，`host_contract_verified` 固定 false。静态预览不会发起模型请求。模型目录由管理页调用 CPA `/v1/models` 获取。

`GroupPlan` 包含 `id,name,revision,policies,created_at,updated_at,bound_key_count`。

每项政策四个字段均必填：

```json
{
  "group_id": "服务器返回的组 ID",
  "enabled": true,
  "daily_limit_usd": "500",
  "concurrency_limit": 2
}
```

不限使用 JSON `null`，不得省略字段；金额必须是大于零的十进制**字符串**，最多 9 位小数、最大 `9223372036.854775807`。并发为 `1..10000` 整数或 null。零不代表不限。政策不包含某组/关闭该组时拒绝访问。

## Key 绑定、用量与重置

| 方法 / 相对 M 路径 | 输入 | 返回 |
| --- | --- | --- |
| GET `/key-plan-bindings` | query `scope` | `{scope,revision,active,pending}` |
| PUT `/key-plan-bindings` | 下方绑定结构 | 绑定视图 |
| POST `/key-plan-bindings/preview` | 同绑定结构 | `{binding,replaced_legacy_constraints,retained_routes,warnings}` |
| GET `/keys/group-usage` | query `scope` | `GroupKeyUsage` |
| POST `/keys/group-reset` | `{scope,group_id,reason}` | 重置后的单组用量项 |

绑定结构：

```json
{
  "scope": "现有 /keys 返回的 scope",
  "mode": "grouped",
  "plan_id": "现有分组计划 ID",
  "expected_revision": 0,
  "effective": "next_day",
  "reason": "切换到分组订阅"
}
```

首次 revision=0，后续用 GET 返回的值。恢复旧模式使用 `mode=legacy,plan_id=null`。只能安排北京时间次日零点生效；重复安排会替换待生效项，保留历史。effective 不支持立即生效。

`GroupKeyUsage`：`scope,label,label_stale,timezone,plan_id,unclassified_records,groups[]`。
每组：`group_id,group_name,enabled,business_date,daily_limit_usd,raw_cost_usd,reset_credit_usd,used_usd,remaining_usd,overage_usd,concurrency_limit,active_requests,requests,tokens,incomplete_records,accounting_complete,next_reset_at,reset_cutoff,admission`。

金额字段均字符串；无限金额的 `daily_limit_usd`、`remaining_usd` 为 null；无限并发 `concurrency_limit=null`。`used_usd=raw_cost_usd-reset_credit_usd`。即使不限也累计费用、Token 和请求。`unclassified_records` 是该 Key 分组模式以来的累计未归类记录，不是今日值。

## Keeper 与审计

| 方法 / 相对 M 路径 | 输入 | 返回 |
| --- | --- | --- |
| GET `/shared-labels` | 无 | `{items: SharedLabel[],enabled,stale,error}`，按需刷新 |
| PATCH `/shared-labels` | `{subject_kind,subject_id,value,expected_value}` | `{items: SharedLabel[],consistency}` |
| GET `/integrations/keeper/status` | 无 | `enabled,last_attempt,error,stale,write_consistency`；启用时另有 `instance_id,password_env_ready` |
| POST `/integrations/keeper/refresh` | `{}` | 强制刷新后的状态 |
| GET `/audit` | 可选 query `cursor,limit,object_type,object_id` | `{items: Audit[],next_cursor}` |

`SharedLabel`：`instance_id,subject_kind,subject_id,external_id,value,source_note,fetched_at`。external_id 为十进制字符串，前端不转 Number。subject_kind=`downstream_key` 时 subject_id 为 caller scope；`upstream_identity` 时为凭证 ref。修改上游 alias 不修改原始 note。expected_value 必填，可为空字符串。Key alias 最长 128 字，凭证 alias 最长 50 字；值先去首尾空白，不允许控制/不可见格式字符。

Keeper 未配置则上述写操作报 `keeper_disabled`。旧管理接口 `POST /keys/label` 仍可用：Keeper 配置时回写 Keeper，不配置时写原本地备注。旧接口没有 expected_value，保持最后写入兼容行为。

审计 `limit` 默认 50，1..200；cursor 为上页 next_cursor，不透明使用。项目含 `id,at,actor_kind,actor_id,action,object_type,object_id,before,after,reason,result`；id 为十进制字符串。管理认证不能区分实际操作人时 actor_kind=management-key、actor_id=null，不伪造用户身份。当前审计覆盖组/计划/绑定/重置的成功写入；Keeper 写入和被拒绝操作不在此审计中。

## 自助查询与原接口扩展

`GET /v0/resource/plugins/cpa-key-billing/v1/subscription` 使用普通 API Key 的 Bearer，只能查看该 Key；不接收 scope/api_key，传入返回 400。由 CPA 原认证和插件 tracked 检查共同保护。

旧自助 `/subscription` 额外返回 `grouped` 和 `binding`，原字段保留。旧管理 `/events`、`/errors`、`/analysis` 支持 query `group_id`。事件 `group` 保存归类、定价状态和原始组映射信息；旧历史事件不追溯分组。

## 错误与并发修改

- HTTP 400：字段/金额/时间/理由无效；404：目标不存在。
- HTTP 409：`revision_conflict`、`model_group_conflict`、`published_model_membership_immutable` 或 Keeper 映射/旧值冲突。刷新后让管理员重新确认，不盲目覆盖。
- 模型请求 403：`model_not_grouped` / `group_disabled`；429：`group_quota_exceeded` / `group_concurrency_exceeded`。
- 模型请求 503：`accounting_unavailable` / `group_no_available_credentials`。缺价仍使用原插件错误，默认拒绝未定价请求。
- Keeper 网络/认证/兼容问题返回脱敏错误。`keeper_write_outcome_unknown` 表示写入可能已发生；先刷新核对，不能当作确定失败后自动重试。

请求错误保持 CPA 对应客户端协议形状；管理错误为 `{error:{code,message}}`。所有组名、模型名、供应商名来自配置，不固定示例名称。
