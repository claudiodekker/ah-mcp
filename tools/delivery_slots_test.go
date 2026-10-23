package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	appie "github.com/gwillem/appie-go"
)

const slotsFixture = `{"data":{"orderDeliverySlots":[
 {"date":"2026-10-23T22:00:00.000Z","slots":[
  {"startTime":"2026-10-24T13:00:00.000Z","endTime":"2026-10-24T15:00:00.000Z","isFullyBooked":true},
  {"startTime":"2026-10-24T15:00:00.000Z","endTime":"2026-10-24T17:00:00.000Z","isFullyBooked":false}]},
 {"date":"2026-10-24T22:00:00.000Z","slots":[
  {"startTime":"2026-10-25T21:30:00.000Z","endTime":"2026-10-25T23:30:00.000Z","isFullyBooked":false}]},
 {"date":"2026-10-25T23:00:00.000Z","slots":[
  {"startTime":"2026-10-26T17:00:00.000Z","endTime":"2026-10-26T19:00:00.000Z","isFullyBooked":false}]}
]}}`

const detailsFixture = `{
 "address":{"street":"Dorpsstraat","houseNumber":12,"zipCode":1234,"zipCodeExtra":"AB","city":"Utrecht","countryCode":"NLD"},
 "deliveryTimePeriod":{"startDateTime":"2026-10-26T18:00:00","endDateTime":"2026-10-26T20:00:00"},
 "orderState":"SUBMITTED"}`

func fixtureDays(t *testing.T) []slotDay {
	t.Helper()
	var data struct {
		Data slotsData `json:"data"`
	}
	if err := json.Unmarshal([]byte(slotsFixture), &data); err != nil {
		t.Fatal(err)
	}
	return data.Data.Days
}

func TestParseWindow(t *testing.T) {
	from, to, err := parseWindow("2026-10-24", "2026-10-25", amsterdam)
	if err != nil {
		t.Fatal(err)
	}
	if from.Format(time.RFC3339) != "2026-10-24T00:00:00+02:00" {
		t.Errorf("from = %v", from)
	}
	// The end of 25 Oct is 26 Oct 00:00 local, after the DST change.
	if to.Format(time.RFC3339) != "2026-10-26T00:00:00+01:00" {
		t.Errorf("to = %v", to)
	}

	_, to, err = parseWindow("2026-10-24T09:30", "2026-10-26T12:00", amsterdam)
	if err != nil || to.Format(time.RFC3339) != "2026-10-26T12:00:00+01:00" {
		t.Errorf("timed to = %v, %v", to, err)
	}

	for _, bad := range [][2]string{{"nope", "2026-10-25"}, {"2026-10-24", "24-10"}, {"2026-10-25", "2026-10-24"}, {"2026-10-24T10:00", "2026-10-24T10:00"}} {
		if _, _, err := parseWindow(bad[0], bad[1], amsterdam); err == nil {
			t.Errorf("parseWindow(%v) should fail", bad)
		}
	}
}

func TestBuildAddressInput(t *testing.T) {
	got, err := buildAddressInput([]byte(detailsFixture))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"postalCode": "1234AB", "street": "Dorpsstraat", "houseNumber": 12, "city": "Utrecht", "countryCode": "NLD"}
	if len(got) != len(want) {
		t.Fatalf("extra fields sent: %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if _, err := buildAddressInput([]byte(`{"address":{"street":"x"}}`)); err == nil {
		t.Error("incomplete address should fail")
	}
}

func TestBuildSlotsResult(t *testing.T) {
	from, to, _ := parseWindow("2026-10-24", "2026-10-26", amsterdam)
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	res := buildSlotsResult(42, []byte(detailsFixture), fixtureDays(t), from, to, now, false)

	if res.CurrentSlot == nil || res.CurrentSlot.Label != "Mon 26 Oct 18:00-20:00" {
		t.Fatalf("current slot = %+v", res.CurrentSlot)
	}
	if res.FullCount != 1 || res.Full != nil {
		t.Errorf("full_count=%d full=%v", res.FullCount, res.Full)
	}
	if len(res.Free) != 3 {
		t.Fatalf("free = %+v", res.Free)
	}
	// 15:00Z on 24 Oct is 17:00 CEST.
	if res.Free[0].Start != "2026-10-24T17:00:00+02:00" || !res.Free[0].EarlierThanCurrent {
		t.Errorf("first free = %+v", res.Free[0])
	}
	// 21:30Z on 25 Oct is 22:30 CET (UTC+1, after the DST change at 01:00Z), so it falls on 25 Oct local.
	if s := res.Free[1]; s.Start != "2026-10-25T22:30:00+01:00" || s.Date != "2026-10-25" || s.End != "2026-10-26T00:30:00+01:00" {
		t.Errorf("DST slot = %+v", s)
	}
	// 17:00Z on 26 Oct is 18:00 CET, the current slot itself, so not earlier.
	if s := res.Free[2]; s.Start != "2026-10-26T18:00:00+01:00" || s.EarlierThanCurrent {
		t.Errorf("current-equal slot = %+v", s)
	}
	if res.EarliestFree == nil || res.EarliestFree.Start != res.Free[0].Start {
		t.Errorf("earliest = %+v", res.EarliestFree)
	}
	if res.Note != "" {
		t.Errorf("unexpected note %q", res.Note)
	}

	out, _ := json.Marshal(res)
	for _, leak := range []string{"Dorpsstraat", "1234AB", "Utrecht"} {
		if strings.Contains(string(out), leak) {
			t.Errorf("output leaks address %q", leak)
		}
	}
}

