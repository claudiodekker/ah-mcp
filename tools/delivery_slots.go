package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"

	appie "github.com/gwillem/appie-go"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const deliverySlotsQuery = `query OrderDeliverySlots($address: MemberAddressInput!) {
  orderDeliverySlots(address: $address) {
    date
    slots { startTime endTime isFullyBooked }
  }
}`

const (
	slotsMaxRetries = 2
	slotsDayLayout  = "2006-01-02"
	slotsTimeLayout = "2006-01-02T15:04"
)

var slotsBackoff = []time.Duration{500 * time.Millisecond, time.Second}

// amsterdam is the zone AH delivery windows are expressed in.
var amsterdam = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		panic(err)
	}
	return loc
}()

// --- wire types ---

type slotDay struct {
	Date  string `json:"date"`
	Slots []struct {
		StartTime     time.Time `json:"startTime"`
		EndTime       time.Time `json:"endTime"`
		IsFullyBooked bool      `json:"isFullyBooked"`
	} `json:"slots"`
}

type slotsData struct {
	Days []slotDay `json:"orderDeliverySlots"`
}

// orderDetailsAddress is the part of details-grouped-by-taxonomy this tool reads.
type orderDetailsAddress struct {
	Address struct {
		Street       string `json:"street"`
		HouseNumber  int    `json:"houseNumber"`
		ZipCode      int    `json:"zipCode"`
		ZipCodeExtra string `json:"zipCodeExtra"`
		City         string `json:"city"`
		CountryCode  string `json:"countryCode"`
	} `json:"address"`
	DeliveryTimePeriod struct {
		StartDateTime string `json:"startDateTime"`
		EndDateTime   string `json:"endDateTime"`
	} `json:"deliveryTimePeriod"`
	OrderState string `json:"orderState"`
}

// --- output types ---

type slotWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
	Label string `json:"label"`
}

type freeSlot struct {
	Date               string `json:"date"`
	Start              string `json:"start"`
	End                string `json:"end"`
	Label              string `json:"label"`
	EarlierThanCurrent bool   `json:"earlier_than_current"`
}

type fullSlot struct {
	Date  string `json:"date"`
	Start string `json:"start"`
	End   string `json:"end"`
	Label string `json:"label"`
}

type slotSearch struct {
	Free   []freeSlot
	Full   []fullSlot
	Latest time.Time // end of the last day AH returned
}

type slotsResult struct {
	OrderID      int         `json:"order_id"`
	CurrentSlot  *slotWindow `json:"current_slot"`
	Window       slotWindow  `json:"window"`
	Free         []freeSlot  `json:"free"`
	FullCount    int         `json:"full_count"`
	EarliestFree *freeSlot   `json:"earliest_free"`
	CheckedAt    string      `json:"checked_at"`
	Note         string      `json:"note,omitempty"`
	Full         []fullSlot  `json:"full,omitempty"`
}

// --- pure functions ---

