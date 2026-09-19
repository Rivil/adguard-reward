package adguard

import (
	"context"
	"reflect"
	"testing"
)

func TestServices(t *testing.T) {
	s := newFake(t)
	c := newClient(t, s)
	ctx := context.Background()

	svcs, err := c.Services(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 3 {
		t.Fatalf("got %d services, want the fixture's 3", len(svcs))
	}
	for _, sv := range svcs {
		if sv.ID == "" || sv.Name == "" || sv.Icon != "PHN2Zy8+" {
			t.Errorf("service %+v: Icon must be icon_svg from the response", sv)
		}
	}

	// A catalogue the app could not have vendored: only what AdGuard serves
	// comes back (r-01).
	s.SetResponse("/control/blocked_services/all", 200, []byte(`{"blocked_services":[
		{"id":"zzz-not-in-any-catalogue","name":"Zzz","icon_svg":"QQ==","rules":[]},
		{"id":"tiktok","name":"TikTok","icon_svg":"Qg==","rules":[]}],"groups":[]}`))
	svcs, err = c.Services(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Service{{ID: "zzz-not-in-any-catalogue", Name: "Zzz", Icon: "QQ=="}, {ID: "tiktok", Name: "TikTok", Icon: "Qg=="}}
	if !reflect.DeepEqual(svcs, want) {
		t.Errorf("Services() = %+v, want exactly %+v", svcs, want)
	}

	s.SetResponse("/control/blocked_services/all", 200, []byte(`{"blocked_services":null}`))
	svcs, err = c.Services(ctx)
	if err != nil || svcs == nil || len(svcs) != 0 {
		t.Errorf("null catalogue: got %#v, %v; want empty slice, nil error", svcs, err)
	}
}
