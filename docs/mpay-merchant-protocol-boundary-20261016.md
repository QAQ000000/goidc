# 现用 MPAY 商户协议边界复核

日期：2026-10-16。只读源码复核，不调用商户 API、不创建订单、不补单、不出款、不修改商户代码。承接 [真实付款验收](real-payment-acceptance-20261013.md)，不替代新的网络或到账验收。

## 标准查单失败的原因

本机 82 端口商户站点为 `/www/wwwroot/127.0.0.1_82/mpay`。其 `public` 入口没有 `api.php`，`route/app.php:6`、`:8` 分别提供 `submit` 和 `mapi` 创建支付，但没有当前 Go 易支付适配器使用的 `/api.php?act=order` 商户查询契约。因此，既有验收中标准查单 404 与当前源文件布局一致；不能通过重试或忽略错误得到可信结果。本轮未重新发起网络请求。

Go 的 `internal/payment/epay_query.go` 要求成功响应完整绑定 PID、支付类型、本站支付号、渠道交易号、金额及状态。这些校验继续保留；HTTP 404、未付款或本地超时均不代表商户已关闭订单。

## 不能直接替用的接口

| 商户源码 | 实际语义 | 对接限制 |
| --- | --- | --- |
| `PayController.php:38` 的 `mapi` | 创建远端订单，返回内部 `trade_no` 和 `payurl` | 现有 Go 创建流程生成 `submit.php` 收银台 URL，没有在意图创建时保存这里返回的内部订单号。改用该协议须单独适配及验收，不能暗中改变现有易支付驱动。 |
| `PayController.php:93` 的 `getOrderState` | 通过商户内部 `order_id` 查询；未付返回 `state/passtime`，已付返回带 MD5 通知签名的 `return_url` | 不是按本站 `out_trade_no` 查询的标准商户接口。未付响应不证明最终关闭；已付响应也须按原商户凭据核验签名、订单、金额及原流水，不能只读 `state=1` 就入账。 |
| `api/OrderController.php:14` 的 `getOrders` | 管理后台订单列表 | 继承 `BaseController` 的 `Auth` 中间件，使用管理会话，不是商户 PID/密钥查询接口。 |
| 同文件的 `changeOrderState/doPayOrder/redoPayOrder/deleteOrder/batchTimeout` | 管理员改状态、人工补单、通知或删除/超时处理 | 属于写操作，不可作为只读查单或可信关单核验；本站不调用这些操作来制造支付成功或关闭证明。 |

认证依据为 `app/BaseController.php:38` 的 `Auth` 声明及 `app/middleware/Auth.php:18` 的管理会话检查；本轮只核对源码，没有尝试登录或绕过管理权限。

## 后续可验收路线

1. 优先使用提供正式商户查询、关单、退款协议的测试商户，保留现有易支付适配器的严格字段校验；查单和关单能力分别声明。
2. 如果继续使用本机 MPAY，需以它实际公开的商户协议建立独立编译期适配器，明确远端订单号保存、签名、金额、原凭据及异常恢复，再用真实通知遗漏场景验证收款查询。只有 `getOrderState` 不足以承诺可信关单或退款。
3. 新的未知关单核验只开放给实现只读 `LookupClose` 的驱动。现有易支付仍不虚构此能力；缺能力或证据时保留冻结。

G01 的公网 HTTPS 通知、可信主动查单和实际退款到账继续待验收。本批本地 mock/隔离浏览器结果不能关闭这些外部条件。
