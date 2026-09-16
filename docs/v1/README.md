# 分组计费 V1 设计

本目录保留最初的 V1 设计记录。现在已有 `1.3.12-group.1` 实现与 Linux x86_64 交付包；实际行为、安装与验证以 [安装指南](../../deploy/README.md)、[实际 API](../../deploy/API.md)、[验证记录](../../deploy/VERIFICATION.md) 为准。下方“拟新增接口”和设计期检查不是当前测试结论。

## 核心约定

- 模型名称、组名称、组数量、供应商均不写死；A/B 和前文模型仅是样例。
- 正式安装不创建样例组。管理员从实际 CPA 模型/供应商/凭证目录配置资源组。
- 资源组定义模型与凭证池，订阅计划定义每组额度和并发，Key 绑定计划。
- 每 Key × 每组独立记账，默认北京时间自然日；金额与并发各自支持不限。
- 新建组不会自动给现有计划放行，须显式添加/开启政策。
- Keeper 是共享备注唯一来源，插件通过其 API 读写，不共用数据库迁移或 SQL 直写。

## 设计文件

- [完整业务和技术规格](specification.md)
- [OpenAPI 3.1 接口契约](openapi.json)
- [新增表结构参考](schema.sql)（不可当作生产迁移直接运行）
- [待实现验收清单](acceptance.md)

## 拟新增接口速览

管理前缀：`/v0/management/plugins/cpa-key-billing/v1`。
全部为设计中的接口，当前原版插件尚不存在这些 `/v1` 路径。

| 路径 | 方法 | 作用 |
| --- | --- | --- |
| `/groups` | GET / POST / PATCH | 查询、创建草稿、修改资源组 |
| `/groups/publish` | POST | 发布并检查模型归属冲突 |
| `/groups/archive` | POST | 归档组，保留账本与归属 |
| `/groups/preview` | POST | 静态匹配和凭证权限预览 |
| `/plans` | GET / POST / PUT | 动态分组计划与每组政策 |
| `/key-plan-bindings` | GET / PUT | 查看或安排次日生效的 Key 计划 |
| `/key-plan-bindings/preview` | POST | 检查旧约束替换及切换时间 |
| `/keys/group-usage` | GET | Key 今日各组额度、使用量、并发 |
| `/keys/group-reset` | POST | 重置单 Key 单组的有效日消费 |
| `/shared-labels` | GET / PATCH | Keeper 共享备注读写 |
| `/integrations/keeper/status` | GET | 连接状态、匹配数量、错误 |
| `/integrations/keeper/refresh` | POST | 按需连接及刷新映射 |
| `/audit` | GET | 查询脱敏配置修改记录 |

自助接口：GET `/v0/resource/plugins/cpa-key-billing/v1/subscription`，只返回当前认证 Key 的分组订阅和用量。
合计 14 个路径、20 个操作。已有事件、错误和分析查询追加可选 `group_id`，保持旧字段兼容。
模型及凭证目录复用宿主现有能力；实现时核对目标 CPA 版本，不引入插件内置供应商名单。

## 本次已验证的设计产物

- JSON 可解析，20 个 operationId 唯一，219 处本地 `$ref` 可解析；这不是完整 OpenAPI 语义认证。
- SQL 在一次性内存库中与原 v14 schema 合并执行，完整性和外键检查通过。
- 检查了重复模型归属、发布后成员冻结、非法零限额、整数溢出、伪造审计身份拒绝及迟到消费重置抵扣。
- 交互预览验证 A 组并发/额度拒绝不影响 B，另一 Key 独立；验证新增任意名称的第三组、自定义模型、动态套餐政策、该组请求与结算。
- 预览备注保存后，两侧刷新显示同值；未连接真实 Keeper。
- 检查浅色桌面、深色桌面与 320px 内容宽度的窄屏布局。

以上是设计阶段的历史检查。实现后的 Go/race/迁移/真实 CPA E2E 和 Keeper 模拟接口联调结果见最新验证记录；真实 Keeper 部署与 Linux 动态加载仍需目标环境验收。

## 实现顺序

组和政策领域逻辑 → SQLite 迁移与日账本 → 宿主准入/usage/complete → Keeper 适配 → 管理/自助界面 → 兼容性和端到端测试。

重点限制：日金额是按实际 usage 后结算的限额，不是绝不超额的预付余额；跨实例配额和多 Key 合并主体不在首版。
