# aibj 原生发布规范

本规范适用于 `elemasu17/sub2api` 的 `aibj` 分支和 `z.aibj.cc` 的原生 systemd 部署。`origin` 指向定制 fork，`upstream` 指向 `Wei-Shaw/sub2api`。构建、备份和部署使用本目录脚本。

## 1. 确定发布源码

先核对 `git status --short --branch`、`origin/aibj` 和服务器的 `/opt/sub2api/current`、`/opt/sub2api/last-deployment.json`。从最新 `origin/aibj` 开始，确认候选提交包含当前线上提交。工作区有无关改动时，在独立工作树准备候选，保留原工作区的修改、暂存状态和未跟踪文件。

| 发布类型 | 源码准备 |
| --- | --- |
| 上游新版本 | 从 `upstream` 获取指定的正式发布标签 `vX.Y.Z`，核对 GitHub 发布说明和标签提交，从 `origin/aibj` 合入该标签。可复用 `upstream-stable-sync.yml` 创建的同步草稿 PR。处理冲突后检查定制改动和数据库迁移。 |
| 本地定制 | 从 `origin/aibj` 创建功能分支，将本次要发布的代码和相关测试提交，经评审合入 `aibj`。准备构建时，核对合并后的实际提交和发布范围。 |

核对账号导入、分组和调度行为、受限管理权限、兑换购买入口以及禁止官方二进制替换的保护。发现候选缺少已上线改动或混入无关修改时，先修正候选。未提交文件仅在明确属于本次发布且已完成评审和提交后方可纳入。

## 2. 验证并构建

1. 在发布候选提交上运行 `native-checks.yml` 覆盖的原生构建和定制功能检查，并按改动补充前后端回归。GitHub Actions 不可用时，使用相同工具链在本机或独立执行机完成对应检查，记录实际结果。
2. 对比线上提交与候选提交的 `backend/migrations/`，检查新增、重名或内容变更的迁移。涉及迁移时，按运维文档在隔离恢复库验证启动、迁移和受影响功能，记录旧程序与迁移后数据库的兼容性。隔离实例须阻断外连并关闭账号自动刷新。 [恢复演练](OPERATIONS.md#恢复演练)
3. 确认 `aibj` 上的最终提交通过检查并已同步到 fork。从该提交的干净工作树构建。版本为 `X.Y.Z-aibj.N`，其中 `X.Y.Z` 取候选已合入的官方发布标签，本地定制沿用当前上游版本。`N` 延续已发定制版的序号，跨上游版本继续递增。分配前核对线上记录、服务器发布目录和已发标签，避免重用。
4. 使用 [build.sh](build.sh) 显式传入 `VERSION`，核对 `TARGET_ARCH` 与服务器架构一致。通过 GitHub Actions 生成正式构建包时，`native-release.yml` 使用 `native-vX.Y.Z-aibj.N` 标签，其产物发布与服务器部署是两个步骤。 [构建与 CI](BUILD.md)
5. 校验产物 SHA256，确认 `manifest.json` 中 `version`、`commit`、`os`、`arch` 与计划一致，且 `build_type` 为 `source`、`dirty_source` 为 `false`。记录版本、提交、测试结论和归档 SHA256。

## 3. 部署与验收

1. 通过已配置的 SSH 通道上传归档，在服务器核对工作站或 CI 记录的 SHA256。确认 systemd 服务、当前版本及其发布目录、备份配置和可用磁盘空间。使用服务器上已核对的发布脚本 `/opt/sub2api/ops/deploy.py`。

   ```sh
   sudo python3 /opt/sub2api/ops/deploy.py "$archive" \
     --sha256 "$expected_sha256" --database sub2api
   ```

   `archive` 为服务器上归档的绝对路径，`expected_sha256` 为可信构建端记录的摘要。脚本校验包后停止应用，生成维护窗口加密备份，切换 `current`，启动服务并检查版本。 [部署脚本](deploy.py)
2. 核对 `/opt/sub2api/last-deployment.json` 的 `phase` 为 `completed`，版本、提交和目标目录与计划一致，且 `backup` 指向有效的加密备份。涉及迁移时，确认预期迁移在 `schema_migrations` 中。检查 `systemctl is-active sub2api.service`、公开接口 `/api/v1/settings/public` 的版本、启动日志及本次改动涉及的功能。
3. 发布或验收失败时先保留所有发布目录，检查发布记录和服务日志。只有确认旧程序兼容当前数据库时才切回旧程序。需要回退数据时，使用同次发布的成套备份按恢复流程处理。迁移指纹相同也不能单独作为回滚依据。 [运维与恢复](OPERATIONS.md)

## 4. 清理服务器历史版本

每次上线验收通过后执行。竞争部署锁前先准备好命令，拿到锁后重新读取记录。以下命令在服务器运行，锁被占用时退出并在正在进行的部署结束后重新核对。

```sh
sudo flock -n /run/lock/sub2api-deploy.lock bash
```

在持锁的 shell 内查看部署状态和待整理目录。

```sh
systemctl is-active sub2api.service
readlink -f /opt/sub2api/current
cat /opt/sub2api/last-deployment.json
find /opt/sub2api/releases -mindepth 1 -maxdepth 1 -type d -print
```

1. 确认 `phase=completed` 且 `current` 解析到 `target_release`。确认 `previous_release` 指向 `/opt/sub2api/releases/` 下另一个真实存在的直属版本目录，并核对本次加密备份的 SHA256。上一个版本以这次部署前实际运行的版本为准，版本号和目录排序仅供参考。关键记录、目录或备份无法核实时暂停清理。
2. 列出保留的两个目录和待删除的历史发布目录。逐项确认后，仅对明确列出的旧版本目录执行 `rm -r --`，传入各目录的完整路径。清理范围限于 `/opt/sub2api/releases/` 的直属历史发布目录。发现 `.release-*`、符号链接或用途不明的目录时先查明归属。独立备份、恢复记录、配置和运行数据继续按各自的保留策略管理。
3. 复查发布目录只保留当前版和上一个历史版，重新核对服务和公开版本接口，退出持锁 shell。发布记录中写明保留版本、已删除目录和备份路径。发现未清理的目录时注明原因。
