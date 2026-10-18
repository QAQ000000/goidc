package httpapi

import (
	"fmt"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/store"
)

type appCatalogueItem struct {
	Descriptor               channel.Descriptor `json:"descriptor"`
	FactoryAvailable         bool               `json:"factory_available"`
	InstanceCount            int64              `json:"instance_count"`
	EnabledInstanceCount     int64              `json:"enabled_instance_count"`
	SecretConfiguredCount    int64              `json:"secret_configured_count"`
	ChannelInstanceCount     int64              `json:"channel_instance_count"`
	SupplierAccountCount     int64              `json:"supplier_account_count"`
	ProfileCount             int64              `json:"profile_count"`
	SchemaIncompatibleCount  int64              `json:"schema_incompatible_count"`
	UnsupportedInstanceCount int64              `json:"unsupported_instance_count"`
	Status                   string             `json:"status"`
	ConfigurationURL         string             `json:"configuration_url"`
}

type appBuild struct {
	Version   *string `json:"version"`
	Revision  *string `json:"revision"`
	Modified  *bool   `json:"modified"`
	GoVersion *string `json:"go_version"`
}

type appCataloguePage struct {
	Items  []appCatalogueItem `json:"items"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
	Build  appBuild           `json:"build"`
}

type appListQuery struct {
	View, Category, Q, Status string
	Limit, Offset             int
}

func parseAppListQuery(values url.Values) (appListQuery, error) {
	out := appListQuery{View: "configured", Status: "all", Limit: 25}
	if v := strings.TrimSpace(values.Get("view")); v != "" {
		out.View = v
	}
	if out.View != "configured" && out.View != "catalogue" {
		return out, fmt.Errorf("应用视图无效")
	}
	out.Category = strings.TrimSpace(values.Get("category"))
	switch channel.Category(out.Category) {
	case "", channel.Payment, channel.OAuth, channel.SMS, channel.Mail, channel.Certification, channel.Provision, channel.Bot:
	default:
		return out, fmt.Errorf("应用类别无效")
	}
	if v := strings.TrimSpace(values.Get("status")); v != "" {
		out.Status = v
	}
	switch out.Status {
	case "all", "enabled", "disabled", "unconfigured", "implementation_missing", "configuration_incompatible":
	default:
		return out, fmt.Errorf("应用状态无效")
	}
	out.Q = strings.ToLower(strings.TrimSpace(values.Get("q")))
	if len(out.Q) > 400 {
		return out, fmt.Errorf("搜索内容过长")
	}
	for key, target := range map[string]*int{"limit": &out.Limit, "offset": &out.Offset} {
		if values.Has(key) {
			n, err := strconv.Atoi(values.Get(key))
			if err != nil || n < 0 || (key == "limit" && (n < 1 || n > 100)) {
				return out, fmt.Errorf("应用分页参数无效")
			}
			*target = n
		}
	}
	return out, nil
}

func appBuildMetadata(info *debug.BuildInfo) appBuild {
	var out appBuild
	if info == nil {
		return out
	}
	if info.Main.Version != "" && info.Main.Version != "(devel)" {
		version := info.Main.Version
		out.Version = &version
	}
	if info.GoVersion != "" {
		goVersion := info.GoVersion
		out.GoVersion = &goVersion
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if setting.Value != "" {
				revision := setting.Value
				out.Revision = &revision
			}
		case "vcs.modified":
			if modified, err := strconv.ParseBool(setting.Value); err == nil {
				out.Modified = &modified
			}
		}
	}
	return out
}

func compiledAppsPage(query appListQuery, counts []store.AppConfigurationCount) appCataloguePage {
	type key struct {
		category channel.Category
		id       string
	}
	state := make(map[key][]store.AppConfigurationCount)
	for _, count := range counts {
		k := key{count.Category, count.ImplementationID}
		state[k] = append(state[k], count)
	}
	descriptors := channel.List(channel.Category(query.Category))
	compiled := make(map[key]bool, len(descriptors))
	for _, descriptor := range descriptors {
		compiled[key{descriptor.Category, descriptor.ID}] = true
	}
	// Configuration outlives a particular build. Keep missing implementations
	// visible so administrators can inspect and disable their saved instances.
	for k, groups := range state {
		if compiled[k] || (query.Category != "" && string(k.category) != query.Category) {
			continue
		}
		mode := "channel"
		for _, group := range groups {
			if group.Source == "supplier" {
				mode = "supplier"
			}
		}
		descriptors = append(descriptors, channel.Descriptor{ID: k.id, Name: k.id + "（实现未编入）", Category: k.category, Version: "unavailable", InstanceMode: mode})
	}
	sort.Slice(descriptors, func(i, j int) bool {
		if descriptors[i].Category != descriptors[j].Category {
			return descriptors[i].Category < descriptors[j].Category
		}
		return descriptors[i].ID < descriptors[j].ID
	})
	items := make([]appCatalogueItem, 0)
	for _, descriptor := range descriptors {
		k := key{descriptor.Category, descriptor.ID}
		item := appCatalogueItem{Descriptor: descriptor, FactoryAvailable: channel.HasFactory(descriptor.Category, descriptor.ID)}
		for _, count := range state[k] {
			item.InstanceCount += count.InstanceCount
			item.EnabledInstanceCount += count.EnabledInstanceCount
			item.SecretConfiguredCount += count.SecretConfiguredCount
			switch count.Source {
			case "supplier":
				item.SupplierAccountCount += count.InstanceCount
			case "profile":
				item.ProfileCount += count.InstanceCount
			default:
				item.ChannelInstanceCount += count.InstanceCount
				if compiled[k] && count.SchemaVersion != descriptor.SchemaVersion {
					item.SchemaIncompatibleCount += count.InstanceCount
				}
				if compiled[k] && (descriptor.InstanceMode != "channel" || !item.FactoryAvailable) {
					item.UnsupportedInstanceCount += count.InstanceCount
				}
			}
		}
		if query.View == "configured" && item.InstanceCount == 0 {
			continue
		}
		status := "unconfigured"
		if !compiled[k] {
			status = "implementation_missing"
		} else if item.SchemaIncompatibleCount > 0 || item.UnsupportedInstanceCount > 0 {
			status = "configuration_incompatible"
		} else if item.EnabledInstanceCount > 0 {
			status = "enabled"
		} else if item.InstanceCount > 0 {
			status = "disabled"
		}
		if query.Status != "all" && query.Status != status {
			continue
		}
		searchable := descriptor.ID + " " + descriptor.Name + " " + string(descriptor.Category) + " " + strings.Join(descriptor.Capabilities, " ")
		if query.Q != "" && !strings.Contains(strings.ToLower(searchable), query.Q) {
			continue
		}
		item.ConfigurationURL = "/admin/channels?category=" + url.QueryEscape(string(descriptor.Category))
		switch descriptor.InstanceMode {
		case "supplier":
			item.ConfigurationURL = "/admin/suppliers"
		case "profile":
			item.ConfigurationURL = "/admin/products"
		}
		item.Status = status
		items = append(items, item)
	}
	page := appCataloguePage{Total: len(items), Limit: query.Limit, Offset: query.Offset, Items: make([]appCatalogueItem, 0)}
	if query.Offset < len(items) {
		// Subtract before adding to avoid overflowing an arbitrary client offset.
		end := query.Offset + min(query.Limit, len(items)-query.Offset)
		page.Items = items[query.Offset:end]
	}
	return page
}

func (s *Server) apps(w http.ResponseWriter, r *http.Request) {
	query, err := parseAppListQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_apps_query", err.Error())
		return
	}
	query.Limit, query.Offset = pagination(r)
	counts, err := s.db.AppConfigurationCounts(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if profiles := len(s.cfg.NOKVMProfiles); profiles > 0 {
		counts = append(counts, store.AppConfigurationCount{Source: "profile", Category: channel.Provision, ImplementationID: "nokvm", InstanceCount: int64(profiles), EnabledInstanceCount: int64(profiles)})
	}
	page := compiledAppsPage(query, counts)
	info, _ := debug.ReadBuildInfo()
	page.Build = appBuildMetadata(info)
	write(w, http.StatusOK, page)
}
