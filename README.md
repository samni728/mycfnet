# mycfnet

一个面向 Cloudflare 优选 IP 场景的自建扫描器。它会从候选 IP 或 CIDR 列表中采样，执行 TCP/TLS/HTTP 探测，解析 `CF-RAY` 里的数据中心代码，并把结果写入 SQLite，随后通过 Web UI 和导出接口供你复用。

## 功能

- 支持读取单 IP 和 CIDR 候选列表
- 支持对 CIDR 随机采样，适合 Cloudflare IPv4 / IPv6 段
- 支持 TCP 连接、TLS + HTTP 状态码探测
- 通过 `CF-RAY` 解析 colo，例如 `HKG`、`SJC`、`FRA`
- 使用 `locations.json` 把 colo 映射成城市和地区
- 扫描结果持久化到 SQLite
- 提供基础 Web UI
- 提供 `TXT` / `CSV` 导出

## 快速开始

```bash
mkdir -p data
cp /path/to/locations.json data/locations.json
cp /path/to/ips-v4.txt data/ips-v4.txt

go mod tidy
go run ./cmd/mycfnet
```

如果你已经有现成的 `locations.json` / `ips-v4.txt` / `ips-v6.txt`，也可以直接执行：

```bash
chmod +x scripts/import_assets.sh
./scripts/import_assets.sh /path/to/CFnat-Docker
```

默认访问地址：

- [http://127.0.0.1:8080](http://127.0.0.1:8080)

## 默认参数

- 候选文件：`data/ips-v4.txt`
- 域名：`cloudflaremirrors.com`
- 路径：`/debian`
- 端口：`443`
- TLS：开启

## 导出

- `GET /export.txt?active=true`
- `GET /export.csv?active=true`

`TXT` 导出格式：

```text
1.0.0.12:443
1.0.0.122:443
```

## 说明

- 第一版重点是把扫描、入库、导出和基础 UI 跑通
- `locations.json` 是 Cloudflare 站点代码映射，不是 IP 数据库
- 候选 IP 最好使用你自己的 `ips-v4.txt` / `ips-v6.txt`
- 如果目标站点返回头里没有 `CF-RAY`，则无法准确标注 colo
