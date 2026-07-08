#!/usr/bin/env python

# 一次性注册脚本，把本集群的 Storage Controller 作为「虚拟 Pageserver」登记到 Neon 的控制平面（control plane / cplane）：
# 用 CPLANE_JWT_TOKEN 带上鉴权，向 CPLANE_URL 的控制平面 API 发送请求
# 先调用 get_pageserver_id 查询该 host 是否已注册
# 若未注册 → POST 注册，携带 region、zone、host、port 等信息，is_storage_controller=True
# 若已注册 → 直接跳过，打印已有的 node_id

# 调用者：charts/neon-storage-controller/templates/post-install-job.yaml 定义的 Helm post-install Hook Job。

# 在多租户/生产环境，control plane 需要知道每个 region 的 storage controller 在哪，才能下发租户、调度 pageserver


import os
import json
import logging
import urllib.request
import urllib.error

# region_id 在 console/cplan 中不同，带前缀 aws-<region>
REGION = os.environ["REGION_ID"]
# ZONE 环境变量由 init 容器自动生成
ZONE = os.environ["ZONE"]
HOST = os.environ["HOST"]
PORT = os.getenv("PORT", 80)

CPLANE_JWT_TOKEN = os.environ["CONTROL_PLANE_JWT_TOKEN"]

# 用于注册新的 pageserver
URL_PATH = "management/api/v2/pageservers"
# 用于获取 pageserver 列表
ADMIN_URL_PATH = f"regions/{REGION}/api/v1/admin/pageservers"

CPLANE_MANAGEMENT_URL = f"{os.environ['CPLANE_URL'].strip('/')}/{URL_PATH}"

PAYLOAD = dict(
    host=HOST,
    region_id=REGION,
    port=6400,
    disk_size=0,
    instance_id=HOST,
    http_host=HOST,
    http_port=int(PORT),
    availability_zone_id=ZONE,
    instance_type="",
    register_reason="Storage Controller Virtual Pageserver",
    active=True,
    is_storage_controller=True,
    # 硬编码为 0，因为没有任何地方会校验这个版本号。
    version=0,
)


def get_data(url, token, host=None, method="GET", data=None, raise_on_error=True):
    if host is not None:
        url = f"{url}/{host}"
    headers = {
        "Authorization": f"Bearer {token}",
        "Accept": "application/json",
        "Content-Type": "application/json",
    }
    # 检查该服务是否已被注册
    req = urllib.request.Request(url=url, headers=headers, method=method, data=data)
    try:
        with urllib.request.urlopen(req) as response:
            code = response.getcode()
            response_body = response.read()
    except urllib.error.HTTPError as e:
        code = e.code
        response_body = e.read()

    if code == 200:
        return json.loads(response_body)

    if raise_on_error:
        raise Exception(f'{method} {url} returned unexpected response: {code} {response_body}')

    return {}


def get_pageserver_id(url, token):
    data = get_data(url, token, HOST, raise_on_error=False)
    if "node_id" in data:
        return int(data["node_id"])


def register(url, token, payload):
    data = str(json.dumps(payload)).encode()
    response = get_data(url, token, data=data, method="POST")
    log.info(response)
    if "node_id" in response:
        return int(response["node_id"])


if __name__ == "__main__":
    logging.basicConfig(
        style="{",
        format="{asctime} {levelname:8} {name}:{lineno} {message}",
        level=logging.INFO,
    )

    log = logging.getLogger()

    log.info(
        json.dumps(
            dict(
                CPLANE_MANAGEMENT_URL=CPLANE_MANAGEMENT_URL,
                **PAYLOAD,
            ),
            indent=4,
        )
    )

    log.info("检查 pageserver 是否已注册")
    node_id = get_pageserver_id(
        CPLANE_MANAGEMENT_URL, CPLANE_JWT_TOKEN
    )

    if node_id is None:
        log.info("正在注册 storage controller")
        node_id = register(
            CPLANE_MANAGEMENT_URL, CPLANE_JWT_TOKEN, PAYLOAD
        )
        log.info(
            f"Storage controller registered with node_id {node_id}"
        )
    else:
        log.info(
            f"Storage controller already registered with node_id {node_id}"
        )
