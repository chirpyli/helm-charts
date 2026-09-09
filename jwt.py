#!/usr/bin/env python3
"""
Neon JWT 密钥对 & 各 scope Token 生成脚本

用途：
  1. 生成 Ed25519 密钥对（publicKey.pem / privateKey.pem）
  2. 用私钥签发各 scope 的 JWT token（PageServerApi / SafekeeperData / Admin / ControlPlane / Tenant / GenerationsApi）
  3. 输出可直接 `kubectl apply` 的 Kubernetes Secret 清单（neon-jwt）

依赖：
  pip install cryptography

用法：
  # 1) 全新生成密钥对 + token + Secret 清单
  python3 jwt.py --namespace neon

  # 2) 基于已有私钥重新签发 token（密钥轮换 / token 轮换场景）
  python3 jwt.py --key-file privateKey.pem --namespace neon

  # 3) 直接输出到标准输出（便于管道给 kubectl 或 SOPS 加密）
  python3 jwt.py --stdout | kubectl apply -f -
  python3 jwt.py --stdout | sops -e --input-type yaml --output-type yaml /dev/stdin > neon-jwt.enc.yaml

输出文件：
  publicKey.pem           — Ed25519 公钥 (PEM)
  privateKey.pem          — Ed25519 私钥 (PEM)，请妥善保管
  neon-jwt.secret.yaml    — Kubernetes Secret 清单（stringData，可直接 apply）
"""

import argparse
import base64
import json
import os
import sys

# Ed25519 的 JWT 签名采用 EdDSA 算法
# JWT 结构: base64url(header).base64url(payload).base64url(signature)

# ---------------------------------------------------------------------------
# Secret 键契约：jwt.py 与 helm chart 之间的唯一边界，双方必须严格一致。
# chart 侧通过 volume items 投影 / env secretKeyRef 按 key 取值，
# 因此这里的 key 名一旦改动，所有子 chart 模板都要同步修改。
#
#   key                      scope            消费方
#   publicKey.pem            -                pageserver / safekeeper / storage_controller / control-plane（校验签名）
#   privateKey.pem           -                仅 control-plane（签发 token）
#   pageserverJwtToken       pageserverapi    storage_controller → pageserver
#   safekeeperJwtToken       safekeeperdata   pageserver → safekeeper（NEON_AUTH_TOKEN）/ storage_controller
#   controlPlaneJwtToken     controlplane     storage_controller → control-plane upcall
#   peerJwtToken             admin            safekeeper 注册 sidecar / storage_controller 副本间通信
#   computeJwtToken          tenant           预留：compute → 存储（当前无消费方，控制面持有私钥可自行签发）
#   generationsApiJwtToken   generations_api  pageserver → storage_controller 的 upcall（re-attach / validate）
# ---------------------------------------------------------------------------
SCOPES = {
    "pageserverJwtToken": "pageserverapi",
    "safekeeperJwtToken": "safekeeperdata",
    "controlPlaneJwtToken": "controlplane",
    "peerJwtToken": "admin",
    "computeJwtToken": "tenant",
    "generationsApiJwtToken": "generations_api",
}


def log(msg: str = "") -> None:
    """
    进度/提示信息一律输出到 stderr。

    原因：--stdout 模式下标准输出必须只有 Secret 清单本身，
    否则 `jwt.py --stdout | kubectl apply -f -` 会因混入非 YAML 文本而解析失败。
    """
    print(msg, file=sys.stderr)


def b64url_encode(data: bytes) -> str:
    """JWT 标准的 Base64URL 编码（去尾部 =，替换 +/ 为 -_）"""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode()


def make_jwt(private_key_bytes: bytes, payload: dict) -> str:
    """
    用 Ed25519 私钥签发一个 JWT。

    参数:
        private_key_bytes: DER 格式的 Ed25519 私钥原始字节 (32 字节)
        payload: JWT payload dict, 例如 {"scope": "pageserverapi"}

    返回:
        完整的 JWT 字符串 (header.payload.signature)

    注意：
        这里刻意不写入 exp / iat 等声明。上游 neon 的 JwtAuth 显式关闭了
        required_spec_claims（libs/utils/src/auth.rs: validation.required_spec_claims = []），
        不要求也不校验过期时间，因此这类静态 token 可以长期有效。
        代价是一旦泄漏即永久有效，只能靠"换私钥 + 重签全部 token + 滚动重启"来轮换。
    """
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey

    header = {"alg": "EdDSA", "typ": "JWT"}

    # 构造签名输入: base64url(header).base64url(payload)
    header_b64 = b64url_encode(json.dumps(header, separators=(",", ":")).encode())
    payload_b64 = b64url_encode(json.dumps(payload, separators=(",", ":")).encode())
    signing_input = f"{header_b64}.{payload_b64}".encode()

    # Ed25519 签名
    private_key = Ed25519PrivateKey.from_private_bytes(private_key_bytes)
    signature = private_key.sign(signing_input)
    signature_b64 = b64url_encode(signature)

    return f"{header_b64}.{payload_b64}.{signature_b64}"


