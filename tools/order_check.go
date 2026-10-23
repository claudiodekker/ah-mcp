package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// checkoutResponse is the documented 200 shape of
// GET /mobile-services/order/v1/<orderId>/checkout. Element shapes are
// undocumented, so every array stays raw until summarized.
type checkoutResponse struct {
	KassaKoopjes        []json.RawMessage `json:"kassaKoopjes"`
	MissingBonus        []json.RawMessage `json:"missingBonus"`
	NonChosen           []json.RawMessage `json:"nonChosen"`
	NonDeliverables     []json.RawMessage `json:"nonDeliverables"`
	RecommendedProducts []json.RawMessage `json:"recommendedProducts"`
	Samples             []json.RawMessage `json:"samples"`
	ShowMakeCompleet    bool              `json:"showMakeCompleet"`
}

// checkoutItem holds the fields extracted from a list element when present.
// Raw carries the element minus image and presentation fields (see trimRaw).
type checkoutItem struct {
	ProductID int             `json:"product_id,omitempty"`
	Title     string          `json:"title,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Raw       json.RawMessage `json:"raw"`
}

type checkoutSummary struct {
	NonDeliverables     []checkoutItem    `json:"non_deliverables"`
	MissingBonus        []checkoutItem    `json:"missing_bonus"`
	NonChosen           []checkoutItem    `json:"non_chosen"`
	KassaKoopjesCount   int               `json:"kassa_koopjes_count"`
	RecommendedCount    int               `json:"recommended_products_count"`
	SamplesCount        int               `json:"samples_count"`
	ShowMakeCompleet    bool              `json:"show_make_compleet"`
	NonDeliverableCount int               `json:"non_deliverable_count"`
	Unallocated         []unallocatedItem `json:"unallocated"`
}

// unallocatedItem is an order line AH could not reserve for the delivery moment.
// The app shows these as not deliverable; the checkout nonDeliverables list
// stayed empty for them in testing (2026-10-23).
type unallocatedItem struct {
	ProductID int    `json:"product_id"`
	Title     string `json:"title"`
	Quantity  int    `json:"quantity"`
	Allocated int    `json:"allocated"`
}

type orderDetailsBody struct {
	Groups []struct {
		Products []struct {
			Quantity          int `json:"quantity"`
			AllocatedQuantity int `json:"allocatedQuantity"`
			Product           struct {
				WebshopID int    `json:"webshopId"`
				Title     string `json:"title"`
			} `json:"product"`
		} `json:"orderedProducts"`
	} `json:"groupedProductsInTaxonomy"`
}

// findUnallocated lists order lines where AH allocated less than was ordered.
func findUnallocated(body []byte) ([]unallocatedItem, error) {
	var d orderDetailsBody
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("decode order details: %w", err)
	}
	out := []unallocatedItem{}
	for _, g := range d.Groups {
		for _, p := range g.Products {
			if p.AllocatedQuantity < p.Quantity {
				out = append(out, unallocatedItem{p.Product.WebshopID, p.Product.Title, p.Quantity, p.AllocatedQuantity})
			}
		}
	}
	return out, nil
}

const notInOrderModeMessage = "The order is not reopened, so the checkout check is unavailable (AH answers \"Server not in order mode\"). " +
	"Call ah_reopen_order for the order first, then run ah_check_order again."

// isNotInOrderMode reports whether err is AH's 412 refusal for an order that
// is not reopened.
func isNotInOrderMode(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	// With no reopened order, the active-order lookup fails before the checkout call does.
	return strings.Contains(msg, "not in order mode") || strings.Contains(msg, "order does not exist")
}

// summarizeCheckout turns the checkout response body into the compact result.
func summarizeCheckout(body []byte) (*checkoutSummary, error) {
	var resp checkoutResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode checkout response: %w", err)
	}
	return &checkoutSummary{
		NonDeliverables:     extractCheckoutItems(resp.NonDeliverables),
		MissingBonus:        extractCheckoutItems(resp.MissingBonus),
		NonChosen:           extractCheckoutItems(resp.NonChosen),
		KassaKoopjesCount:   len(resp.KassaKoopjes),
		RecommendedCount:    len(resp.RecommendedProducts),
		SamplesCount:        len(resp.Samples),
		ShowMakeCompleet:    resp.ShowMakeCompleet,
		NonDeliverableCount: len(resp.NonDeliverables),
	}, nil
}

func extractCheckoutItems(raw []json.RawMessage) []checkoutItem {
	items := make([]checkoutItem, 0, len(raw))
	for _, r := range raw {
		item := checkoutItem{Raw: r}
		var m map[string]any
		if json.Unmarshal(r, &m) == nil {
			item.ProductID, item.Title, item.Reason = findCheckoutFields(m)
			if trimmed, err := json.Marshal(trimRaw(m)); err == nil {
				item.Raw = trimmed
			}
		}
		items = append(items, item)
	}
	return items
}

// noisyCheckoutKeys are dropped from raw elements: each product carries five
// image renditions and display-only fields that bloat the tool output.
var noisyCheckoutKeys = map[string]bool{"images": true, "extraDescriptions": true, "propertyIcons": true}

func trimRaw(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if !noisyCheckoutKeys[k] {
				out[k] = trimRaw(val)
			}
		}
		return out
	case []any:
		for i := range t {
			t[i] = trimRaw(t[i])
		}
		return t
	}
	return v
}

// findCheckoutFields looks for a product id, title and reason at the top level
// of an element and inside a nested "product" object.
func findCheckoutFields(m map[string]any) (id int, title, reason string) {
	for _, level := range []map[string]any{m, asMap(m["product"])} {
		if level == nil {
			continue
		}
		if id == 0 {
			id = firstInt(level, "webshopId", "productId", "product_id", "id")
		}
		if title == "" {
			title = firstString(level, "title", "name", "productTitle")
		}
		if reason == "" {
			reason = firstString(level, "reason", "reasonDescription", "message", "description", "unavailableReason")
		}
	}
	return id, title, reason
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func firstInt(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if n := toInt(m[k]); n > 0 {
			return n
		}
	}
	return 0
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// --- ah_check_order ---

func registerCheckOrder(s *server.MCPServer, deps Deps) {
	tool := mcp.NewTool("ah_check_order",
		mcp.WithTitleAnnotation("Albert Heijn: Check Reopened Order"),
		mcp.WithDescription(
			"Read-only checkout check for the active (reopened) AH order. "+
				"Lists order lines AH could not allocate for the delivery moment (unallocated: the items the app shows as not deliverable), "+
				"items the checkout reports as non-deliverable (non_deliverables), bonus offers the order misses (missing_bonus) and items not yet chosen (non_chosen), "+
				"plus counts of the other checkout lists. Each list entry has product_id, title and reason when AH provides them, and always the raw element. "+
				"Product is_available does NOT reflect deliverability, so use this tool after ah_update_order_items to catch undeliverable items before the user confirms in the Albert Heijn app. "+
				"Only works while an order is reopened (ah_reopen_order); otherwise it says to reopen first. Does not change the order.",
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

		order, err := c.GetOrder(ctx)
		if isNotInOrderMode(err) {
			return errResult(notInOrderModeMessage), nil
		}
		if err != nil {
			return errResult(fmt.Sprintf("Failed to get active order: %v", err)), nil
		}

		var body json.RawMessage
		path := fmt.Sprintf("/mobile-services/order/v1/%s/checkout", order.ID)
		if err := c.DoRequest(ctx, http.MethodGet, path, nil, &body); err != nil {
			if isNotInOrderMode(err) {
				return errResult(notInOrderModeMessage), nil
			}
			return errResult(fmt.Sprintf("Failed to check order %s: %v", order.ID, err)), nil
		}

		summary, err := summarizeCheckout(body)
		if err != nil {
			return errResult(err.Error()), nil
		}

		var details json.RawMessage
		detailsPath := fmt.Sprintf("/mobile-services/order/v1/%s/details-grouped-by-taxonomy", order.ID)
		if err := c.DoRequest(ctx, http.MethodGet, detailsPath, nil, &details); err != nil {
			return errResult(fmt.Sprintf("Failed to get order %s details: %v", order.ID, err)), nil
		}
		if summary.Unallocated, err = findUnallocated(details); err != nil {
			return errResult(err.Error()), nil
		}
		return jsonResult(summary)
	})
}