// parseWindow reads from/to as YYYY-MM-DD (whole day) or YYYY-MM-DDTHH:MM in
// loc. A date-only "to" extends to the end of that day.
func parseWindow(from, to string, loc *time.Location) (time.Time, time.Time, error) {
	start, _, err := parseWindowEdge("from", from, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	end, dateOnly, err := parseWindowEdge("to", to, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if dateOnly {
		end = end.AddDate(0, 0, 1)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, fmt.Errorf("'to' must be after 'from'")
	}
	return start, end, nil
}

func parseWindowEdge(name, s string, loc *time.Location) (t time.Time, dateOnly bool, err error) {
	s = strings.TrimSpace(s)
	if t, err = time.ParseInLocation(slotsTimeLayout, s, loc); err == nil {
		return t, false, nil
	}
	if t, err = time.ParseInLocation(slotsDayLayout, s, loc); err == nil {
		return t, true, nil
	}
	return time.Time{}, false, fmt.Errorf("invalid %q %q: use YYYY-MM-DD or YYYY-MM-DDTHH:MM (Europe/Amsterdam)", name, s)
}

// buildAddressInput maps order details to a MemberAddressInput variable.
// Only the five verified fields are sent.
func buildAddressInput(body []byte) (map[string]any, error) {
	var d orderDetailsAddress
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("decode order details: %w", err)
	}
	a := d.Address
	if a.Street == "" || a.HouseNumber <= 0 || a.ZipCode <= 0 || a.City == "" || a.CountryCode == "" {
		return nil, fmt.Errorf("order details carry no complete delivery address")
	}
	return map[string]any{
		"postalCode":  fmt.Sprintf("%d%s", a.ZipCode, a.ZipCodeExtra),
		"street":      a.Street,
		"houseNumber": a.HouseNumber,
		"city":        a.City,
		"countryCode": a.CountryCode,
	}, nil
}

// currentSlotFromDetails reads the order's booked window (local time, no offset).
func currentSlotFromDetails(body []byte, loc *time.Location) (start, end time.Time, ok bool) {
	var d orderDetailsAddress
	if json.Unmarshal(body, &d) != nil {
		return
	}
	s, err1 := time.ParseInLocation("2006-01-02T15:04:05", d.DeliveryTimePeriod.StartDateTime, loc)
	e, err2 := time.ParseInLocation("2006-01-02T15:04:05", d.DeliveryTimePeriod.EndDateTime, loc)
	if err1 != nil || err2 != nil {
		return
	}
	return s, e, true
}

func slotLabel(start, end time.Time) string {
	return start.Format("Mon 2 Jan 15:04") + "-" + end.Format("15:04")
}

func toWindow(start, end time.Time) *slotWindow {
	return &slotWindow{Start: start.Format(time.RFC3339), End: end.Format(time.RFC3339), Label: slotLabel(start, end)}
}

// filterSlots converts slots to loc and keeps those inside [from, to].
// current is the zero time when unknown.
func filterSlots(days []slotDay, from, to, current time.Time, loc *time.Location) slotSearch {
	res := slotSearch{Free: []freeSlot{}, Full: []fullSlot{}}
	for _, day := range days {
		if dayStart, err := time.Parse(time.RFC3339, day.Date); err == nil {
			ld := dayStart.In(loc)
			if next := time.Date(ld.Year(), ld.Month(), ld.Day()+1, 0, 0, 0, 0, loc); next.After(res.Latest) {
				res.Latest = next
			}
		}
		for _, s := range day.Slots {
			start, end := s.StartTime.In(loc), s.EndTime.In(loc)
			if start.Before(from) || end.After(to) {
				continue
			}
			date := start.Format(slotsDayLayout)
			if s.IsFullyBooked {
				res.Full = append(res.Full, fullSlot{date, start.Format(time.RFC3339), end.Format(time.RFC3339), slotLabel(start, end)})
				continue
			}
			res.Free = append(res.Free, freeSlot{
				Date:               date,
				Start:              start.Format(time.RFC3339),
				End:                end.Format(time.RFC3339),
				Label:              slotLabel(start, end),
				EarlierThanCurrent: !current.IsZero() && start.Before(current),
			})
		}
	}
	sort.SliceStable(res.Free, func(i, j int) bool { return parseRFC(res.Free[i].Start).Before(parseRFC(res.Free[j].Start)) })
	sort.SliceStable(res.Full, func(i, j int) bool { return parseRFC(res.Full[i].Start).Before(parseRFC(res.Full[j].Start)) })
	return res
}

func parseRFC(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// windowNote explains a window that reaches past what AH returned.
func windowNote(to, latest time.Time, days int) string {
	if latest.IsZero() {
		return "AH returned no delivery days."
	}
	if to.After(latest) {
		return fmt.Sprintf("AH returns only about %d days of slots (until %s); the part of the window after that cannot be checked yet.", days, latest.Format("Mon 2 Jan"))
	}
	return ""
}

// pickDeliveryFulfillment returns the earliest open, non-cancelled delivery order.
// pickDeliveryFulfillment returns the earliest upcoming, non-cancelled delivery.
// TransactionCompleted is true for every confirmed order, so it says nothing here.
func pickDeliveryFulfillment(fs []appie.Fulfillment, today string) (appie.Fulfillment, bool) {
	var cands []appie.Fulfillment
	for _, f := range fs {
		if !strings.EqualFold(f.ShoppingType, "DELIVERY") || f.Delivery.Slot.Date < today {
			continue
		}
		if strings.Contains(strings.ToUpper(f.Status), "CANCEL") {
			continue
		}
		cands = append(cands, f)
	}
	if len(cands) == 0 {
		return appie.Fulfillment{}, false
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].Delivery.Slot.Date < cands[j].Delivery.Slot.Date })
	return cands[0], true
}

func isRedactedError(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "redacted")
}

