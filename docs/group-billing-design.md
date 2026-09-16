# 分组计费与 Keeper 备注共享设计草案

状态：用户已接受推荐的首版方向；本文件保留前期分析，实施以 [V1 设计规格](v1/specification.md) 为准。当前为设计产物，功能尚未实现。

基线：cpa-plugin-key-billing v1.3.11，提交 `0014bc58ed23833c5d949dd05b9f6ab6ea4a2b9d`。

核对日期：2026-09-17。Keeper 和 CPA 的结论来自本次取得的源代码；集成时还需核对实际部署版本。

## 1. 用户提出的目标

- 以现有计费插件为基础开发。
- 与 Keeper 共用备注来源，在任一页面修改后，另一页面重新读取时显示同一值。
- 定义可复用资源组：例如 A 组为 GPT6、GPT5.6 sol；B 组为 GPT5.6 luna、DeepSeek Flash。
- 订阅计划为各组分别设置每人额度，例如 A 组每天 $500，B 组每天 $1000 或不限。
- 各组分别设置每人并发，例如 A 组同时最多 2 个，B 组不限。
- 分组和路由可使用现有 AI 供应商、认证文件凭证。
- 提出可选扩展供用户选择。

V1 默认已采用：一个 Key 一人、北京时间自然日、先按模型归组后限定凭证。
共享下游 Key 的 Keeper 备注和上游身份的 Keeper 别名；CPA 原始 note 单独只读展示。
模型名是用户给出的示例；配置时必须使用 CPA 实际模型目录中的 ID，并核对 alias 映射，不能硬编码推测的模型 ID。

## 2. 现有能力与缺口

| 需求 | 现有代码 | 需要改造 |
| --- | --- | --- |
| 每个 Key 额度 | `KeyState.PlanID` 与 `Cycles` | 增加资源组维度 |
| 每个 Key 并发 | `activeByScope`、`activeRequests` | 增加 `(scope, group_id)` 并发状态 |
| 模型和凭证权限 | `RouteRule`、`scheduler.pick` | 将资源组的凭证池与既有权限求交集 |
| 多个额度窗口 | 每个请求扣该 Key 的所有适用周期 | 只扣当前资源组的周期，另行处理显式全局总额 |
| 两个同为一天的组限额 | 同一计划不允许重复 `period_seconds` | 按组定义窗口；A/B 都可配置日窗口 |
| 下游 Key 备注 | 本地 `api_keys.label` | Keeper 来源适配、精确身份匹配、写回 |
| 上游备注 | callback DTO 尚未接收 `note` | 区分 CPA `note` 与 Keeper `alias` |
| 请求和分析报表 | 当前无组字段 | 增加组 ID、筛选和独立统计 |

不能仅创建两条现有路由或两个现有窗口就实现 A/B 独立记账。

## 3. 备注来源：三类数据必须分清

`cpa-plugin-usage-keeper` 只是 iframe 入口。数据实际由独立应用 `cpa-usage-keeper` 持有，默认数据库为 `WORK_DIR/app.db`。

| 对象 | Keeper 表与字段 | 读取接口（相对 Keeper 根路径） | 写入位置 |
| --- | --- | --- | --- |
| 下游 Key 自定义备注 | `cpa_api_keys.key_alias` | `GET /api/v1/usage/api-keys`、`GET /api/v1/usage/api-keys/settings` | `PATCH /api/v1/usage/api-keys/:id`，`{"keyAlias":"名称"}` |
| 上游身份自定义别名 | `usage_identities.alias` | `GET /api/v1/usage/identities` | `PATCH /api/v1/usage/identities/:id`，`{"alias":"名称"}` |
| CPA 上游原始备注 | `usage_identities.note` 是同步副本 | 同上；也可用 CPA `host.auth.list` 直接读取 | 应修改 CPA 中的原始备注，Keeper 下次同步获取 |

Keeper 的元数据同步保留本地 `alias`，但会更新 `note`。直接改 Keeper 的 `note` 有被下轮同步覆盖的风险。

### 推荐：Keeper 保存备注，插件通过接口读写

- 计费 SQLite 与 Keeper SQLite 保持各自的表和迁移机制。
- 两个页面的备注操作最终落到 Keeper 的同一字段。
- 计费页面打开或手动刷新时读取；写入成功后回读并刷新自己的显示缓存。
- 已经打开的 Keeper 页面通常需要刷新或等待它自己的重新读取；当前不能承诺两边页面无需刷新即时联动。
- 备注只影响显示与搜索，不能作为用户、资源组或路由主键。
- Keeper 暂时不可用时，计费与限流继续工作；备注显示最近成功值并标注状态，修改明确报错，不制造另一份待合并的权威备注。
- 保留插件原有 `label` 供未启用集成时使用；启用共享后，空字符串也是有效备注值，不可错误回退到旧备注。
- 现有本地备注与 Keeper 冲突时提供导入预览和显式选择，默认采用 Keeper 值。

