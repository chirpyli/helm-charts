-- 端点类型（对齐 Neon API 的 EndpointType）：
--   read_write：分支主计算节点，每个分支最多一个；
--   read_only ：只读副本（hot standby），每个分支可有多个。
--
-- 为什么不从 K8s / spec 推断：read_write 唯一性校验必须在控制面重启后仍然生效，
-- 而 spec 与 compute_ctl 现场状态都是可重建的派生数据。
--
-- 存量数据回填为 read_write（与迁移前行为一致），read_only 端点需重建后才会带正确类型。
ALTER TABLE endpoints
    ADD COLUMN IF NOT EXISTS type VARCHAR(16) NOT NULL DEFAULT 'read_write'
        CHECK (type IN ('read_write', 'read_only'));
