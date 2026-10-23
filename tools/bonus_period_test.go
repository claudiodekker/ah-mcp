package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appie "github.com/gwillem/appie-go"
)

func day(s string) time.Time {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseWeekSelector(t *testing.T) {
	for _, ok := range []string{"", "current", "next", "2026-11-04"} {
		if _, err := parseWeekSelector(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"volgende", "NEXT", "2026-13-40", "tomorrow"} {
		if _, err := parseWeekSelector(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestSelectPeriod(t *testing.T) {
	w1 := bonusPeriod{BonusStartDate: "2026-10-26", BonusEndDate: "2026-11-01"}
	w2 := bonusPeriod{BonusStartDate: "2026-11-02", BonusEndDate: "2026-11-08"}
	cases := []struct {
		name    string
		periods []bonusPeriod
		today   string
		sel     weekSelector
		idx     int
		found   bool
		note    bool
	}{
		{"current", []bonusPeriod{w1, w2}, "2026-10-28", "current", 0, true, false},
		{"next", []bonusPeriod{w1, w2}, "2026-10-28", "next", 1, true, false},
		{"next unpublished", []bonusPeriod{w1}, "2026-10-28", "next", 0, false, false},
		{"reordered while order reopened: current by date", []bonusPeriod{w2, w1}, "2026-10-28", "current", 1, true, true},
		{"reordered: next by date", []bonusPeriod{w2, w1}, "2026-10-28", "next", 0, true, true},
		{"date in second period", []bonusPeriod{w1, w2}, "2026-10-28", "2026-11-04", 1, true, false},
		{"date outside all periods", []bonusPeriod{w1, w2}, "2026-10-28", "2027-01-01", 0, false, false},
		{"no period contains today", []bonusPeriod{w2}, "2026-10-28", "current", 0, true, true},
		{"no periods", nil, "2026-10-28", "current", 0, false, false},
		{"empty start date never matches", []bonusPeriod{{BonusEndDate: "2026-12-31"}}, "2026-10-28", "next", 0, false, false},
	}
	for _, c := range cases {
		idx, note, found := selectPeriod(c.periods, day(c.today), c.sel)
		if found != c.found || (found && idx != c.idx) || (note != "") != c.note {
			t.Errorf("%s: idx=%d note=%q found=%v, want idx=%d found=%v note=%v", c.name, idx, note, found, c.idx, c.found, c.note)
		}
	}
}

func TestGetBonusProductsForPeriodDatesSectionsAtPeriodStart(t *testing.T) {
	type call struct{ date, category, application string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		calls = append(calls, call{q.Get("date"), q.Get("category"), q.Get("application")})
		io.WriteString(w, `{"bonusGroupOrProducts":[
			{"product":{"webshopId":1,"title":"Melk","currentPrice":1.0,"priceBeforeBonus":1.5,"isBonus":true,"bonusMechanism":"1+1"}},
			{"bonusGroup":{"id":"seg1","segmentDescription":"Alle kaas","discountDescription":"25% korting"}},
			{"bonusGroup":{"id":"seg2","products":[{"webshopId":2,"title":"Inline"}]}}]}`)
	}))
	defer srv.Close()
	c := appie.New(appie.WithBaseURL(srv.URL), appie.WithTokens("a", "r"))

	p := bonusPeriod{BonusStartDate: "2026-11-02", BonusEndDate: "2026-11-08"}
	if err := json.Unmarshal([]byte(`[{"urlMetadataList":[
		{"bonusType":"NATIONAL","description":"Zuivel"},{"bonusType":"PERSONAL","description":"Skip"}]}]`), &p.Tabs); err != nil {
		t.Fatal(err)
	}

	got, err := getBonusProductsForPeriod(context.Background(), c, "AHBEWEBSHOP", p)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != (call{"2026-11-02", "Zuivel", "AHBEWEBSHOP"}) {
		t.Errorf("calls = %+v", calls)
	}
	var titles []string
	for _, pr := range got {
		titles = append(titles, pr.Title+"/"+pr.BonusSegmentID)
	}
	if strings.Join(titles, ",") != "Melk/,Alle kaas/seg1,Inline/" {
		t.Errorf("products = %v", titles)
	}
}

func TestGetBonusProductsForPeriodRejectsMissingStartDate(t *testing.T) {
	c := appie.New(appie.WithBaseURL("http://127.0.0.1:1"))
	if _, err := getBonusProductsForPeriod(context.Background(), c, "AHWEBSHOP", bonusPeriod{}); err == nil {
		t.Fatal("want error, not a fallback to today")
	}
}

func TestGetBonusGroupProductsForPeriodSendsThePeriod(t *testing.T) {
	var vars map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Variables map[string]any `json:"variables"`
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		vars = body.Variables
		io.WriteString(w, `{"data":{"bonusPromotions":[{"products":[{"id":5,"title":"Yoghurt","salesUnitSize":"1 l",
			"priceV2":{"now":{"amount":1.1},"was":{"amount":1.6},"promotionLabel":{"tiers":[{"description":"25% korting"}]}},
			"imagePack":[{"large":{"url":"http://img/1.png"}}]}]}]}}`)
	}))
	defer srv.Close()
	c := appie.New(appie.WithBaseURL(srv.URL), appie.WithTokens("a", "r"))

	got, err := getBonusGroupProductsForPeriod(context.Background(), c, "seg9", "2026-11-02", "2026-11-08")
	if err != nil {
		t.Fatal(err)
	}
	if vars["id"] != "seg9" || vars["periodStart"] != "2026-11-02" || vars["periodEnd"] != "2026-11-08" {
		t.Errorf("variables = %v", vars)
	}
	if len(got) != 1 || got[0].Title != "Yoghurt" || got[0].Price.Now != 1.1 || got[0].Price.Was != 1.6 ||
		got[0].BonusMechanism != "25% korting" || got[0].UnitSize != "1 l" || len(got[0].Images) != 1 {
		t.Errorf("product = %+v", got)
	}
}

func TestBonusOffersResultStatesThePeriod(t *testing.T) {
	out := bonusOffersResult{
		Period: &bonusPeriodInfo{Start: "2026-11-02", End: "2026-11-08"},
		Offers: bonusOffers([]appie.Product{
			{ID: 1, Title: "Kaas jong", Price: appie.Price{Now: 2, Was: 4}},
			{ID: 2, Title: "Melk", Price: appie.Price{Now: 1}},
		}, "kaas", 20),
	}
	b, _ := json.Marshal(out)
	var v struct {
		Period struct{ Start, End string }
		Offers []struct {
			Title              string
			DiscountPercentage float64 `json:"discount_percentage"`
		}
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if v.Period.Start != "2026-11-02" || v.Period.End != "2026-11-08" || len(v.Offers) != 1 || v.Offers[0].DiscountPercentage != 50 {
		t.Errorf("json = %s", b)
	}
}
