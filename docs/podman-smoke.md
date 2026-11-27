# Podman 客户端冒烟记录

- 日期：2026-11-26
- 客户端：Podman 5.8.9（`/usr/bin/podman`）
- 约束：客户端验证只使用 Podman，不使用 Docker、crane、oras、containerd 或 nerdctl。
- 服务端：最终交付二进制 `bin/crproxy`、`crproxy 0.1.0-dev`。

## 1. 环境

- 本地 Registry 为明文 HTTP，客户端使用 `--tls-verify=false`，仅限本机冒烟。
- 上游 endpoint 均为 HTTPS；配置校验不接受明文 HTTP 上游。
- Podman 使用独立 `--root`、`--runroot`、`--authfile`，避免影响宿主机镜像和登录状态。
- SQLite 使用 rollback journal，不产生 WAL。
- HTTP/Bearer 实例：`127.0.0.1:15023`
- HTTPS/Basic 实例：`127.0.0.1:15024`
- 容器实例：`127.0.0.1:15025`
- 数据目录：`/tmp/crproxy-podman-closeout/data`
- Podman root：`/tmp/crproxy-podman-closeout/podman-root`
- Podman runroot：`/tmp/crproxy-podman-closeout/runroot`
- Authfile：`/tmp/crproxy-podman-closeout/auth.json`
- 出站代理：`http://127.0.0.1:1080`（只配置在 crproxy 服务端，Podman 直连本地 crproxy）

## 2. 启动与登录

```bash
make build
HTTP_PROXY=http://127.0.0.1:1080 \
HTTPS_PROXY=http://127.0.0.1:1080 \
NO_PROXY= \
CRPROXY_ADMIN_PASSWORD='podman-smoke-password' \
  bin/crproxy -config /tmp/crproxy-podman-closeout/config.yaml

podman --root /tmp/crproxy-podman-closeout/podman-root \
  --runroot /tmp/crproxy-podman-closeout/runroot \
  login --authfile /tmp/crproxy-podman-closeout/auth.json \
  --tls-verify=false -u admin -p 'podman-smoke-password' 127.0.0.1:15023
```

结果：`Login Succeeded!`。登录过程验证 `/v2/` Bearer challenge、`/v2/token` Basic 认证和 Docker Registry token flow。

## 3. Quay 首拉与缓存命中

```bash
podman --root /tmp/crproxy-podman-closeout/podman-root \
  --runroot /tmp/crproxy-podman-closeout/runroot \
  pull --authfile /tmp/crproxy-podman-closeout/auth.json --tls-verify=false \
  127.0.0.1:15023/r/quay/prometheus/busybox:latest
```

结果：成功，manifest、config 和全部 layer 通过 Quay challenge token 回源并写入本地 CAS。

删除 Podman 本地镜像后再次拉取同一 tag：

```bash
podman --root /tmp/crproxy-podman-closeout/podman-root \
  --runroot /tmp/crproxy-podman-closeout/runroot \
  rmi 127.0.0.1:15023/r/quay/prometheus/busybox:latest
```

再次 pull 成功，耗时约 `0.609s`。服务端日志中 manifest 与 blob 均为本地命中；缓存命中路径不重新下载内容。

## 4. GHCR 拉取

```bash
podman --root /tmp/crproxy-podman-closeout/podman-root \
  --runroot /tmp/crproxy-podman-closeout/runroot \
  pull --authfile /tmp/crproxy-podman-closeout/auth.json --tls-verify=false \
  127.0.0.1:15023/r/ghcr/actions/actions-runner:latest
```

结果：成功，耗时约 `1m10s`。manifest、config 和全部 layer 通过 GHCR challenge token 回源并写入本地 CAS。

## 5. 本地 push 与按 digest pull

```bash
PODMAN='podman --root /tmp/crproxy-podman-closeout/podman-root --runroot /tmp/crproxy-podman-closeout/runroot'
$PODMAN tag 35a735315ff67dfc699d1b2becb6a586d630454f51bef4b537730c94b665b420 \
  127.0.0.1:15023/local/crproxy-smoke:podman
$PODMAN push --authfile /tmp/crproxy-podman-closeout/auth.json --tls-verify=false \
  127.0.0.1:15023/local/crproxy-smoke:podman
$PODMAN pull --authfile /tmp/crproxy-podman-closeout/auth.json --tls-verify=false \
  127.0.0.1:15023/local/crproxy-smoke@sha256:8dee08674b0112cb855eb105c4b42fcd9aa6a11548e8df64d9a96449c3e53381
```

结果：两个 layer、config 和 manifest 上传成功。push 后 Podman 记录的远端 digest 为 `sha256:8dee08674b0112cb855eb105c4b42fcd9aa6a11548e8df64d9a96449c3e53381`，随后按该 digest 拉取成功。

注意：不能用拉取源镜像时 Podman 保存的本地 digest 假定 push 后的远端 digest。应在 push 后从 Registry 重新读取 digest 或使用 Podman 返回结果；本轮第一次按旧 digest 拉取得到 `manifest unknown`，验证了 Registry 没有错误地把不存在的内容当作成功，改用 push 后 digest 后通过。

## 6. 代理写拒绝

```bash
$PODMAN tag 35a735315ff67dfc699d1b2becb6a586d630454f51bef4b537730c94b665b420 \
  127.0.0.1:15023/r/quay/crproxy-smoke:rejected
$PODMAN push --authfile /tmp/crproxy-podman-closeout/auth.json --tls-verify=false \
  127.0.0.1:15023/r/quay/crproxy-smoke:rejected
```

结果：退出码 `125`，Podman 重试三次后报告：