def generate_keypair():
    """生成 Ed25519 密钥对，返回 (private_pem, public_pem, private_raw_bytes)"""
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
    from cryptography.hazmat.primitives import serialization

    private_key = Ed25519PrivateKey.generate()
    public_key = private_key.public_key()

    # 导出 PEM 格式私钥
    private_pem = private_key.private_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PrivateFormat.PKCS8,
        encryption_algorithm=serialization.NoEncryption(),
    )

    # 导出 PEM 格式公钥
    public_pem = public_key.public_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PublicFormat.SubjectPublicKeyInfo,
    )

    # 导出原始私钥字节用于 JWT 签名
    private_raw = private_key.private_bytes_raw()

    return private_pem, public_pem, private_raw


def load_private_key_from_pem(pem_data: bytes):
    """从 PEM 格式的 Ed25519 私钥中提取原始字节"""
    from cryptography.hazmat.primitives import serialization

    private_key = serialization.load_pem_private_key(pem_data, password=None)
    return private_key.private_bytes_raw()


def public_pem_from_private_raw(private_raw: bytes) -> bytes:
    """从私钥原始字节推导出公钥 PEM（--key-file 场景下补齐公钥）"""
    from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
    from cryptography.hazmat.primitives import serialization

    public_key = Ed25519PrivateKey.from_private_bytes(private_raw).public_key()
    return public_key.public_bytes(
        encoding=serialization.Encoding.PEM,
        format=serialization.PublicFormat.SubjectPublicKeyInfo,
    )


def yaml_block_scalar(key: str, value: str, indent: int = 2) -> str:
    """
    把多行文本（如 PEM）渲染成 YAML 的字面量块标量（`key: |`）。

    之所以不用 yaml 库：本脚本只依赖 cryptography，不希望为输出一份清单再引入 PyYAML。
    块标量能完整保留 PEM 的真实换行，K8s 写入 Secret 后组件才能正确解析。
    """
    pad = " " * indent
    # 去掉尾部多余换行，避免块标量末尾出现空行差异
    lines = value.rstrip("\n").split("\n")
    body = "\n".join(f"{pad}{line}" if line else "" for line in lines)
    return f"{key}: |\n{body}\n"


def yaml_scalar(key: str, value: str, indent: int = 2) -> str:
    """渲染单行字符串（JWT 只含 base64url 字符与点号，双引号包裹即可安全表达）"""
    pad = " " * indent
    return f'{pad}{key}: "{value}"\n'


def render_secret_yaml(
    secret_name: str,
    namespace: str,
    public_key_pem: str,
    private_key_pem: str,
    tokens: dict,
) -> str:
    """
    渲染 Kubernetes Secret 清单。

    使用 stringData 而非 data：
      stringData 是明文写法，便于人工检视、diff，以及被 SOPS / Sealed Secrets
      加密后安全入库；kubectl apply 时由 API Server 负责 base64 编码写入 data。
    """
    out = []
    out.append("# 由 jwt.py 生成的 Neon 共享 JWT Secret")
    out.append("#")
    out.append("# 安全提示：")
    out.append("#   1. 本文件包含 Ed25519 私钥，禁止明文提交到 Git 仓库；")
    out.append("#      如需入库请使用 SOPS / Sealed Secrets / External Secrets Operator 加密。")
    out.append("#   2. 应用后请设置合适的 RBAC，避免私钥被无关工作负载读取。")
    out.append("#   3. chart 侧不再创建该 Secret，只通过 global.jwt.existingSecret 引用它。")
    out.append("#")
    out.append("# 应用方式：")
    out.append(f"#   kubectl apply -f {secret_name}.secret.yaml")
    out.append(f"#   helm upgrade neon charts/neon -n {namespace} --set global.jwt.existingSecret={secret_name}")
    out.append("---")
    out.append("apiVersion: v1")
    out.append("kind: Secret")
    out.append("metadata:")
    out.append(f"  name: {secret_name}")
    out.append(f"  namespace: {namespace}")
    out.append("  labels:")
    out.append("    app.kubernetes.io/part-of: neon")
    out.append("type: Opaque")
    out.append("stringData:")
    # 公钥/私钥为多行 PEM，必须用块标量保留换行
    out.append(yaml_block_scalar("  publicKey.pem", public_key_pem, indent=4).rstrip("\n"))
    out.append(yaml_block_scalar("  privateKey.pem", private_key_pem, indent=4).rstrip("\n"))
    # 各 scope token 为单行 JWT
    for key in SCOPES:
        out.append(yaml_scalar(key, tokens[key], indent=2).rstrip("\n"))
    return "\n".join(out) + "\n"


# ---- 主流程 ----

