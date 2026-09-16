# v1.3.14-group.1 · 分组计费界面升级

本版改进分组订阅界面，计费规则与数据库结构保持不变。基于上游 `haowang02/cpa-plugin-key-billing` v1.3.11 二开，保留 MIT 许可证与上游署名。

## 本次更新与安装

- 资源组使用“基本信息 / 模型范围 / 凭证池”分栏，底部固定保存按钮。
- 模型、供应商及凭证改为可搜索的勾选列表，显示已选数量；仍支持任意模型 ID / 别名。
- 改进资源组标签、计划双列布局、手机单列及深浅主题。
- 在插件商店刷新自定义源，选择本仓库的插件，手动 Tag 填写 `v1.3.14-group.1`。不要选 Latest。
- 从 `v1.3.13-group.1` 升级前备份原库及 SQLite 数据目录，替换后重启 CPA，再刷新浏览器。此次升级没有新增数据库迁移。

## 下载

- `cpa-key-billing_1.3.14-group.1_linux_amd64.tar.gz`：推荐，包含动态库、安装说明、配置示例、接口文档、构建信息和包内校验。
- `cpa-key-billing_1.3.14-group.1_linux_amd64.zip`：仅 `cpa-key-billing.so`，供手动/插件管理安装。
- `cpa-plugin-group-billing_1.3.14-group.1_source.tar.gz`：与 Git tag 对应的完整源码。
- `checksums.txt`：以上附件的 SHA-256。先核验下载包，解压完整包后再运行 `sha256sum -c SHA256SUMS`。

## 功能

- 模型、组名、供应商和凭证动态配置，没有写死的模型或 A/B 组。
- 每 Key × 每组独立日额度（USD）和并发，支持显式不限；计划绑定北京时间次日生效。
- 组级凭证池与旧权限取交集，支持日账本、重置与审计。
- 通过 Keeper API 共享 Key/凭证备注，普通用户仅能查看自己的数据。
- 管理页与手机布局、普通 Key 用户分组额度展示。

## 部署前必读

**此版本是 Pre-release，不是已验证可直接生产上线的稳定版。**

- 只发布 Linux x86_64 / amd64、glibc 包（Zig 构建目标 glibc 2.17）；不是 Alpine/musl 包。
- 本版重新执行单元测试、race、vet 和界面回归；未改动的计费后端沿用上一版真实 CPA v7.2.143 与 Keeper 模拟集成结果。**Linux `.so` 已交叉编译，但尚未做 Linux 动态加载运行验证；Dockerfile 未实测。**
- CPA 必须支持插件；只允许一个 CPA 实例管理数据库。替换原计费库，不可同时加载两个同 PluginID 的插件。
- 升级前停机备份旧动态库和完整 SQLite 数据库目录。首次加载迁移至 v15；回退旧版要恢复升级前备份。
- 额度按宿主 usage 和模型配置单价后结算，在途请求可能超额；不是供应商真实账单或严格预扣余额。
- Keeper 使用独立管理密码；请先在目标服务器做小流量验收，不要运行上游联网安装脚本。预发布不会被 `releases/latest` 或一键更新自动选中。

[安装说明](https://github.com/2418554463/cpa-plugin-group-billing/blob/v1.3.14-group.1/deploy/README.md) · [实际接口](https://github.com/2418554463/cpa-plugin-group-billing/blob/v1.3.14-group.1/deploy/API.md) · [验证记录](https://github.com/2418554463/cpa-plugin-group-billing/blob/v1.3.14-group.1/deploy/VERIFICATION.md)