```text
writing blob: initiating layer upload to /v2/r/quay/crproxy-smoke/blobs/uploads/: unsupported: method not allowed
```

服务端对 POST 立即返回 `405`，没有向上游发起写请求。

## 7. PAT 拉取

通过管理台 Session 创建只读 PAT 后，将 PAT 作为 Podman 密码完成 Distribution Basic 登录并拉取 Quay 镜像。结果成功，证明 PAT scope 与 Registry pull 鉴权链路可用；PAT 本身不能调用管理 API。

## 8. 原生 HTTPS + Basic challenge

第二个实例：`127.0.0.1:15024`，原生自签 TLS，`auth.registry.challenge=basic`，`hsts=true`。

```bash
podman --root /tmp/crproxy-podman-closeout/podman-root \
  --runroot /tmp/crproxy-podman-closeout/runroot \
  login --authfile /tmp/crproxy-podman-closeout/tls-auth.json --tls-verify=false \
  -u admin -p 'podman-smoke-password' 127.0.0.1:15024

podman --root /tmp/crproxy-podman-closeout/podman-root \
  --runroot /tmp/crproxy-podman-closeout/runroot \
  pull --authfile /tmp/crproxy-podman-closeout/tls-auth.json --tls-verify=false \
  127.0.0.1:15024/r/quay/prometheus/busybox:latest
```

结果：登录和拉取均成功；HTTPS 响应包含 `Strict-Transport-Security: max-age=31536000; includeSubDomains`。随后原子替换证书和私钥，等待 5 秒后 TLS serial 从 `142192E338DD9DC06B0E2CFA196AEB6042A61897` 变为 `19FDB4937A5234F25AD069FE63B691136413BB9C`，Podman 再次拉取成功，确认运行时热加载生效。

## 9. 容器化运行

`make container-offline` 使用本机静态二进制、配置和 CA bundle 构建 `scratch` 运行时镜像，绕开当前环境访问 Docker Hub 基镜超时的问题。容器以宿主端口 `15025` 启动并设置 `CRPROXY_PUBLIC_HOST=127.0.0.1:15025`，验证：

```text
health=healthy running=true
uid=65532:65532 readonly=true cap-drop=ALL no-new-privileges
Login Succeeded!
Quay 首次 pull 成功：5.49s
删除本地镜像后再次 pull 成功：0.48s
GET / 返回内嵌 React 管理台：200
```

容器固定以 UID `65532`、只读根文件系统、`cap-drop=ALL`、`no-new-privileges` 运行；数据通过独立 volume 持久化到 `/var/lib/crproxy`。标准多阶段 Dockerfile 已提供，适用于可访问 Docker Hub、GCR 或企业镜像镜像站的环境。

## 10. HTTP/HTTPS 出站代理

当前环境直连 Docker Hub 时 TLS 建连被重置，registry.k8s.io 的 manifest 重定向目标超时。启动基线使用本机 HTTP CONNECT 代理 `127.0.0.1:1080`：

```yaml
outbound_proxy:
  http_proxy: http://127.0.0.1:1080
  https_proxy: http://127.0.0.1:1080
  no_proxy: 127.0.0.1,localhost
```

`outbound_proxy` 块省略时仍回退到 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`。管理台“系统设置 → 出站代理”保存的运行时覆盖会立即替换已构建上游 Transport 使用的代理，并关闭旧空闲连接。

```bash
PODMAN='podman --root /tmp/crproxy-proxy-podman/root --runroot /tmp/crproxy-proxy-podman/runroot'
AUTH=/tmp/crproxy-proxy-podman/auth.json

$PODMAN pull --authfile "$AUTH" --tls-verify=false \
  127.0.0.1:15023/r/dockerhub/busybox:latest
$PODMAN pull --authfile "$AUTH" --tls-verify=false \
  127.0.0.1:15023/r/k8s/pause:3.10
```

运行时代理热切换结果：

```text
Docker Hub busybox:latest     首次 12.60s，删除本地镜像后 1.42s
registry.k8s.io pause:3.10    首次 9.27s
Docker Hub hello-world:latest 切到不可用代理 127.0.0.1:1081 后返回 502
Docker Hub hello-world:latest 切回 127.0.0.1:1080 后拉取成功
```

临时验收实例的进程环境已清空全部代理变量。先创建上游，再通过管理 API 把出站代理从空值热切换到 `127.0.0.1:1080`，证明既有上游立即使用新代理；死代理失败与切回成功的对照证明下一次上游请求没有复用旧 Transport。Podman 本身直连本地 crproxy，不使用出站代理。

自动化回归测试使用独立子进程和本地测试代理，避免 Go `ProxyFromEnvironment` 的进程级缓存掩盖回归：

- `TestUpstreamUsesHTTPProxyFromEnvironment`：验证 `HTTP_PROXY` 的绝对 URI 转发。
- `TestUpstreamUsesHTTPSProxyTunnelFromEnvironment`：验证 `HTTPS_PROXY` 的 CONNECT 隧道。
- `TestUpstreamUsesConfiguredHTTPProxy`：验证 YAML 配置的 HTTP 代理。
- `TestUpstreamConfiguredProxyHonorsNoProxy`：验证配置中的 `NO_PROXY` 绕过规则。
- `TestUpstreamConfiguredProxySendsCredentials`：验证配置中的代理 Basic 凭证。
- `TestUpstreamHotSwitchesConfiguredProxy`：验证不重建上游即可让下一条请求切换到新代理。

## 11. 自动化验证

```bash
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
npm --prefix web test
npm --prefix web run typecheck
npm --prefix web run build
```

全部通过。