下游 Key 的接口返回有一个重要区别：普通列表只返回 ID 与脱敏值；管理员 settings 接口返回 `apiKey`。
插件在后端瞬时计算 `SHA256("cli-proxy-api:caller-scope:v1\0" + apiKey)`，匹配后丢弃明文。
只保存 Keeper 实例标识、Keeper 行 ID、caller scope 与显示值；不能依赖脱敏 Key、邮箱或数组顺序匹配。
上游身份通过 CPA 的 `auth_index` 对应 Keeper 的 `identity`，并保留 `auth_type` 防止不同来源混淆。

集成需使用 Keeper 自己的管理员认证；CPA 管理密钥不能替代 Keeper session。
写请求需遵守 Keeper 的 `X-CPA-Usage-Keeper-Request: fetch` 规则。
现有 Keeper 没有从已检查代码中确认的、按 scope 返回备注的专用机器接口；不要假设存在。
可复用现有管理员 session 流程；凭据通过环境变量等配置提供，不写入计费库、日志或前端。
请求必须带超时，认证失败不得关闭 Keeper 认证来绕过。

### SQL 直读可作为同机部署的可选适配器

可以增加独立只读 SQLite 连接，按已验证的 schema 读取上述字段，再通过 Keeper API 写回。
这个连接不得调用计费插件的 `sqlite.Open`，不得运行计费迁移、修改 Keeper 的 `PRAGMA user_version` 或文件权限。
WAL 模式下应读取正常在线数据库视图，不能简单复制 `app.db` 当作完整快照。
跨主机不通过网络盘共享在线 SQLite。

“只读 SQL”本身不能实现从计费页面修改 Keeper；共享读写必须另外实现写入路径。
不推荐直接 SQL UPDATE：它绕过 Keeper 校验以及上游 alias 更新后的显示名缓存回调。

## 4. 资源组和计划分开定义

资源组定义模型范围和上游凭证池；计划定义该组对每人的配额。
这样普通套餐和高级套餐可复用同一个 A 组，但额度不同。

| 资源组 | 模型示例 | 每人每日额度 | 每人并发 |
| --- | --- | --- | --- |
| A | GPT6、GPT5.6 sol | $500，组内模型合计 | 2，组内请求合计 |
| B | GPT5.6 luna、DeepSeek Flash | $1000 或不限 | 不限 |

Alice 在 A 组花费 $490，在 B 组花费 $800；A 剩余 $10，B 剩余 $200。
Alice 同时运行一个 GPT6 和一个 sol 请求，第三个 A 组请求被拒绝；B 组仍可运行。
Bob 的额度与并发不受 Alice 影响。

新接口用显式 `null` 表示不限；有限金额必须为正，访问开关单独表示禁用。
旧接口中 `0` 的原有含义保持兼容，不能在迁移时偷偷改为禁止访问。
不限额度的组仍累计费用和并发数，便于报表以及将来降低限额。

## 5. AI 供应商和认证文件怎样参与分组

推荐首版：先按唯一的逻辑模型（含配置的 alias 映射）确定计费组，再从该组允许的凭证池选上游。

凭证池支持：

- 整类认证文件，例如所有 Codex Auth Files。
- 单个 Auth File，使用稳定指纹，不依赖名称。
- AI 供应商配置中的具体渠道/凭证。
- 供应商整类选择以及单个凭证排除。

最终候选 = CPA 当前可用候选 ∩ Key 原有路由权限 ∩ 资源组凭证池。
不能把组凭证池与原有白名单直接取并集，否则可能扩大 Key 的既有权限。
配置型供应商应使用 CPA 的 ID/指纹；只用 `provider=codex` 无法区分两个不同的 Codex 供应商渠道。
新凭证是否自动进入取决于“整类选择”或“固定列表”；界面明确显示。

同一个模型如果需要在 A/B 两个凭证池中按不同政策出售，应配置明确且不重叠的对外模型 alias，或采用明确的优先级且显示匹配预览。
首版建议拒绝有歧义的归组；不允许请求运行后随意改变计费组，也不在 A 额度耗尽后自动借用 B 额度。
“只根据最终随机选中的供应商确定计费组”会影响准入时的并发和额度预判，需独立验证，不承诺当前接口直接支持所有组合。

