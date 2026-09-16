# 分组计费插件安装 · Linux x86_64

版本 `1.3.13-group.1`（Pre-release），在上游 cpa-key-billing `1.3.11` 上二开。它替换原计费插件，与 Usage Keeper 插件并存；不能同时装两份相同 PluginID 的计费动态库。

## 适用环境

- Linux x86_64 / amd64，glibc 动态链接环境。提供的 Zig 交叉构建以 glibc 2.17 为目标；建议 Debian 12 / Ubuntu 22.04+。
- CPA **支持插件**的 v7.2.143 构建；不使用 no-plugin 或纯静态禁用插件构建。其他 CPA 版本先跑回归。
- Docker 使用 glibc 系统镜像；本包不是 Alpine/musl 包。以容器内的 `uname -m`、`ldd --version` 为准。
- 仅支持一个 CPA 进程管理一份计费数据库，不提供多进程/多副本共享限流。

## 安装或升级

从 [版本页面](https://github.com/2418554463/cpa-plugin-group-billing/releases/tag/v1.3.13-group.1) 下载 `cpa-key-billing_1.3.13-group.1_linux_amd64.tar.gz` 与 `checksums.txt`。先用 `sha256sum` 对比压缩包和清单中同名行，再解压；下面的 `SHA256SUMS` 用于验证解压后的包内文件。ZIP 只有动态库，不含文档；首次部署优先选择完整 tar.gz 包。

1. 停止 CPA，备份旧 `.so` 与计费 SQLite 数据库。停止后备份整个数据库目录，包含可能存在的 `-wal` / `-shm`。保留旧配置和上游凭证。
2. 在解压目录运行 `sha256sum -c SHA256SUMS`，核对包内文件。
3. 将包里的 `cpa-key-billing.so` 放入你的 CPA `plugins/`。旧库移到该目录以外的备份目录，避免加载两份。动态库和目录须让 CPA 运行用户可读；数据库目录须可写。
4. 合并 `plugins.example.yaml` 到已有 CPA 配置，**不要覆盖**原来的模型、Key、端口及供应商配置。已有计费数据库必须继续使用原 `state_file` 路径，才能保留历史。
5. 如果使用 Keeper，设置 `keeper_url` 为 Keeper 后端地址（可带子路径，不带 `/api/v1`），并把其**独立管理密码**放进 CPA 进程环境变量 `CPA_KEEPER_PASSWORD`。不是 CPA 的管理密钥。远程 Keeper 应使用 HTTPS；只在可信内网使用 HTTP。
6. 启动 CPA。首次加载将计费数据库事务迁移至 v15，保留旧事件和旧套餐。检查插件已启用、无加载/迁移错误。
7. 在管理面板打开“API Key 计费 → 分组订阅”；也可打开 `/v0/resource/plugins/cpa-key-billing/ui` 并用管理密钥登录。

二开源码仓库为 [2418554463/cpa-plugin-group-billing](https://github.com/2418554463/cpa-plugin-group-billing)，插件仓库元数据指向此地址。此版本是预发布，`releases/latest` 和原一键安装脚本不会自动选中它，请手动下载指定版本。**不要运行上游联网安装脚本或点击上游一键更新**，否则可能被原版覆盖。源码构建只生成动态库；上面的校验文件和示例文件步骤适用于完整交付包。

## 第一次配置

1. 打开页面，自动从 CPA 同步 Key、模型和凭证目录。没有内置模型清单或示例组。
2. 新建任意资源组：选模型或填写精确客户端模型 ID；选择继承池、指定凭证或供应商类别，并可排除某些凭证。供应商类别匹配未来新增的同类凭证。
3. 发布组。每个模型只能归属一个已发布/归档组，发布后模型成员冻结。改组名、描述、凭证池或开关不受此限制。
4. 创建分组计划，显式纳入并开启需要的组，配置“每日 USD”和“同时并发”。不限需明确勾选；未纳入/未开启的组拒绝访问。先给模型配好单价。
5. 选择 Key，预览并安排计划绑定。**北京时间次日零点生效**，不提供立即切换。此前继续旧版计费。一个 Key 代表一个用户，多 Key 不合并额度。
6. 生效后，小流量验证每组：请求成功、usage 出现在正确组、用量增长、并发释放、另一组额度不受影响。不要以静态预览代替真实请求验证。

分组模式替代旧套餐额度和旧 Key 全局并发，但与旧模型/凭证权限取交集。旧页仍保留旧配置；实际额度在“分组订阅”查看。组政策修改立即影响所有当前绑定 Key，今日账本不会清零。

## Keeper 备注规则

- Key 备注对应 Keeper `cpa_api_keys.key_alias`；凭证备注对应 `usage_identities.alias`；原始 `note` 只读。
- 通过 Keeper HTTP API 读写，共享同一份权威数据，不直接执行其 SQL。插件 SQLite `gb_shared_labels` 只是展示缓存。
- Key 用原始 Key 的 CPA caller-scope 哈希精确对应；凭证通过 `auth_type + auth_index` 对应 CPA 凭证指纹。不会按掩码、邮箱或近似名字猜测。
- 在任一页面修改后，另一个页面刷新即可读取新值。计费管理读取按需刷新（30 秒节流），可点击“立即同步”；不创建后台轮询。普通 Key 用户只读自己的备注缓存。
- Keeper 不提供原子 CAS。新共享备注编辑器会检查旧值，但多人同时写仍可能最后写入者覆盖。旧 `/keys/label` 接口保持兼容并使用 Keeper 最后写入语义。
- 映射缺失、认证失败、超时或缓存失败会明确报错，不悄悄写成本地备注。写入结果未知时先刷新确认，不盲目重试。

## Docker

参考 `compose.override.example.yaml`，合并到现有服务，保留已有配置和认证文件挂载。独立持久化 `billing-data`。多个容器不可共用本库。Keeper 同机但在别的容器时，使用服务名/网络地址，不使用 `127.0.0.1`。

源码构建：

```sh
docker build --platform linux/amd64 -f deploy/Dockerfile --target artifact --output type=local,dest=dist/docker .
```

该 Dockerfile 在 Debian 12 构建，产物要求匹配的 glibc；预编译包采用单独的 glibc 2.17 目标。当前开发机没有 Docker，Dockerfile 未实际运行验证。

也可在 Linux x86_64（Go、gcc 可用）运行 `bash deploy/build-linux-amd64.sh`。macOS 交叉编译另设 `CPA_BUILD_GO` 和 `CPA_BUILD_ZIG` 为各工具绝对路径。

## 限制、备份与回退

- 金额是 usage 后结算的日限额，不是严格预扣余额；在途请求可超额。不承诺宿主崩溃时未上报 usage 的费用可恢复或 exactly-once。
- 金额按 1e-9 USD 保存整数，每事件四舍五入一次。缺价/无效 usage/未归类记录标记不完整；不猜测费用。金额溢出或保存失败关闭相关准入。
- 普通请求支持 CPA 思考后缀，保留路由前缀和别名。宿主仅提供上游 Model 时按精确上游名匹配，不能证明别名映射时须停用相关计划并核对。
- “重置”记录抵扣基线和理由，不删除消费；重置前启动的迟到 usage 不扣新额度。历史事件保留策略沿用原插件，日账本不会随事件清理而删除。
- 无订阅到期、支付、充值、多 Key 用户、分布式限流、跨实例配额或自动迁移已发布组成员。
- v15 数据库不能交给旧 v14 插件继续写。回退须停止 CPA，恢复升级前备份的旧库和旧插件；备份之后新增的数据另行保留/导出，不覆盖丢弃。定期做停机备份。

接口见 `API.md`；验证范围见 `VERIFICATION.md`。未连接你的生产 CPA/Keeper，也没有在你的服务器上安装。
