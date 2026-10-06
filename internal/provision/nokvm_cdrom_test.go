package provision

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNOKVMCDROMProtocolAndHostProof(t *testing.T) {
	writes := 0
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if len(q) != 3 || q.Get("signature") != Signature("cdrom-token", q.Get("time"), q.Get("random")) {
			t.Error("unsigned request")
		}
		if r.Method != "GET" {
			t.Error("unexpected method")
		}
		switch r.URL.Path {
		case "/api/virtual/42":
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"cd_roms_id":0,"secret":"hidden"}}`)
		case "/api/cd_rom/":
			fmt.Fprint(w, `{"code":0,"data":[{"id":9007199254740993,"name":"Rescue ISO","desc":"Recovery","path":"private.iso"}]}`)
		case "/api/virtual_cd_rom/42/mount/9007199254740993":
			writes++
			fmt.Fprint(w, `{"code":0}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer remote.Close()
	n, err := New(Profile{ID: "test", Name: "test", URL: remote.URL, Token: "cdrom-token"})
	if err != nil {
		t.Fatal(err)
	}
	listing, err := n.ListCDROM(t.Context(), "42")
	if err != nil || len(listing.Items) != 1 || listing.Items[0].ID != "9007199254740993" {
		t.Fatal(listing, err)
	}
	raw, _ := json.Marshal(listing)
	if strings.Contains(string(raw), "private") || strings.Contains(string(raw), "secret") {
		t.Fatal("supplier data leaked", string(raw))
	}
	req := CDROMRequest{Operation: "mount", ImageID: listing.Items[0].ID, ProfileID: "test", UpstreamID: "42", Image: listing.Items[0]}
	result, err := n.ExecuteCDROM(t.Context(), req)
	var failure *Error
	if !errors.As(err, &failure) || !failure.Unknown || failure.Code != "cdrom_confirmation_pending" || !result.CommandAccepted || writes != 1 {
		t.Fatal(result, err, writes)
	}
	req.Image.Name = "forged"
	_, err = n.ExecuteCDROM(t.Context(), req)
	if !errors.As(err, &failure) || failure.Unknown || writes != 1 {
		t.Fatal("forged catalog reached command", err, writes)
	}
}

func TestNOKVMCDROMRejectsWrongHostAndAmbiguousCatalog(t *testing.T) {
	for _, catalog := range []string{`[{"id":1,"name":"A"},{"id":1,"name":"B"}]`, `[{"id":1,"name":"bad\nname"}]`, `[{"id":0,"name":"A"}]`} {
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/virtual/42" {
				fmt.Fprint(w, `{"code":0,"data":{"id":42,"cd_roms_id":0}}`)
			} else {
				fmt.Fprintf(w, `{"code":0,"data":%s}`, catalog)
			}
		}))
		n, _ := New(Profile{ID: "test", Name: "test", URL: remote.URL, Token: "token"})
		if _, err := n.ListCDROM(t.Context(), "42"); err == nil {
			t.Fatal("accepted unsafe catalog", catalog)
		}
		remote.Close()
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"id":43,"cd_roms_id":0}}`)
	}))
	defer remote.Close()
	n, _ := New(Profile{ID: "test", Name: "test", URL: remote.URL, Token: "token"})
	if _, err := n.ListCDROM(t.Context(), "42"); err == nil {
		t.Fatal("accepted wrong host")
	}
}
