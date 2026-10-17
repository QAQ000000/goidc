package httpapi

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

var siteHexColor = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

type themeSettingsInput struct {
	Template        string `json:"template"`
	PrimaryColor    string `json:"primary_color"`
	LogoVariant     string `json:"logo_variant"`
	FooterText      string `json:"footer_text"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Server) themeSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.listOne(w, r, "SELECT template,primary_color,logo_variant,footer_text,version,updated_at FROM site_theme_settings WHERE id=1")
		return
	}
	var in themeSettingsInput
	if !decode(w, r, &in) {
		return
	}
	in.Template = strings.TrimSpace(in.Template)
	in.PrimaryColor = strings.TrimSpace(in.PrimaryColor)
	in.LogoVariant = strings.TrimSpace(in.LogoVariant)
	in.FooterText = strings.TrimSpace(in.FooterText)
	if in.Template != "default" && in.Template != "classic" && in.Template != "minimal" || !siteHexColor.MatchString(in.PrimaryColor) || in.LogoVariant != "full" && in.LogoVariant != "compact" && in.LogoVariant != "text" || len([]rune(in.FooterText)) > 500 || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_theme_settings", "主题配置无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "theme.settings", key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{200, replay}, nil
		}
		var version int64
		if version, err = store.LockVersion(r.Context(), tx, "site_theme_settings", 1, in.ExpectedVersion, "主题配置已变更，请刷新后重试"); err != nil {
			return txResponse{}, err
		}
		var out map[string]any
		row, err := tx.Query(r.Context(), `UPDATE site_theme_settings SET template=$1,primary_color=$2,logo_variant=$3,footer_text=$4,version=version+1,updated_at=now() WHERE id=1 RETURNING template,primary_color,logo_variant,footer_text,version,updated_at`, in.Template, in.PrimaryColor, in.LogoVariant, in.FooterText)
		if err == nil {
			out, err = pgx.CollectExactlyOneRow(row, pgx.RowToMap)
		}
		if err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, actor, "theme.settings_updated", "site_theme_settings", 1, map[string]any{"version": version + 1}); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, "theme.settings", key, hash, out); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, out}, nil
	})
}

type siteFieldWrite struct {
	FieldKey        string `json:"field_key"`
	Label           string `json:"label"`
	FieldType       string `json:"field_type"`
	Description     string `json:"description"`
	Required        bool   `json:"required"`
	Active          bool   `json:"active"`
	SortOrder       int    `json:"sort_order"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Server) siteFields(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.siteContentPaged(w, r, `SELECT id,field_key,label,field_type,description,required,active,sort_order,version,created_at,updated_at FROM site_custom_fields`, `($1='' OR field_key ILIKE '%'||$1||'%' OR label ILIKE '%'||$1||'%') AND ($2='' OR active=($2='true'))`, strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("active"))
		return
	}
	id := int64(0)
	if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in siteFieldWrite
	if !decode(w, r, &in) {
		return
	}
	in.FieldKey = strings.TrimSpace(in.FieldKey)
	in.Label = strings.TrimSpace(in.Label)
	in.FieldType = strings.TrimSpace(in.FieldType)
	in.Description = strings.TrimSpace(in.Description)
	if in.FieldKey == "" || in.Label == "" || len(in.FieldKey) > 64 || len([]rune(in.Label)) > 120 || len([]rune(in.Description)) > 500 || in.SortOrder < -100000 || in.SortOrder > 100000 || in.FieldType != "text" && in.FieldType != "textarea" && in.FieldType != "url" && in.FieldType != "email" && in.FieldType != "number" && in.FieldType != "boolean" || (id > 0 && in.ExpectedVersion < 1) {
		writeError(w, 400, "invalid_site_field", "官网字段无效")
		return
	}
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input siteFieldWrite
	}{id, in})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		op := "site_field.create"
		if id > 0 {
			op = "site_field.update:" + strconv.FormatInt(id, 10)
		}
		if r.Method == http.MethodDelete {
			op = "site_field.delete:" + strconv.FormatInt(id, 10)
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, op, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{200, replay}, nil
		}
		var version int64
		if id > 0 {
			if err = tx.QueryRow(r.Context(), "SELECT version FROM site_custom_fields WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
				if err == pgx.ErrNoRows {
					return txResponse{}, &txHTTPError{404, "not_found", "官网字段不存在"}
				}
				return txResponse{}, err
			}
			if version != in.ExpectedVersion {
				return txResponse{}, &txHTTPError{409, "version_conflict", "官网字段已变更，请刷新后重试"}
			}
		}
		var result map[string]any
		if r.Method == http.MethodDelete {
			if _, err = tx.Exec(r.Context(), "DELETE FROM site_custom_fields WHERE id=$1", id); err == nil {
				result = map[string]any{"id": id, "deleted": true}
			}
		} else if id == 0 {
			err = tx.QueryRow(r.Context(), `INSERT INTO site_custom_fields(field_key,label,field_type,description,required,active,sort_order) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, in.FieldKey, in.Label, in.FieldType, in.Description, in.Required, in.Active, in.SortOrder).Scan(&id)
			result = map[string]any{"id": id, "version": 1}
		} else {
			_, err = store.UpdateVersioned(r.Context(), tx, "site_custom_fields", `field_key=$1,label=$2,field_type=$3,description=$4,required=$5,active=$6,sort_order=$7`, id, in.ExpectedVersion, in.FieldKey, in.Label, in.FieldType, in.Description, in.Required, in.Active, in.SortOrder)
			result = map[string]any{"id": id, "version": version + 1}
		}
		if err != nil {
			return txResponse{}, err
		}
		action := "site_field.created"
		if id > 0 && version > 0 {
			action = "site_field.updated"
		}
		if r.Method == http.MethodDelete {
			action = "site_field.deleted"
		}
		if err = store.Audit(r.Context(), tx, actor, action, "site_custom_field", id, result); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, op, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, result}, nil
	})
}

type siteNavWrite struct {
	ParentID        *int64 `json:"parent_id"`
	Label           string `json:"label"`
	HRef            string `json:"href"`
	Icon            string `json:"icon"`
	SortOrder       int    `json:"sort_order"`
	OpenNewTab      bool   `json:"open_new_tab"`
	Active          bool   `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Server) siteNavigation(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.siteContentPaged(w, r, `SELECT id,parent_id,label,href,icon,sort_order,open_new_tab,active,version,created_at,updated_at FROM site_navigation_items`, `($1='' OR label ILIKE '%'||$1||'%' OR href ILIKE '%'||$1||'%') AND ($2='' OR active=($2='true'))`, strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("active"))
		return
	}
	id := int64(0)
	if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in siteNavWrite
	if !decode(w, r, &in) {
		return
	}
	in.Label = strings.TrimSpace(in.Label)
	in.HRef = strings.TrimSpace(in.HRef)
	in.Icon = strings.TrimSpace(in.Icon)
	if in.Label == "" || in.HRef == "" || len([]rune(in.Label)) > 120 || len(in.HRef) > 2048 || in.SortOrder < -100000 || in.SortOrder > 100000 || (id > 0 && in.ExpectedVersion < 1) {
		writeError(w, 400, "invalid_navigation", "导航项无效")
		return
	}
	if u, e := url.Parse(in.HRef); e != nil || u.User != nil || strings.ContainsAny(in.HRef, "\\ \t\r\n") || (u.IsAbs() && u.Scheme != "https") {
		writeError(w, 400, "invalid_navigation", "导航链接无效")
		return
	}
	s.siteNavigationMutation(w, r, id, in)
}
func (s *Server) siteNavigationMutation(w http.ResponseWriter, r *http.Request, id int64, in siteNavWrite) {
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input siteNavWrite
	}{id, in})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		op := "site_navigation.create"
		if id > 0 {
			op = "site_navigation.update:" + strconv.FormatInt(id, 10)
		}
		if r.Method == http.MethodDelete {
			op = "site_navigation.delete:" + strconv.FormatInt(id, 10)
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, op, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{200, replay}, nil
		}
		var version int64
		if id > 0 {
			if err = tx.QueryRow(r.Context(), "SELECT version FROM site_navigation_items WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
				if err == pgx.ErrNoRows {
					return txResponse{}, &txHTTPError{404, "not_found", "导航项不存在"}
				}
				return txResponse{}, err
			}
			if version != in.ExpectedVersion {
				return txResponse{}, &txHTTPError{409, "version_conflict", "导航项已变更，请刷新后重试"}
			}
		}
		var result = map[string]any{"id": id}
		if r.Method == http.MethodDelete {
			_, err = tx.Exec(r.Context(), "DELETE FROM site_navigation_items WHERE id=$1", id)
			result["deleted"] = true
		} else if id == 0 {
			err = tx.QueryRow(r.Context(), `INSERT INTO site_navigation_items(parent_id,label,href,icon,sort_order,open_new_tab,active) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, in.ParentID, in.Label, in.HRef, in.Icon, in.SortOrder, in.OpenNewTab, in.Active).Scan(&id)
			result["id"] = id
			result["version"] = 1
		} else {
			_, err = store.UpdateVersioned(r.Context(), tx, "site_navigation_items", `parent_id=$1,label=$2,href=$3,icon=$4,sort_order=$5,open_new_tab=$6,active=$7`, id, in.ExpectedVersion, in.ParentID, in.Label, in.HRef, in.Icon, in.SortOrder, in.OpenNewTab, in.Active)
			result["version"] = version + 1
		}
		if err != nil {
			return txResponse{}, err
		}
		action := "site_navigation.created"
		if id > 0 && version > 0 {
			action = "site_navigation.updated"
		}
		if r.Method == http.MethodDelete {
			action = "site_navigation.deleted"
		}
		if err = store.Audit(r.Context(), tx, actor, action, "site_navigation_item", id, result); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, op, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, result}, nil
	})
}

type friendlyLinkWrite struct {
	Name            string `json:"name"`
	URL             string `json:"url"`
	LogoURL         string `json:"logo_url"`
	Description     string `json:"description"`
	SortOrder       int    `json:"sort_order"`
	Active          bool   `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Server) friendlyLinks(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.siteContentPaged(w, r, `SELECT id,name,url,logo_url,description,sort_order,active,version,created_at,updated_at FROM friendly_links`, `($1='' OR name ILIKE '%'||$1||'%' OR url ILIKE '%'||$1||'%') AND ($2='' OR active=($2='true'))`, strings.TrimSpace(r.URL.Query().Get("q")), r.URL.Query().Get("active"))
		return
	}
	id := int64(0)
	if r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		var ok bool
		id, ok = pathID(w, r)
		if !ok {
			return
		}
	}
	var in friendlyLinkWrite
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.URL = strings.TrimSpace(in.URL)
	in.LogoURL = strings.TrimSpace(in.LogoURL)
	in.Description = strings.TrimSpace(in.Description)
	if in.Name == "" || len([]rune(in.Name)) > 120 || len(in.URL) > 2048 || len(in.LogoURL) > 2048 || len([]rune(in.Description)) > 500 || in.SortOrder < -100000 || in.SortOrder > 100000 || (id > 0 && in.ExpectedVersion < 1) {
		writeError(w, 400, "invalid_friendly_link", "友情链接无效")
		return
	}
	for _, raw := range []string{in.URL, in.LogoURL} {
		if raw == "" {
			continue
		}
		u, e := url.Parse(raw)
		if e != nil || u.User != nil || u.Scheme != "https" || u.Hostname() == "" {
			writeError(w, 400, "invalid_friendly_link", "链接必须是 HTTPS 地址")
			return
		}
	}
	s.friendlyLinkMutation(w, r, id, in)
}
func (s *Server) friendlyLinkMutation(w http.ResponseWriter, r *http.Request, id int64, in friendlyLinkWrite) {
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(struct {
		ID    int64
		Input friendlyLinkWrite
	}{id, in})
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		op := "friendly_link.create"
		if id > 0 {
			op = "friendly_link.update:" + strconv.FormatInt(id, 10)
		}
		if r.Method == http.MethodDelete {
			op = "friendly_link.delete:" + strconv.FormatInt(id, 10)
		}
		if replay, replayed, e := loadSettingReplayTx(r, tx, actor, op, key, hash); e != nil {
			return txResponse{}, e
		} else if replayed {

			return txResponse{200, replay}, nil
		}
		var version int64
		if id > 0 {
			if err = tx.QueryRow(r.Context(), "SELECT version FROM friendly_links WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
				if err == pgx.ErrNoRows {
					return txResponse{}, &txHTTPError{404, "not_found", "友情链接不存在"}
				}
				return txResponse{}, err
			}
			if version != in.ExpectedVersion {
				return txResponse{}, &txHTTPError{409, "version_conflict", "友情链接已变更，请刷新后重试"}
			}
		}
		result := map[string]any{"id": id}
		if r.Method == http.MethodDelete {
			_, err = tx.Exec(r.Context(), "DELETE FROM friendly_links WHERE id=$1", id)
			result["deleted"] = true
		} else if id == 0 {
			err = tx.QueryRow(r.Context(), `INSERT INTO friendly_links(name,url,logo_url,description,sort_order,active) VALUES($1,$2,$3,$4,$5,$6) RETURNING id`, in.Name, in.URL, in.LogoURL, in.Description, in.SortOrder, in.Active).Scan(&id)
			result["id"] = id
			result["version"] = 1
		} else {
			_, err = store.UpdateVersioned(r.Context(), tx, "friendly_links", `name=$1,url=$2,logo_url=$3,description=$4,sort_order=$5,active=$6`, id, in.ExpectedVersion, in.Name, in.URL, in.LogoURL, in.Description, in.SortOrder, in.Active)
			result["version"] = version + 1
		}
		if err != nil {
			return txResponse{}, err
		}
		action := "friendly_link.created"
		if id > 0 && version > 0 {
			action = "friendly_link.updated"
		}
		if r.Method == http.MethodDelete {
			action = "friendly_link.deleted"
		}
		if err = store.Audit(r.Context(), tx, actor, action, "friendly_link", id, result); err != nil {
			return txResponse{}, err
		}
		if err = saveSettingOperationTx(r, tx, actor, op, key, hash, result); err != nil {
			return txResponse{}, err
		}

		return txResponse{200, result}, nil
	})
}

