// Package all is the explicit default binary composition root. Both API and
// worker import this profile so a registered catalogue entry means the same
// implementation in either process. Configuration never adds executable code.
package all

import (
	_ "github.com/codex/zjmfgo/internal/captcha"
	_ "github.com/codex/zjmfgo/internal/certification"
	_ "github.com/codex/zjmfgo/internal/filestorage"
	_ "github.com/codex/zjmfgo/internal/notification"
	_ "github.com/codex/zjmfgo/internal/payment"
	_ "github.com/codex/zjmfgo/internal/supplier"
)

const Profile = "default"