## 6. 计费、限额和时间语义

- 费用仍由每个模型的实际用量与价格计算，再汇总到资源组；不是所有 A 组模型统一价格。
- 金额限额使用插件计费表计算的 USD，不代表自动读取官方账单或余额。
- 模型必须有明确价格来源；UI 显示价格来源以及是否包含 fast/priority 等已有倍率。
- A 组达到额度只封锁 A 组；B 独立。
- 首版建议自然日，时区 `Asia/Shanghai`，每天 00:00 切换周期；窗口按时区计算，明确区别于首次请求起 24 小时。
- 账本保存历史周期；跨午夜结束的请求按 `UsageRecord.RequestedAt` 归属原日，不扣次日额度，也不能因旧周期过期丢掉历史费用。
- 更新额度不清空已用金额；更改组、周期、计划绑定需明确生效规则并保留审计。
- 未归组或套餐未授权的模型默认拒绝，管理员可配置显式默认组。
- “请求结束后结算”：已完成消费达到上限后拒绝新请求；已在执行的请求继续结算，因此不是严格无超支的预付费余额。
- 不承诺严格 $500 封顶。精确预扣、退款和 exactly-once 结算依赖可靠的用量关联/幂等标识，需额外验证 CPA 已有能力。
- 失败但有可计费用量的事件沿用现有计费口径并独立展示；没有可靠用量不能编造费用。

## 7. CPA 插件契约的限制

当前检查到的 CPA `UsageRecord` 提供 `APIKey`、`Model`、`Alias`、`AuthID`、`AuthIndex`、`RequestedAt` 等，但没有与拦截器相同的 `RequestID` 或可回传的任意 Metadata。
现有计费插件的 DTO 还没有接收 CPA 新增的全部字段；增加 `AuthID` 等字段前需核对目标 CPA 版本。

因此：

- 并发使用拦截器的 RequestID 注册与完成回调释放。
- 计费只从 `usage.handle` 接收用量，使用明确的模型/alias 和配置规则归组。
- 不按“同模型、同凭证、时间接近”猜测哪个 usage 对应哪个并发请求。
- 实时更改模型所属组时，不能假装已经做到每次请求固定准入配置版本。
- 对在途请求、alias、内部 helper、重试和延迟回调做专项联调后才能承诺热更新归属；首版应限制活动归组配置的直接变更，并设计明确的生效边界。
- 去重与精确预扣不能用自行拼出的 `(Key, 模型, 时间)` 充当可靠事件 ID。

不依赖修改 CPA 主项目；若目标 CPA 的接口不足，应明确限制该功能，而不是绕过来源约束。

## 8. 拟新增的数据结构与接口

以下均为设计，不是上游已存在接口。

逻辑数据结构：

| 结构 | 主用途 |
| --- | --- |
| `resource_groups` | 组 ID、名称、模型选择、凭证池、版本 |
| `plan_group_policies` | 计划与组的绑定、额度窗口、并发上限 |
| `group_usage_cycles` | `(scope, group_id, window_id, cycle_start)` 独立账本，保留历史周期 |
| `request_events` 新字段 | 可空 `group_id` 与实际计费配置版本；旧历史不强行猜测归组 |
| 备注映射缓存 | Keeper 实例、对象类型、外部 ID、内部 scope/identity、最近成功值 |

内存状态：`activeByScopeGroup[(scope, groupID)]`；`activeRequests[requestID]` 同时记录 scope 和 group。
并发检查与申请必须原子完成；重复 complete 不重复释放，失败和取消都能释放。
如后续加入用户多 Key 合并，用独立 subject ID 管理；不能从相同备注推定同一人。

建议扩展管理员接口（沿用插件管理前缀）：

| 方法与路径 | 用途 |
| --- | --- |
| `GET/POST/PATCH/DELETE /groups` | 资源组管理 |
| `POST /groups/preview` | 预览模型/凭证命中与重复归组 |
| `GET/PUT /plans/group-policies` | 计划的各组限额与并发 |
| `GET /keys/group-usage?scope=...` | Key 的分组额度与实时并发 |
| `POST /keys/group-reset` | 重置指定 Key 的指定组，写审计 |
| `GET/PATCH /shared-labels` | Keeper 共享显示名读写，管理员权限 |
| `GET /integrations/keeper/status` | 集成连通性、认证与最近读取状态 |

