package connectix

import (
	"net/http"
	"reflect"
	"testing"
)

func TestTemplatesListTheirSlots(t *testing.T) {
	const body = `[
	 {"id":"a1","media":null,"layout":"text","name":"Code","channel":"sms","text":"HuboGO: {{ code }} x {{minutes}} {{ code }}","buttonLabel":null,"buttonUrl":null,"status":"approved"},
	 {"id":"a2","media":null,"layout":"text","name":"Plain","channel":"sms","text":"No slots here","buttonLabel":null,"buttonUrl":null,"status":"pending"},
	 {"id":"a3","media":null,"layout":"text","name":"Empty","channel":"viber","text":null,"buttonLabel":null,"buttonUrl":null,"status":"rejected"},
	 {"id":"a4","media":null,"layout":"text","name":"Odd","channel":"sms","text":"{{ order_id }} {{bad slot}} {{ a-b }} {{}} {{  x1  }}","buttonLabel":null,"buttonUrl":null,"status":"approved"}
	]`
	var method, path string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		_, _ = w.Write([]byte(body))
	})
	got, err := c.ListTemplates(t.Context())
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if method != http.MethodGet || path != "/templates" {
		t.Errorf("request = %s %s, want GET /templates", method, path)
	}
	want := []Template{
		{ID: "a1", Name: "Code", Channel: "sms", Status: "approved", Text: "HuboGO: {{ code }} x {{minutes}} {{ code }}", Slots: []string{"code", "minutes"}},
		{ID: "a2", Name: "Plain", Channel: "sms", Status: "pending", Text: "No slots here"},
		{ID: "a3", Name: "Empty", Channel: "viber", Status: "rejected"},
		{ID: "a4", Name: "Odd", Channel: "sms", Status: "approved", Text: "{{ order_id }} {{bad slot}} {{ a-b }} {{}} {{  x1  }}", Slots: []string{"order_id", "x1"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("templates =\n%+v\nwant\n%+v", got, want)
	}
}

func TestAnEmptyTemplateListIsNotAnError(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`[]`)) })
	got, err := c.ListTemplates(t.Context())
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want no templates and no error", got, err)
	}
}

func TestMeReturnsTheApplicationName(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"name":"HuboGO"}`)) })
	got, err := c.Me(t.Context())
	if err != nil || got != "HuboGO" {
		t.Errorf("Me = %q, %v; want HuboGO", got, err)
	}
}

func TestListTemplatesAndMeReportAnUnauthorizedToken(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`"This action needs a valid token!"`))
	})
	if _, err := c.ListTemplates(t.Context()); !isErr(err, ErrUnauthorized) {
		t.Errorf("ListTemplates err = %v, want ErrUnauthorized", err)
	}
	if _, err := c.Me(t.Context()); !isErr(err, ErrUnauthorized) {
		t.Errorf("Me err = %v, want ErrUnauthorized", err)
	}
}
