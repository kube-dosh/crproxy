# OCI Distribution Spec Conformance 记录

## 2026-11-26（最终交付二进制）

- 被测二进制：`bin/crproxy`，`crproxy 0.1.0-dev`
- Spec：OCI Distribution Spec `1.1`
- Conformance suite commit：`97274622c11112caa21efb8c52acca3c6b8fa7f1`
- 服务端：`127.0.0.1:15013`，HTTP，Bearer challenge + Docker token flow
- 仓库：`final/repo1`、`final/repo2`
- 结果：`850 passed`、`4 skipped`、`0 failed`、`0 error`、`854 total`
- 结果目录：`/tmp/crproxy-conformance-final-results-1795680396`

跳过项均为 conformance 对可选 mount 行为的自动降级记录：Registry 对可回退请求返回 `202` 并回退到普通 blob 上传，符合规范允许的行为。

### 运行命令

```bash
OCI_VERSION=1.1 \
OCI_REGISTRY=127.0.0.1:15013 \
OCI_TLS=disabled \
OCI_REPO1=final/repo1 \
OCI_REPO2=final/repo2 \
OCI_USERNAME=admin \
OCI_PASSWORD='<bootstrap-password>' \
OCI_API_BLOBS_UPLOAD_CANCEL=true \
OCI_RESULTS_DIR=/tmp/crproxy-conformance-final-results \
OCI_LOG=warn \
go run -buildvcs=true .
```

### 覆盖范围

ping、OCI image/index/artifact、subject/referrers、sha256/sha512、普通与 chunked/stream 上传、upload 取消、Range、mount 回退、tag 分页与删除、manifest 删除、错误 digest、非法 digest 格式、空 blob、自定义字段、nondistributable layer 等。

### 本轮历史问题回归

此前修复并继续通过：

- upload 状态查询/取消的 token scope 与方法语义。
- 同一 manifest 重复引用同一 descriptor 时的 `content_edges` 主键冲突。
- 不存在的 repository 查询 referrers 返回空 image index。
- 非法 digest 的 manifest PUT 优先返回 `400 DIGEST_INVALID`。

## 2026-11-26（发布前回归）

同一 suite 在 `127.0.0.1:15003` 上使用 `conformance2/repo1`、`conformance2/repo2` 完成 `850 passed / 4 skipped / 0 failed`。最终二进制在 `127.0.0.1:15013` 上重新执行同一 suite，结果未回退。
