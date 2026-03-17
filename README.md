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

## Web UI 认证

项目支持通过 `.env` 配置 Web UI 管理员账号密码。未配置时，Web UI 默认不启用认证。

先复制示例文件：

```bash
cp .env.example .env
```

然后填写：

```env
MYCFNET_LISTEN_ADDR=:8080
MYCFNET_ADMIN_USER=admin
MYCFNET_ADMIN_PASS=change-this-password
```

重新启动后，服务会监听你配置的地址和端口；除 `/healthz` 外的页面和导出接口都会启用 HTTP Basic Auth。

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

## GitHub 编译

仓库已带 GitHub Actions 构建工作流：

- 推送到 `main` 会自动编译
- 打 `v*` 标签会自动创建 GitHub Release 并上传编译产物
- 也可以在 GitHub Actions 页面手动触发

`main` 分支默认构建产物：

- `mycfnet-linux-amd64`
- `mycfnet-linux-arm64`

发版时默认上传到 Releases 的压缩包：

- `mycfnet-vX.Y.Z-linux-amd64.tar.gz`
- `mycfnet-vX.Y.Z-linux-arm64.tar.gz`

推荐发版方式：

```bash
git tag v0.1.0
git push origin v0.1.0
```

推送标签后，GitHub 会自动：

- 编译 Linux 二进制
- 打包 `.env.example`、`README.md`、`data` 里的基础数据文件
- 创建对应版本的 Release
- 把压缩包挂到 Release 页面
