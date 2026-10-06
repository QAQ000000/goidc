package provision

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"
)

type CDROMImage struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type CDROMListing struct {
	Items     []CDROMImage `json:"items"`
	MountedID string       `json:"mounted_id"`
}

type CDROMRequest struct {
	Operation  string     `json:"operation"`
	ImageID    string     `json:"image_id"`
	ProfileID  string     `json:"profile_id"`
	UpstreamID string     `json:"upstream_id"`
	Image      CDROMImage `json:"image"`
	MountedID  string     `json:"mounted_id"`
	Prepared   bool       `json:"-"`
}

func IsCDROMAction(action string) bool { return action == "cdrom_mount" || action == "cdrom_unmount" }

func validCDROMText(s string, max int) bool {
	return utf8.ValidString(s) && utf8.RuneCountInString(s) <= max && strings.IndexFunc(s, unicode.IsControl) < 0
}

// The catalog is account-wide. Pair it with the service's virtual host read
// before exposing an item as a mount target or trusting mounted state.
func (n *NOKVM) ListCDROM(ctx context.Context, upstreamID string) (CDROMListing, error) {
	out := CDROMListing{Items: []CDROMImage{}}
	if !ValidResourceID(upstreamID) {
		return out, &Error{Code: "invalid_cdrom_request"}
	}
	var host struct {
		ID        json.Number  `json:"id"`
		MountedID *json.Number `json:"cd_roms_id"`
	}
	if err := n.call(ctx, http.MethodGet, "/api/virtual/"+upstreamID, nil, &host); err != nil {
		return out, err
	}
	if host.ID.String() != upstreamID || host.MountedID == nil || host.MountedID.String() == "" {
		return out, &Error{Code: "invalid_cdrom_host"}
	}
	if host.MountedID.String() != "0" {
		if !ValidResourceID(host.MountedID.String()) {
			return out, &Error{Code: "invalid_cdrom_host"}
		}
		out.MountedID = host.MountedID.String()
	}
	var rows []struct {
		ID          json.Number `json:"id"`
		Name        string      `json:"name"`
		Description string      `json:"desc"`
	}
	if err := n.call(ctx, http.MethodGet, "/api/cd_rom/", nil, &rows); err != nil {
		return CDROMListing{}, err
	}
	if len(rows) > 1000 {
		return CDROMListing{}, &Error{Code: "invalid_cdrom_catalog"}
	}
	seen := map[string]bool{}
	for _, row := range rows {
		id := row.ID.String()
		if !ValidResourceID(id) || seen[id] || strings.TrimSpace(row.Name) == "" || !validCDROMText(row.Name, 255) || !validCDROMText(row.Description, 1000) {
			return CDROMListing{}, &Error{Code: "invalid_cdrom_catalog"}
		}
		seen[id] = true
		out.Items = append(out.Items, CDROMImage{ID: id, Name: row.Name, Description: row.Description})
	}
	if out.MountedID != "" && !seen[out.MountedID] {
		return CDROMListing{}, &Error{Code: "invalid_cdrom_host"}
	}
	return out, nil
}

func (n *NOKVM) ExecuteCDROM(ctx context.Context, request CDROMRequest) (Result, error) {
	if request.ProfileID != n.profile.ID || !ValidResourceID(request.UpstreamID) || !ValidResourceID(request.ImageID) || request.Image.ID != request.ImageID || (request.Operation != "mount" && request.Operation != "unmount") {
		return Result{}, &Error{Code: "invalid_cdrom_request"}
	}
	listing, err := n.ListCDROM(ctx, request.UpstreamID)
	if err != nil {
		return Result{}, &Error{Code: "cdrom_preflight_unavailable"}
	}
	if listing.MountedID != request.MountedID {
		return Result{}, &Error{Code: "cdrom_state_changed"}
	}
	found := false
	for _, image := range listing.Items {
		if image == request.Image {
			found = true
			break
		}
	}
	if !found {
		return Result{}, &Error{Code: "cdrom_image_changed"}
	}
	if (request.Operation == "mount" && listing.MountedID != "") || (request.Operation == "unmount" && listing.MountedID != request.ImageID) {
		return Result{}, &Error{Code: "cdrom_state_changed"}
	}
	if ctx.Err() != nil {
		return Result{}, &Error{Code: "cdrom_cancelled"}
	}
	path := "/api/virtual_cd_rom/" + request.UpstreamID + "/" + request.Operation + "/" + request.ImageID
	if err = n.call(ctx, http.MethodGet, path, nil, nil); err != nil {
		return Result{}, err
	}
	return Result{UpstreamID: request.UpstreamID, CommandAccepted: true}, &Error{Code: "cdrom_confirmation_pending", Unknown: true}
}
