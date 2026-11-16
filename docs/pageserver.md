# pageserver

## nodes 表字段说明


nodes是storage controller（storcon）元数据库中的pageserver节点注册表，每一行代表一个已向storcon注册的pageserver实例。该表只保存需要持久化的注册信息，节点实时可用性、利用率等运行时状态由storcon在内存中维护，不落库。

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `node_id` | BIGINT | pageserver 节点的唯一 ID，由 storcon 分配。单池部署时为 `0,1,2...`；使用 pod 池（pool_id）时为 `pool_id * 1000000 + 序号`，例如 pool 1 的 Pod 为 `1000000, 1000001...`。示例中的 `1001`/`1000` 即 `neon-pageserver-1`/`neon-pageserver-0`。 |
| `scheduling_policy` | VARCHAR | 节点调度策略，决定 storcon 是否把 tenant shard 调度到该节点。取值：`active`（正常调度）、`filling`（正常调度但优先填满，用于新节点预热）、`pause`（暂停调度，保留现有 tenant）、`pause_for_restart`（重启期间暂停调度）、`draining`（排空，逐步迁走 tenant）、`deleting`（待删除）。 |
| `listen_http_addr` | VARCHAR | pageserver 管理 API（HTTP，`/v1/*` 等端点）的监听地址。示例使用 k8s headless Service 的 Pod 级 DNS，便于直连具体实例而不是随机负载均衡。 |
| `listen_http_port` | INTEGER | 管理 API 的 HTTP 端口，默认 `9898`。storcon 通过该端口下发 tenant/timeline 操作、获取状态。 |
| `listen_pg_addr` | VARCHAR | page service 监听地址。计算节点（compute）通过 PostgreSQL 协议直连该地址拉取 page，属于数据面流量。 |
| `listen_pg_port` | INTEGER | page service 端口，默认 `64000`。`safekeepers`/`pageservers` 之间回填、compute 读取都走这个端口。 |
| `availability_zone_id` | VARCHAR | 节点所在的可用区（AZ）标识，如 `zone-b`、`zone-c`。storcon 用它做 AZ 感知调度：满足 tenant 的 `preferred_az_id`、避免主备分片落在同一 AZ，从而提升容灾能力。 |
| `listen_https_port` | INTEGER | 管理 API 的 HTTPS 端口，默认 `9899`，可为空。仅当 storcon 配置为通过 HTTPS 访问 pageserver 管理 API 时使用；为空表示只提供 HTTP。 |
| `lifecycle` | VARCHAR | 节点生命周期状态。`active` 表示节点正常在用；`deleted` 表示节点已被（软）删除，storcon 会忽略该节点，不会再向其调度 tenant。 |
| `listen_grpc_addr` | VARCHAR | gRPC API 监听地址，可为空。用于内部工具和组件（如 storage scrubber、safekeeper 相关调用）的 gRPC 请求，与 HTTP/pg 端口相互独立。 |
| `listen_grpc_port` | INTEGER | gRPC 端口，默认 `51051`，可为空。只有当 pageserver 与调用方都启用 gRPC 时才会注册。 |

补充说明：

- `listen_http_addr` / `listen_pg_addr` / `listen_grpc_addr` 在 k8s 中通常指向 `<pod-name>.<headless-svc>.<namespace>.svc.cluster.local`，直接解析到具体 Pod IP。
- 注册（register）时按 `node_id` 幂等写入；`listen_https_port`、`listen_grpc_addr`、`listen_grpc_port` 允许在迁移/升级过程中发生变化，而 `listen_http_addr`、`listen_http_port`、`listen_pg_addr`、`listen_pg_port`、`availability_zone_id` 变化会被视为与已有记录不匹配而拒绝注册。
- 对应建表语句见 `storage_controller/migrations/`（初始表 `2024-01-07-212945_create_nodes`，后续迁移新增 `availability_zone_id`、`listen_https_port`、`lifecycle`、`listen_grpc_*` 列）。

```sql
-- pageserver
storage_controller=# select * from nodes;
-[ RECORD 1 ]--------+----------------------------------------------------------------
node_id              | 1001
scheduling_policy    | active
listen_http_addr     | neon-pageserver-1.neon-pageserver-hl-svc.neon.svc.cluster.local
listen_http_port     | 9898
listen_pg_addr       | neon-pageserver-1.neon-pageserver-hl-svc.neon.svc.cluster.local
listen_pg_port       | 64000
availability_zone_id | zone-b
listen_https_port    | 9899
lifecycle            | active
listen_grpc_addr     | neon-pageserver-1.neon-pageserver-hl-svc.neon.svc.cluster.local
listen_grpc_port     | 51051
-[ RECORD 2 ]--------+----------------------------------------------------------------
node_id              | 1000
scheduling_policy    | active
listen_http_addr     | neon-pageserver-0.neon-pageserver-hl-svc.neon.svc.cluster.local
listen_http_port     | 9898
listen_pg_addr       | neon-pageserver-0.neon-pageserver-hl-svc.neon.svc.cluster.local
listen_pg_port       | 64000
availability_zone_id | zone-c
listen_https_port    | 9899
lifecycle            | active
listen_grpc_addr     | neon-pageserver-0.neon-pageserver-hl-svc.neon.svc.cluster.local
listen_grpc_port     | 51051
```