`/events`、`/errors`、`/analysis` 增加 `group_id` 筛选。
普通 API Key `/subscription` 增加组列表、剩余额度、重置时间和并发；返回当前 Key 的数据。
备注的用户可见性单独定义：下游 Key 名称可自助查看，上游内部备注默认仅管理员可见。

需要真实 schema 迁移；Keeper 数据库不参与本插件迁移。
旧计划作为“全局旧策略”保留，用户显式迁移到分组计划；不能清空历史消费并当作全新额度。

## 9. 建议首版包含的界面

- 资源组管理：选择模型、供应商/凭证，显示重复项与未归组模型。
- 计划编辑：每行一个组，额度窗口、金额、并发、不限开关。
- Key 详情：按组显示已用、剩余、下次重置时间、当前并发。
- 请求事件：显示归属组和实际模型；报表可按组过滤。
- Keeper 集成设置：连接状态、备注来源、读取/保存错误；更改后的页面刷新。

## 10. 用户可选的后续功能

| 功能 | 价值 | 默认建议 |
| --- | --- | --- |
| 每人多个 Key 共用额度 | 更换 Key 或多设备不重复领取额度 | 需要用户明确选择 |
| 日/月多窗口叠加 | A 每天 $500、每月 $5000 同时约束 | 第二阶段，沿用窗口概念 |
| 单个 Key 的组额度覆盖 | VIP 或临时加额 | 第二阶段，明确覆盖来源 |
| 全站组并发上限 | 即使 B 对每人不限，也能保护整体上游池 | 有多人使用时建议 |
| 阈值提醒 | 达到 80%/100% 时显示提示 | 首版可做页面提示；外部通知需另选渠道 |
| 计划到期时间 | 订阅到期失效，区别于每天额度重置 | 若为客户提供套餐则建议 |
| 变更审计 | 记录谁改了备注、额度、路由以及生效时间 | 首版建议 |
| 余额充值、支付、严格预扣 | 进一步发展商业计费 | 独立阶段，不能假定现有 usage 契约足够 |

## 11. 实施与验收顺序

1. 确认三个业务选择，锁定 CPA/Keeper 实际版本和部署方式。
2. 完成资源组匹配器与计划组策略；使用虚构 Key 的单元测试。
3. 实现组并发与组账本；A/B 隔离、同组共享、多用户隔离、重启、跨日和超限联调。
4. 实现 Keeper 适配器；精确身份匹配、空备注、同名不同 Key、失效认证、超时、修改回读和脱敏测试。
5. 实现管理及自助页面；桌面/窄屏验证。
6. 旧数据库迁移和历史保留回归；备份副本上验证，不接触生产数据。
7. 按 AGENTS.md 执行 `go test -race ./...`、相关静态检查和 `scripts/e2e_cpa_billing.sh v7.2.143`，再验证实际目标 CPA 版本。

当前环境的 PATH 中未找到 Go；本次完成的是源码核对、本地分支和设计草案，未编译或测试新增功能。

## 12. 源码依据

- [计费 Key 状态](https://github.com/haowang02/cpa-plugin-key-billing/blob/0014bc58ed23833c5d949dd05b9f6ab6ea4a2b9d/internal/billing/state.go)
- [额度窗口校验](https://github.com/haowang02/cpa-plugin-key-billing/blob/0014bc58ed23833c5d949dd05b9f6ab6ea4a2b9d/internal/billing/plan.go)
- [用量记账](https://github.com/haowang02/cpa-plugin-key-billing/blob/0014bc58ed23833c5d949dd05b9f6ab6ea4a2b9d/internal/billing/account.go)
- [并发实现](https://github.com/haowang02/cpa-plugin-key-billing/blob/0014bc58ed23833c5d949dd05b9f6ab6ea4a2b9d/internal/billing/concurrency.go)
- [Keeper 插件说明](https://github.com/Willxup/cpa-plugin-usage-keeper/blob/main/README.md)
- [Keeper 下游 Key 实体](https://github.com/Willxup/cpa-usage-keeper/blob/main/internal/entities/cpa_api_key.go)
- [Keeper 下游 Key API](https://github.com/Willxup/cpa-usage-keeper/blob/main/internal/api/cpa_api_keys.go)
- [Keeper 上游身份 API](https://github.com/Willxup/cpa-usage-keeper/blob/main/internal/api/usage_identities.go)
- [Keeper metadata 更新](https://github.com/Willxup/cpa-usage-keeper/blob/main/internal/repository/usage_identities.go)
- [CPA UsageRecord 契约](https://github.com/router-for-me/CLIProxyAPI/blob/8335eac731946bd4eff18f500653f93736df53d6/sdk/pluginapi/types.go#L1400)
