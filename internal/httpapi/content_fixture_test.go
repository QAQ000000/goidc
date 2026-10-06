package httpapi

import (
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestContentBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "content-super", func(db *store.Store, h http.Handler) {
		reader, _ := user(t, db, h, "content-reader", "admin")
		writer, cookie := user(t, db, h, "content-editor", "admin")
		contentRole(t, db, reader, "content.view")
		contentRole(t, db, writer, "content.view", "content.manage")
		user(t, db, h, "content-client", "customer")
		cid := contentCategoryID(t, h, cookie, categoryInput("knowledge", "client-help"))
		secondary := contentCategoryID(t, h, cookie, categoryInput("knowledge", "more-help"))
		tag := contentTagID(t, h, cookie, "knowledge", "setup-help")
		for _, level := range []string{"public", "login", "active_service"} {
			in := articleInput("knowledge", map[string]string{"public": "public-help", "login": "login-help", "active_service": "service-help"}[level], "published")
			in.Title = map[string]string{"public": "公开帮助", "login": "登录帮助", "active_service": "有效服务帮助"}[level]
			in.AccessLevel, in.CategoryID = level, &cid
			if level == "public" {
				in.CategoryIDs = []int64{cid, secondary}
				in.TagIDs = []int64{tag}
				sortOrder := 5
				in.SortOrder = &sortOrder
			}
			contentID(t, h, cookie, in)
		}
	})
}