func (s *Server) listOne(w http.ResponseWriter, r *http.Request, query string) {
	rows, err := s.db.Pool.Query(r.Context(), query)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	item, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, item)
}

func (s *Server) siteContentPaged(w http.ResponseWriter, r *http.Request, selectSQL, where string, args ...any) {
	limit, offset := pagination(r)
	s.writeListPage(w, r, "SELECT count(*) FROM ("+selectSQL+" WHERE "+where+") AS rows",
		selectSQL+" WHERE "+where+" ORDER BY sort_order,id", args, limit, offset)
}

func (s *Server) adminFileGroups(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	active := r.URL.Query().Get("active")
	where := `FROM download_category_tree c WHERE ($1='' OR c.name ILIKE '%'||$1||'%' OR c.description ILIKE '%'||$1||'%') AND ($2='' OR c.active=($2='true'))`
	s.writeListPage(w, r, "SELECT count(*) "+where,
		`SELECT c.id,c.parent_id,c.name,c.description,c.sort_order,c.active,c.version,c.created_at,c.updated_at,c.path,c.depth,c.effective_active,(SELECT count(*) FROM downloads d WHERE download_category_contains(c.id,d.category_id)) AS file_count,(SELECT count(*) FROM download_categories child WHERE child.parent_id=c.id) AS child_count `+where+` ORDER BY c.sort_path`, []any{q, active}, limit, offset)
}