def main():
    parser = argparse.ArgumentParser(description="Neon JWT 密钥对 & Token 生成器")
    parser.add_argument(
        "--key-file",
        help="已有私钥文件路径（PEM 格式）。若指定则跳过密钥生成，直接签发 token（密钥/token 轮换场景）",
    )
    parser.add_argument(
        "--output-dir",
        default="./jwt-out",
        help="输出目录（默认 ./jwt-out）",
    )
    parser.add_argument(
        "--namespace",
        default="neon",
        help="生成的 Secret 所在命名空间（默认 neon）",
    )
    parser.add_argument(
        "--secret-name",
        default="neon-jwt",
        help="生成的 Secret 名称（默认 neon-jwt），需与 --set global.jwt.existingSecret 一致",
    )
    parser.add_argument(
        "--stdout",
        action="store_true",
        help="将 Secret 清单打印到标准输出而非写文件（便于管道给 kubectl / SOPS）",
    )
    # 可选：允许用户指定特定 tenant_id（用于 Tenant scope 的 compute token）
    parser.add_argument(
        "--tenant-id",
        default="3d1f7595b468230304e0b73cecbcb081",
        # 该默认值仅为示例：控制面已移除"默认租户"概念，tenant 由 POST /projects 按需创建
        help="Tenant scope token 的 tenant_id（仅示例默认值，可替换为任意 32 位 hex）",
    )
    args = parser.parse_args()

    # Step 1: 获取/生成密钥对
    if args.key_file:
        log(f"[1/3] 从已有私钥加载: {args.key_file}")
        with open(args.key_file, "rb") as f:
            private_pem_bytes = f.read()
        private_raw = load_private_key_from_pem(private_pem_bytes)
        private_pem = private_pem_bytes.decode() if isinstance(private_pem_bytes, bytes) else private_pem_bytes
        # 从私钥推导公钥 PEM（保证与私钥严格配对）
        public_pem = public_pem_from_private_raw(private_raw).decode()
    else:
        log("[1/3] 生成 Ed25519 密钥对...")
        private_pem_b, public_pem_b, private_raw = generate_keypair()
        private_pem = private_pem_b.decode()
        public_pem = public_pem_b.decode()

    # Step 2: 签发各 scope JWT
    log("[2/3] 签发 JWT tokens...")
    tokens = {}
    for key, scope in SCOPES.items():
        if scope == "tenant":
            # Tenant scope 需要 tenant_id 字段
            payload = {"scope": scope, "tenant_id": args.tenant_id}
        else:
            payload = {"scope": scope}
        tokens[key] = make_jwt(private_raw, payload)

    secret_yaml = render_secret_yaml(
        secret_name=args.secret_name,
        namespace=args.namespace,
        public_key_pem=public_pem,
        private_key_pem=private_pem,
        tokens=tokens,
    )

    # Step 3: 输出
    if args.stdout:
        # --stdout 时标准输出只允许出现清单本身，否则 `jwt.py --stdout | kubectl apply -f -` 会解析失败
        log("[3/3] 输出 Secret 清单到标准输出")
        sys.stdout.write(secret_yaml)
        return

    log(f"[3/3] 写入输出文件到 {args.output_dir}/")
    os.makedirs(args.output_dir, exist_ok=True)

    with open(os.path.join(args.output_dir, "publicKey.pem"), "w") as f:
        f.write(public_pem)
    with open(os.path.join(args.output_dir, "privateKey.pem"), "w") as f:
        f.write(private_pem)
    # 私钥只给属主读写，避免同机其他用户读取
    os.chmod(os.path.join(args.output_dir, "privateKey.pem"), 0o600)

    secret_file = f"{args.secret_name}.secret.yaml"
    with open(os.path.join(args.output_dir, secret_file), "w") as f:
        f.write(secret_yaml)

    log("=" * 70)
    log("输出文件:")
    log(f"  {args.output_dir}/publicKey.pem         — Ed25519 公钥")
    log(f"  {args.output_dir}/privateKey.pem        — Ed25519 私钥（权限 600，请妥善保管！）")
    log(f"  {args.output_dir}/{secret_file} — Kubernetes Secret 清单")
    log("=" * 70)
    log("\n各 scope JWT token（前 40 字符）:")
    for key, scope in SCOPES.items():
        log(f"  {key:<24} scope={scope:<16} {tokens[key][:40]}...")
    log("=" * 70)
    log("\n使用方式:")
    log("  # 1) 应用 Secret（如需入库请先 SOPS 加密）")
    log(f"  kubectl apply -f {args.output_dir}/{secret_file}")
    log("")
    log("  # 2) 安装 / 升级 chart（chart 不再创建 Secret，只引用它）")
    log("  #    注意：PostgreSQL 连接串同样走外部 Secret，不再通过 --set 传明文")
    log(f"  helm upgrade --install neon charts/neon -n {args.namespace} --create-namespace \\")
    log(f"    --set global.jwt.existingSecret={args.secret_name} \\")
    log("    --set global.storageController.databaseUrl.existingSecret=storage-controller-pg-cluster")
    log("")
    log("  # 密钥轮换：重新生成密钥对后重签全部 token，再滚动重启所有组件")
    log(f"  python3 jwt.py --namespace {args.namespace} --secret-name {args.secret_name}")
    log(f"  kubectl apply -f {args.output_dir}/{secret_file}")
    log(f"  kubectl rollout restart sts,deploy -n {args.namespace}")
    log("")
    log("  # 仅轮换 token（沿用原私钥）")
    log(f"  python3 jwt.py --key-file {args.output_dir}/privateKey.pem --namespace {args.namespace}")


if __name__ == "__main__":
    main()