// fetchSlotDays calls call, retrying AH's intermittent "Subgraph errors redacted".
func fetchSlotDays(ctx context.Context, call func(context.Context) ([]slotDay, error), sleep func(context.Context, time.Duration) error) ([]slotDay, error) {
	var lastErr error
	for attempt := 0; attempt <= slotsMaxRetries; attempt++ {
		days, err := call(ctx)
		if err == nil {
			return days, nil
		}
		lastErr = err
		if !isRedactedError(err) || attempt == slotsMaxRetries {
			break
		}
		if err := sleep(ctx, slotsBackoff[attempt]); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// buildSlotsResult assembles the tool output from already-fetched data.
func buildSlotsResult(orderID int, details []byte, days []slotDay, from, to, now time.Time, includeFull bool) slotsResult {
	cs, ce, haveCurrent := currentSlotFromDetails(details, from.Location())
	var current time.Time
	res := slotsResult{OrderID: orderID, Window: *toWindow(from, to), CheckedAt: now.In(from.Location()).Format(time.RFC3339)}
	if haveCurrent {
		current = cs
		res.CurrentSlot = toWindow(cs, ce)
	}
	found := filterSlots(days, from, to, current, from.Location())
	res.Free = found.Free
	res.FullCount = len(found.Full)
	if len(found.Free) > 0 {
		first := found.Free[0]
		res.EarliestFree = &first
	}
	if includeFull {
		res.Full = found.Full
	}
	res.Note = windowNote(to, found.Latest, len(days))
	return res
}

// --- tool ---

func registerFindDeliverySlots(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_find_delivery_slots",
		mcp.WithTitleAnnotation("Albert Heijn: Find Delivery Slots"),
		mcp.WithDescription(
			"Read-only: list free Albert Heijn delivery slots inside a time window for the delivery address of an order, "+
				"and compare them with the order's current slot. Nothing is booked or changed. "+
				"Returns free slots (flagged earlier_than_current), the count of fully booked slots, earliest_free and the current slot, all in Europe/Amsterdam time. "+
				"There is no API to move an order: the user moves the order to a slot in the AH app. "+
				"To keep checking for a slot that frees up, the agent schedules repeat calls (for example a /loop or a session cron) and reports only newly freed slots. "+
				"AH returns about 21 days of slots; a window beyond that is flagged in note.",
		),
		mcp.WithString("from",
			mcp.Required(),
			mcp.Description("Window start, Europe/Amsterdam: YYYY-MM-DD (start of that day) or YYYY-MM-DDTHH:MM"),
		),
		mcp.WithString("to",
			mcp.Required(),
			mcp.Description("Window end, Europe/Amsterdam: YYYY-MM-DD (end of that day) or YYYY-MM-DDTHH:MM. A slot matches when it starts at or after from and ends at or before to."),
		),
		mcp.WithString("order_id",
			mcp.Description("Order whose address and current slot are used. Default: the earliest open, non-cancelled upcoming delivery order from ah_get_order_history."),
		),
		mcp.WithBoolean("include_full",
			mcp.Description("Also list fully booked slots in the window (default false)"),
		),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if !deps.IsAuthenticated() {
			return notAuthResult(), nil
		}
		if err := refreshTokens(ctx, deps); err != nil {
			return errResult(fmt.Sprintf("Token refresh failed: %v", err)), nil
		}
		c, err := deps.GetClient()
		if err != nil {
			return errResult(fmt.Sprintf("Client error: %v", err)), nil
		}

		from, to, err := parseWindow(req.GetString("from", ""), req.GetString("to", ""), amsterdam)
		if err != nil {
			return errResult(err.Error()), nil
		}

		orderID := req.GetInt("order_id", 0)
		if orderID == 0 {
			fulfillments, err := c.GetFulfillments(ctx)
			if err != nil {
				return errResult(fmt.Sprintf("Failed to get order history: %v", err)), nil
			}
			f, ok := pickDeliveryFulfillment(fulfillments, time.Now().In(amsterdam).Format("2006-01-02"))
			if !ok {
				return errResult("No open delivery order found. Pass order_id explicitly."), nil
			}
			orderID = f.OrderID
		}

		var details json.RawMessage
		path := fmt.Sprintf("/mobile-services/order/v1/%d/details-grouped-by-taxonomy", orderID)
		if err := c.DoRequest(ctx, http.MethodGet, path, nil, &details); err != nil {
			return errResult(fmt.Sprintf("Failed to get order %d details: %v", orderID, err)), nil
		}
		address, err := buildAddressInput(details)
		if err != nil {
			return errResult(fmt.Sprintf("Order %d: %v", orderID, err)), nil
		}

		days, err := fetchSlotDays(ctx, func(ctx context.Context) ([]slotDay, error) {
			var data slotsData
			err := c.DoGraphQL(ctx, deliverySlotsQuery, map[string]any{"address": address}, &data)
			return data.Days, err
		}, sleepCtx)
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get delivery slots: %v", err)), nil
		}

		return jsonResult(buildSlotsResult(orderID, details, days, from, to, time.Now(), req.GetBool("include_full", false)))
	})
}
