# 常驻 Web 构建覆盖事故与恢复（2026-10-17）

本轮主代理错误地在常驻 Next 服务的工作目录 `web` 执行构建，覆盖了该服务正在读取的 `.next`。随后检查发现登录页 HTML 仍可响应，但引用的 JS/CSS 返回500。这是本轮操作造成的故障，不是本轮支付功能的验收结果。

## 恢复范围与来源

使用保留在独立目录 `/tmp/zjmfgo-release-20261016/web` 的原发布 Web 源码重新构建。主代理再次逐文件对比该目录与 `ae271c7` 的全部314个已跟踪 Web 文件，字节完全一致；运行中旧 API/worker 的 build info 为 `9d54a72 + dirty`，不能把它描述为当前干净主线。本次恢复选择原 Web 源码，不把需要迁移153的新前端部署到旧业务库。

在独立目录执行 `API_INTERNAL_URL=http://127.0.0.1:8088 npm run build -- --webpack`，结果PASS。将被覆盖的构建移至本地忽略目录 `output/live-web-recovery-20261017/overwritten-next`，安装独立重建的 `.next`，**只重启 `zjmfgo-web`**。没有更新API/worker、应用业务迁移或执行真实资金/资源动作。

原发布 BUILD_ID 为 `N1vvURzkvmu3kW0wOStjx`；误覆盖后观察到的磁盘ID不能作为该原发布证据。恢复 BUILD_ID 为 `0iI9JDdJluwxA2Y10gqnW`，由同源码重新构建产生，与原ID不同。原始旧构建已不存在；本次没有恢复其逐字节产物、伪造同名chunk或声称跨版本旧chunk保留验收通过。

| 检查 | 结果 |
| --- | --- |
| API PID / 二进制摘要前后 | `608393`；`1573fdcd0270275fa4fcdfbe8b4c61bdfd8afe06e98495cb4affd9e95c85e7ef`，均未改变 |
| worker PID / 二进制摘要前后 | `608413`；`e3298d0af181ec7f95cb64a18979ade5389681b418918ea9df0be43513b09fd6`，均未改变 |
| Web恢复 | active，PID `1647696`；恢复后BUILD_ID符合上述独立构建 |
| 本机HTML与引用资源 | `/admin-login`、`/login`、`/register`、`/`均200；分别9、12、12、18次资源检查均200 |
| API健康探针 | `/healthz`、`/readyz`均200；不等于完整业务健康 |
| 只读浏览器 | Playwright Chrome实际打开上述四页，分别采用1440/1024/390/320宽度，均200、正文可见、文档宽度等于视口；4张截图摘要已登记 |
| 浏览器错误 | JS异常0、非取消传输失败0；匿名访问 `/api/v1/me` 和 `/api/v1/auth/impersonation` 各401，为预期会话探测，浏览器对应2条资源错误，不能称所有请求均200 |

截图与去敏索引见 [恢复检查索引](generated/live-web-build-recovery-20261017.json)。人工复看1440后台登录及390注册截图，布局与控件可见；没有登录、提交注册、发送验证码或验证完整经营功能。每页仅一个尺寸的恢复冒烟，也不等于每页四尺寸深度验收。浏览器已关闭。

最初Chrome启动因root sandbox限制失败；添加已有独立浏览器配置后成功。第一次只读采集脚本错误使用了CLI回调签名，在导航或业务操作前失败；修正后完成上述检查。两次工具失败均未计为产品验收通过。

## 构建防护与剩余发布边界

已集成 `3dd0196`：`web/package.json` 的 `prebuild` 调用 [运行目录检查](../scripts/check-web-build-runtime.mjs)。Linux检测同真实cwd的Next常驻进程，有则拒绝构建。主目录仅运行 `npm run prebuild --prefix web`，实际识别PID `1647696` 并退出1，**没有再执行主目录构建**；独立worktree检查放行。真实子进程测试涵盖next-server、next start/dev、cwd软链接与无关fixture，合并后Node专项15/15通过（其中14项为支付恢复）。

该保护只提供检查时刻的cwd检测，不替代独立发布目录。直接调用Next、禁用lifecycle、共享 `.next` 或检查后才启动进程仍可绕过。正式一致发布、旧页面chunk保留、切流/切回和恢复验收继续按 [G04/G09契约](release-readiness-next-contract-20261017.md)推进，尚未关闭。本次是恢复原Web服务，不是部署本轮新功能。
