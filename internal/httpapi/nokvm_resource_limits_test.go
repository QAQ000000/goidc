package httpapi

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/provision"
)

func TestNOKVMResourceRateLimitsArePerActor(t *testing.T) {
	remote := newNATRemote()
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/virtual/42":
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"cd_roms_id":0}}`)
		case "/api/cd_rom/":
			fmt.Fprint(w, `{"code":0,"data":[]}`)
		default:
			remote.handler(w, r)
		}
	}, true, func(d *provision.Delivery) { d.Resources.NATACL = 2 })
	f.process(t)
	for _, suffix := range []string{"nat/acl", "cdrom"} {
		path := fmt.Sprintf("/api/v1/services/%d/%s", f.sid, suffix)
		for i := 0; i < 30; i++ {
			expect(t, request(f.h, "GET", path, nil, f.customer, ""), 200)
		}
		expect(t, request(f.h, "GET", path, nil, f.customer, ""), 429)
		admin := fmt.Sprintf("/api/v1/admin/services/%d/%s", f.sid, suffix)
		expect(t, request(f.h, "GET", admin, nil, f.root, ""), 200)
		expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
	}
}
