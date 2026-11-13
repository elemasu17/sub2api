# 原生发布与本地备份

本目录通过源码构建 Linux 二进制，由 systemd 运行，不发布容器镜像，不依赖对象存储。构建入口和上游同步见同目录构建文档。

## 目录与权限

- `/opt/sub2api/releases/<version>`：部署用户/root 持有，服务用户只读；程序与 resources 必须成套。
- `/opt/sub2api/current`：当前版本链接；禁止在运行目录执行 git pull。
- `/etc/sub2api/runtime.env`：0600，systemd 读取；保存原有密钥，配置 `DATA_DIR=/var/lib/sub2api`、`PRICING_DATA_DIR=/var/lib/sub2api` 和明确的数据库/Redis 地址。
- `/var/lib/sub2api`、`/var/log/sub2api`：服务用户可写；保留 config.yaml、.installed、pages、插件数据。
- `REDEEM_PURCHASE_URL`：空值不显示购买入口；填入 HTTPS 地址后重启服务生效，普通新标签链接不附带登录令牌。
- `ALLOW_OFFICIAL_BINARY_UPDATES=false`：固定在原生 service 中；官方网页下载升级和回滚不能替换定制程序。

数据库和 Redis 必须只监听本机/受保护网络。迁移期间不要同时升级大版本。应用连接角色、JWT/TOTP 密钥及原有环境配置必须保留。

## 备份

将 `backup.py` 安装为 `/usr/local/sbin/sub2api-backup`（root，0700），公钥放 `/etc/sub2api/backup/recipient.asc`，配置 `/etc/sub2api/backup/config.json`（0600）。**解密私钥只在独立恢复设备保存，勿提交 Git 或复制到生产备份目录。**

配置例（数据库名及路径按实际部署调整）：

```json
{
  "mode": "native",
  "database": "sub2api",
  "backup_dir": "/var/backups/sub2api",
  "recipient_file": "/etc/sub2api/backup/recipient.asc",
  "release_dir": "/opt/sub2api/current",
  "paths": {
    "data": "/var/lib/sub2api",
    "service-config": "/etc/sub2api",
    "nginx": "/etc/nginx",
    "certificates": "/etc/letsencrypt"
  }
}
```

若 Redis 使用密码，另设 `redis_password_file` 指向 root-only 文件。脚本不把密码写进命令行或输出。应用数据库采用 PostgreSQL 18 客户端通过本机 postgres 身份导出，不依赖 shell 加载 .env。

```sh
sudo /usr/local/sbin/sub2api-backup --label manual
sudo /usr/local/sbin/sub2api-backup --quiesce --label before-upgrade
```

日常备份不中断服务，PostgreSQL 和 Redis 各自生成一致快照，二者不是同一事务时间点。升级前使用 `--quiesce` 排空并停止应用，保存同一维护窗口的状态后自动恢复应用；发布脚本已经停应用时，备份不会自行启动它。Redis 保存独立 RDB，不在线复制变化中的 AOF。

归档包括数据库逻辑 dump、集群角色、Redis RDB、运行中的真实程序及资源、配置、持久密钥和数据；容器迁移模式也会保存容器可写层中的实际二进制。文件先在0700临时目录生成，完成 GPG 公钥加密后清理明文；输出密文 SHA256。失败不产生看似成功的归档，已停止的应用会在 finally 中恢复。

安装 timer 后每日北京时间04:15左右生成一份本地备份。默认不自动删除归档，运营者需监控空间并确认异机副本后制定保留策略。

`pull-backups.py` 通过 SSH/rsync 拉取密文并验证校验值。Mac 未开机时不能承担唯一异机定时备份目标；可以把同一工具放在常开的 SSH 主机/NAS 执行。脚本不使用 `--delete`。

## 恢复演练

在保存私钥的设备运行 GPG 解密到0700临时目录，再运行 `verify-backup.py`。它验证每个文件和恢复组件，拒绝路径穿越、符号链接、额外文件和校验失败。

`restore.py` 在服务器将已解密的备份恢复到**全新的数据库和目录**，拒绝覆盖已存在的目标。它不会启动服务或切换生产。原应用角色须先存在。

```sh
sudo python3 restore.py /protected/recovery.tar.gz \
  --database sub2api_restore_check --owner sub2api \
  --directory /var/lib/sub2api-restore-check
```

恢复验证还需：检查数据库记录数量和迁移清单；隔离 Redis 加载归档中的 RDB；修正复制配置中的连接地址；用归档配套程序在仅本机端口启动。演练实例必须阻断外连并关闭自动刷新，避免复制出的生产 refresh token 被再次消费。测试后清理明文临时归档。

实际恢复 Redis 时，应用必须停止；目标目录不能残留旧 AOF，否则它会优先于 RDB加载。先使用独立RDB启动、确认加载，再启用AOF并等待重写完成；不得混用不同备份的PostgreSQL/Redis。完整恢复保留归档中的密钥，不能重新生成TOTP/JWT密钥代替。

## 后续发布

`deploy.py` 要求干净源码构建的包与独立提供的 SHA256，验证架构、版本和逐文件清单。它将文件安装到新版本目录，停止应用、生成维护窗口备份、切换 current、启动并检查公开版本接口。

```sh
sudo python3 deploy.py /protected/sub2api_VERSION_linux_amd64.tar.gz \
  --sha256 VERIFIED_SHA256 --database sub2api
```

失败时只有数据库迁移清单未变，才会自动恢复旧程序。数据库变化后会保持应用停止，要求按记录的成套备份恢复；不能宣称软链接回退等于数据库回退。`/opt/sub2api/last-deployment.json` 保存版本、源码commit、备份位置和迁移指纹。

此脚本针对已完成原生安装的后续发布；首次容器迁移应先完成隔离恢复演练，停旧应用后再做最终dump和Redis快照，保留旧目录与容器直到验收完成。切换后只禁用旧sub2api相关容器的重启策略，不停止同机其他业务。
