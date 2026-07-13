#!/usr/bin/env python3
"""
Neon JWT 密钥对 & 各 scope Token 生成脚本

用途：
  1. 生成 Ed25519 密钥对（publicKey.pem / privateKey.pem）
  2. 用私钥签发各 scope 的 JWT token（PageServerApi / SafekeeperData / Admin / ControlPlane / Tenant）
  3. 输出可直接用于 helm --set-file / --set 的参数

依赖：
  pip install cryptography

用法：
  python3 gen_jwt.py [--output-dir ./jwt-out]
  或从已有私钥生成 token：
  python3 gen_jwt.py --key-file privateKey.pem

输出文件：
  publicKey.pem          — Ed25519 公钥 (PEM)
  privateKey.pem         — Ed25519 私钥 (PEM)
  tokens.env             — shell 环境变量（方便 eval 后传给 helm）
  helm-set-args.txt      — 直接粘贴到 helm install 命令的参数
"""

import argparse
import base64
import json
import os
import sys

# Ed25519 的 JWT 签名采用 EdDSA 算法
# JWT 结构: base64url(header).base64url(payload).base64url(signature)


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


# ---- 主流程 ----

def main():
    parser = argparse.ArgumentParser(description="Neon JWT 密钥对 & Token 生成器")
    parser.add_argument(
        "--key-file",
        help="已有私钥文件路径（PEM 格式）。若指定则跳过密钥生成，直接签发 token",
    )
    parser.add_argument(
        "--output-dir",
        default="./jwt-out",
        help="输出目录（默认 ./jwt-out）",
    )
    # 可选：允许用户指定特定 tenant_id（用于 Tenant scope 的 compute token）
    parser.add_argument(
        "--tenant-id",
        default="3d1f7595b468230304e0b73cecbcb081",
        help="Tenant scope token 的 tenant_id（默认与 umbrella values 中的 defaultTenantId 一致）",
    )
    args = parser.parse_args()

    # Step 1: 获取/生成密钥对
    if args.key_file:
        print(f"[1/4] 从已有私钥加载: {args.key_file}")
        with open(args.key_file, "rb") as f:
            private_pem = f.read()
        private_raw = load_private_key_from_pem(private_pem)
        # 从私钥推导公钥 PEM
        from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey
        from cryptography.hazmat.primitives import serialization
        private_key = Ed25519PrivateKey.from_private_bytes(private_raw)
        public_key = private_key.public_key()
        public_pem = public_key.public_bytes(
            encoding=serialization.Encoding.PEM,
            format=serialization.PublicFormat.SubjectPublicKeyInfo,
        )
    else:
        print("[1/4] 生成 Ed25519 密钥对...")
        private_pem, public_pem, private_raw = generate_keypair()

    # Step 2: 签发各 scope JWT
    print("[2/4] 签发 JWT tokens...")

    # 5 种 scope 定义（对齐 Neon 架构语义）
    scopes = {
        "pageserverJwtToken": "pageserverapi",      # PageServerApi — SC → pageserver（SC 调 pageserver 用）
        "safekeeperJwtToken": "safekeeperdata",     # SafekeeperData — SC → safekeeper
        "controlPlaneJwtToken": "controlplane",     # ControlPlane — SC → control-plane upcall
        "peerJwtToken": "admin",                    # Admin — SC 副本间通信 / control-plane 管理接口
        "computeJwtToken": "tenant",                # Tenant — compute → pageserver/safekeeper（含 tenant_id）
        "generationsApiJwtToken": "generations_api",  # GenerationsApi — pageserver → SC 的 upcall(re-attach/validate)
    }

    tokens = {}
    for key, scope in scopes.items():
        if scope == "tenant":
            # Tenant scope 需要 tenant_id 字段
            payload = {"scope": scope, "tenant_id": args.tenant_id}
        else:
            payload = {"scope": scope}
        tokens[key] = make_jwt(private_raw, payload)

    # Step 3: 写入输出文件
    print(f"[3/4] 写入输出文件到 {args.output_dir}/")
    os.makedirs(args.output_dir, exist_ok=True)

    # 密钥文件（注意：public_pem 和 private_pem 可能是 bytes 或 str）
    public_key_str = public_pem.decode() if isinstance(public_pem, bytes) else public_pem
    private_key_str = private_pem.decode() if isinstance(private_pem, bytes) else private_pem

    with open(os.path.join(args.output_dir, "publicKey.pem"), "w") as f:
        f.write(public_key_str)
    with open(os.path.join(args.output_dir, "privateKey.pem"), "w") as f:
        f.write(private_key_str)

    # tokens.env —— 方便 shell 中 eval 后传给 helm
    env_lines = []
    env_lines.append(f'export NEON_PUBLIC_KEY={public_key_str!r}')
    env_lines.append(f'export NEON_PRIVATE_KEY={private_key_str!r}')
    for key, tok in tokens.items():
        env_lines.append(f'export NEON_{key.upper()}={tok}')
    with open(os.path.join(args.output_dir, "tokens.env"), "w") as f:
        f.write("\n".join(env_lines) + "\n")

    # helm-set-args.txt —— 直接拼接 helm install 参数
    with open(os.path.join(args.output_dir, "helm-set-args.txt"), "w") as f:
        f.write("\\\n")
        f.write("  --set-file global.jwt.publicKey=jwt-out/publicKey.pem \\\n")
        f.write("  --set-file global.jwt.privateKey=jwt-out/privateKey.pem \\\n")
        for key, tok in tokens.items():
            f.write(f"  --set global.jwt.{key}={tok} \\\n")
        # 也需要填 neon-storage-controller 的 JWT
        f.write(f"  --set neon-storage-controller.settings.jwtToken={tokens['pageserverJwtToken']} \\\n")
        f.write(f"  --set neon-storage-controller.settings.safekeeperJwtToken={tokens['safekeeperJwtToken']} \\\n")
        f.write(f"  --set neon-storage-controller.settings.controlPlaneJwtToken={tokens['controlPlaneJwtToken']} \\\n")
        f.write(f"  --set neon-storage-controller.settings.peerJwtToken={tokens['peerJwtToken']} \\\n")
        f.write(f"  --set-file neon-storage-controller.settings.publicKey=jwt-out/publicKey.pem\n")

    # Step 4: 打印结果摘要
    print("[4/4] 生成完成！\n")
    print("=" * 70)
    print("📁 输出文件:")
    print(f"  {args.output_dir}/publicKey.pem    — Ed25519 公钥")
    print(f"  {args.output_dir}/privateKey.pem   — Ed25519 私钥（请妥善保管！）")
    print(f"  {args.output_dir}/tokens.env       — shell 环境变量（eval 后可用）")
    print(f"  {args.output_dir}/helm-set-args.txt — helm install 参数（直接粘贴）")
    print("=" * 70)
    print("\n📋 各 scope JWT token:")
    print(f"{'Scope':<25} {'Token 值（前50字符）...'}")
    print("-" * 70)
    scope_labels = {
        "pageserverJwtToken": "PageServerApi (SC→pageserver)",
        "safekeeperJwtToken": "SafekeeperData (SC→safekeeper)",
        "controlPlaneJwtToken": "ControlPlane (SC→control-plane)",
        "peerJwtToken": "Admin (SC 副本间)",
        "computeJwtToken": f"Tenant (compute→存储, tid={args.tenant_id})",
    }
    for key, tok in tokens.items():
        label = scope_labels.get(key, key)
        print(f"  {label:<25} {tok[:50]}...")
    print("=" * 70)
    print("\n🚀 使用方式:")
    print("  # 方式一：直接粘贴参数（需要先 cd 到 charts 目录）")
    print(f"  cd charts/neon")
    print(f"  helm install neon . -n neon --create-namespace \\")
    print(f"    --set neon-storage-controller.settings.databaseUrl='postgres://user:pass@host:5432/sc' \\")
    print(f"    $(cat {os.path.abspath(args.output_dir)}/helm-set-args.txt)")
    print()
    print("  # 方式二：使用 tokens.env")
    print(f"  source {args.output_dir}/tokens.env")
    print('  helm install neon . -n neon --create-namespace \\')
    print('    --set-file global.jwt.publicKey=$NEON_PUBLIC_KEY \\')
    print('    ...')


if __name__ == "__main__":
    main()
