# 验证记录

测试日期：2026-09-17。目标：Linux x86_64 可部署动态库。测试请求均使用 dummy Key 和本地模拟上游，没有调用付费模型或连接生产 Keeper。

## v1.3.14-group.1 本次验证

- Go 全包测试、race、vet；UI 格式和 JavaScript 语法检查。
- Playwright 在本地 dummy backend 18765 验证 1440px 桌面和 390px 手机、浅色和深色主题。
- 搜索和勾选模型、手动别名同步、保存回读、指定供应商和凭证、排除项保留、已发布模型只读、计划额度和不限并发均通过。
- 手机弹窗无横向溢出，长表单内容区独立滚动，底部操作按钮可见；控制台无错误。
- 本次未修改计费后端，未重新执行下述真实 CPA E2E；下述结果来自 v1.3.13-group.1。

## 上一版功能验证（v1.3.13-group.1）

- Go 全包单元测试（含 SQLite v14→v15 迁移和旧数据保留）；`go test -race ./internal/...`；`go vet ./...`。
- 分组领域测试：精确金额、双组额度/并发隔离、多 Key 隔离、60 个并行准入只成功 2 个、重复释放、不限、第三组默认拒绝、迟到 usage/重置/跨日、修订冲突、数据库失败关闭准入。
- SQLite：分组、政策、日账本、审计、共享备注往返；已发布模型冻结；完整性与外键检查。
- Keeper 适配器：自身密码/cookie 登录、子路径、准确 scope/auth-index 匹配、双向 alias、清空、旧值冲突、错误脱敏、拒绝重定向；不传 CPA Authorization、不缓存原始 Key/identity。
- `scripts/e2e_cpa_billing.sh v7.2.143`：实际 CPA + 本机动态库 + dummy provider。51 次上游测试请求；4 协议 × 4 上游 × 流式/非流式矩阵，原额度/并发/路由/参考价回归通过。
- `scripts/e2e_group_billing.py`：实际 CPA v7.2.143 + 本机动态库；分组别名/思考后缀归组、quota/concurrency 拦截、另组不受影响、重置、供应商候选交集、自助权限、重启用量保留、Keeper 模拟 API 与真实 SQLite 集成。
- 前端：按仓库要求启动 dummy backend 18765；Playwright/Chrome 验证 1440px 桌面与 390px 手机。创建/发布三组、按组有限/不限计划、次日绑定预览与保存、手机编辑器、无控制台异常及无横向溢出。前端数据接口使用测试 fixtures，与上述真实后端 E2E 分开验证。
- 模板经过 `scripts/format_ui.mjs --check`，独立分组 UI 脚本经过 JavaScript 语法检查。

## 构建来源与未验证项

- 基线：上游 cpa-key-billing v1.3.11，commit `0014bc58ed23833c5d949dd05b9f6ab6ea4a2b9d`；二开源码对应 [v1.3.14-group.1](https://github.com/2418554463/cpa-plugin-group-billing/tree/v1.3.14-group.1)。完整安装包的 `BUILDINFO.json` 记录实际源码提交，包内外分别提供 SHA-256 校验清单。
- 本机工具：官方 Go 1.27.1、Zig 0.15.2，下载校验通过。Linux 构建指定 `x86_64-linux-gnu.2.17`、CGO、`c-shared`、`cshared` tag。
- 构建机是 macOS arm64，真实 CPA 回调 E2E 在 **macOS** 执行。Linux `.so` 已交叉编译并检查 ELF/链接符号，但没有 Linux 执行环境，**尚未做 Linux 动态加载运行验证**。Dockerfile 未运行。这一差异不能用本机 E2E 掩盖。
- 未验证你的实际 CPA 配置、真实 Keeper 部署/反代/版本、生产流量及其他 CPA 版本。必须按安装指南在目标服务器做小流量验收后再开放用户。
- 模拟次日生效采用停机后修改一次性测试库的绑定时间；生产接口不支持立即切换，也不提供此测试入口。