func TestBuildSlotsResultWindowFiltersAndFull(t *testing.T) {
	// The 22:30 slot on 25 Oct ends after 'to' (23:00 local) and does not match.
	from, to, _ := parseWindow("2026-10-24T14:00", "2026-10-25T23:00", amsterdam)
	res := buildSlotsResult(42, []byte(detailsFixture), fixtureDays(t), from, to, time.Now(), true)
	if len(res.Free) != 1 || res.Free[0].Start != "2026-10-24T17:00:00+02:00" {
		t.Errorf("free = %+v", res.Free)
	}
	if res.FullCount != 1 || len(res.Full) != 1 {
		t.Errorf("full = %+v", res.Full)
	}
	from, _, _ = parseWindow("2026-10-24T16:00", "2026-10-25", amsterdam)
	res = buildSlotsResult(42, []byte(detailsFixture), fixtureDays(t), from, to, time.Now(), true)
	if res.FullCount != 0 {
		t.Errorf("full slot starting before from must not match: %+v", res.Full)
	}

	from, to, _ = parseWindow("2026-10-24", "2026-10-24", amsterdam)
	res = buildSlotsResult(42, []byte(detailsFixture), fixtureDays(t), from, to, time.Now(), true)
	if res.FullCount != 1 || len(res.Full) != 1 || res.Full[0].Label != "Sat 24 Oct 15:00-17:00" {
		t.Errorf("full = %+v", res.Full)
	}
}

func TestBuildSlotsResultBeyondHorizon(t *testing.T) {
	from, to, _ := parseWindow("2026-10-24", "2026-11-30", amsterdam)
	res := buildSlotsResult(42, []byte(detailsFixture), fixtureDays(t), from, to, time.Now(), false)
	if !strings.Contains(res.Note, "cannot be checked") {
		t.Errorf("note = %q", res.Note)
	}
	from, to, _ = parseWindow("2026-12-01", "2026-12-02", amsterdam)
	res = buildSlotsResult(42, []byte(detailsFixture), fixtureDays(t), from, to, time.Now(), false)
	if len(res.Free) != 0 || res.EarliestFree != nil || res.Note == "" {
		t.Errorf("res = %+v", res)
	}
}

func TestBuildSlotsResultNoCurrentSlot(t *testing.T) {
	from, to, _ := parseWindow("2026-10-24", "2026-10-26", amsterdam)
	res := buildSlotsResult(42, []byte(`{}`), fixtureDays(t), from, to, time.Now(), false)
	if res.CurrentSlot != nil || res.Free[0].EarlierThanCurrent {
		t.Errorf("res = %+v", res)
	}
}

func TestFetchSlotDaysRetriesRedacted(t *testing.T) {
	var sleeps []time.Duration
	sleep := func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	redacted := errors.New("graphql error: Subgraph errors redacted")

	calls := 0
	days, err := fetchSlotDays(context.Background(), func(context.Context) ([]slotDay, error) {
		calls++
		if calls < 3 {
			return nil, redacted
		}
		return fixtureDays(t), nil
	}, sleep)
	if err != nil || len(days) != 3 || calls != 3 || len(sleeps) != 2 {
		t.Fatalf("days=%d err=%v calls=%d sleeps=%v", len(days), err, calls, sleeps)
	}

	calls = 0
	_, err = fetchSlotDays(context.Background(), func(context.Context) ([]slotDay, error) { calls++; return nil, redacted }, sleep)
	if err == nil || calls != 3 {
		t.Errorf("persistent redaction: err=%v calls=%d", err, calls)
	}

	calls = 0
	_, err = fetchSlotDays(context.Background(), func(context.Context) ([]slotDay, error) { calls++; return nil, errors.New("401 unauthorized") }, sleep)
	if err == nil || calls != 1 {
		t.Errorf("other errors must not retry: err=%v calls=%d", err, calls)
	}
}

func TestPickDeliveryFulfillment(t *testing.T) {
	mk := func(id int, typ, status, date string, done bool) appie.Fulfillment {
		f := appie.Fulfillment{OrderID: id, ShoppingType: typ, Status: status, TransactionCompleted: done}
		f.Delivery.Slot.Date = date
		return f
	}
	got, ok := pickDeliveryFulfillment([]appie.Fulfillment{
		mk(1, "PICKUP", "SUBMITTED", "2026-10-24", false),
		mk(2, "DELIVERY", "CANCELLED", "2026-10-25", false),
		mk(3, "DELIVERY", "SUBMITTED", "2026-10-30", false),
		mk(4, "DELIVERY", "REOPENED", "2026-10-26", false),
	})
	if !ok || got.OrderID != 4 {
		t.Errorf("got %+v ok=%v", got, ok)
	}
	if _, ok := pickDeliveryFulfillment(nil); ok {
		t.Error("empty should not pick")
	}
}
