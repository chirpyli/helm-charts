

## safekeepers 表字段说明

`safekeepers` 是 storage controller（storcon）元数据库中的 safekeeper 节点注册表，每一行代表一个已注册到 storcon 的 safekeeper 实例（对应 k8s 中一个 safekeeper Pod）。该表只保存静态注册信息，节点在线状态与利用率由 storcon 心跳在内存中维护，不落库。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `id` | BIGINT | safekeeper 节点唯一 ID，主键，最初沿用控制面数据库序列分配的代理 ID。本 chart 中 `id = nodeIdBase(2000) + StatefulSet 序号`，因此 `2000/2001/2002` 依次对应 `neon-safekeeper-0/1/2`。该 ID 就是 `--id` 启动参数，也是 timeline 的 `sk_set` 成员标识。 |
| `region_id` | TEXT | 节点所属区域标识。Neon Cloud 中形如 `aws-us-east-2`；自建/单区域部署固定为 `default`。用于 describe 展示与多区域归属判断。 |
| `version` | BIGINT | 节点版本号，来自镜像发布标签 `release-<分支提交数>` 的数字部分；特殊值 `1` 表示"刚创建、尚未由控制面发布"，负数非法。storcon 只存储并原样返回，不参与调度决策；本 chart 的注册 sidecar 固定上报 `1`。 |
| `host` | TEXT | safekeeper 对外主机名，k8s 中为 Pod 级 DNS（`<pod>.<headless-svc>.<ns>.svc.cluster.local`）。storcon 用它拼接管理 API 地址，compute 的 walproposer 也用它连接 WAL 服务。 |
| `port` | INTEGER | pg 协议（WAL 服务）端口，默认 `5454`。walproposer 通过该端口推送 WAL，pageserver 通过该端口拉取 WAL 做回放。 |
| `http_port` | INTEGER | safekeeper 管理 API 的 HTTP 端口，默认 `7676`。storcon 通过它做心跳、查询 status、下发 timeline 成员变更（`/v1/timeline`、`/v1/status` 等）。 |
| `availability_zone_id` | TEXT | 节点所在可用区，如 `zone-a`/`zone-b`/`zone-c`。storcon 为新建 timeline 挑选 SK 时按该字段去重，**强制要求复制组成员来自不同 AZ**（`safekeepers_for_new_timeline` 按 AZ 去重），AZ 不足时直接报错，因此副本数不能超过可用 AZ 数量。 |
| `scheduling_policy` | VARCHAR | safekeeper 调度策略，默认 `activating`。取值：`active`（正常，可被选入新 timeline 的 `sk_set`，并启动 reconciler）、`activating`（刚注册，等待首次心跳成功后由 storcon 自动转为 `active`）、`pause`（暂停，不再分配新 timeline 并停止 reconciler）、`decomissioned`（已下线，跳过心跳，禁止调度或迁移到该节点；注意源码中拼写为单个 `m`）。 |
| `https_port` | INTEGER | 管理 API 的 HTTPS 端口，可为 NULL（示例中为空即未启用）。仅当 storcon 配置为通过 HTTPS 访问 safekeeper 管理 API 时必须提供，此时与 `http_port` 二选一使用。 |



```sql
storage_controller=# select * from safekeepers;
-[ RECORD 1 ]--------+----------------------------------------------------------------
id                   | 2001
region_id            | default
version              | 1
host                 | neon-safekeeper-1.neon-safekeeper-hl-svc.neon.svc.cluster.local
port                 | 5454
http_port            | 7676
availability_zone_id | zone-c
scheduling_policy    | active
https_port           | 
-[ RECORD 2 ]--------+----------------------------------------------------------------
id                   | 2000
region_id            | default
version              | 1
host                 | neon-safekeeper-0.neon-safekeeper-hl-svc.neon.svc.cluster.local
port                 | 5454
http_port            | 7676
availability_zone_id | zone-b
scheduling_policy    | active
https_port           | 
-[ RECORD 3 ]--------+----------------------------------------------------------------
id                   | 2002
region_id            | default
version              | 1
host                 | neon-safekeeper-2.neon-safekeeper-hl-svc.neon.svc.cluster.local
port                 | 5454
http_port            | 7676
availability_zone_id | zone-a
scheduling_policy    | active
https_port           | 
```

补充说明：

- 表结构迁移历史：`2024-08-23-102952_safekeepers`（初始表，从控制面 schema 精简复制）、`2024-12-12-212515_safekeepers_scheduling_policy`（新增 `scheduling_policy`）、`2025-01-09-160454_safekeepers_remove_active`（删除旧的 `active` 布尔列，由 `scheduling_policy` 取代）、`2025-02-28-141741_safekeeper_use_https`（新增 `https_port`）、`2025-07-02-170751_safekeeper_default_no_pause`（默认值改为 `activating`）。
- 注册流程：`sk-register` sidecar 先调 `POST /control/v1/safekeeper/:id` 幂等 upsert（实现为 `INSERT ... ON CONFLICT (id) DO UPDATE`，重复注册安全，成功返回 `204`），再调 `POST /control/v1/safekeeper/:id/scheduling_policy` 置为 `active` 才会被调度新 timeline。新插入的记录初始为 `activating`，首次心跳成功后 storcon 会自动改为 `active`。
- 与 `nodes` 表的差异：safekeepers 没有 `lifecycle` 列，也没有删除接口，节点下线统一用 `scheduling_policy = decomissioned` 表达。
- 请求体中的 `active` 字段仅为兼容控制面旧接口保留，storcon 会忽略它，不写入数据库。