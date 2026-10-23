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
// Raw always carries the unmodified element.
type checkoutItem struct {
	ProductID int             `json:"product_id,omitempty"`
	Title     string          `json:"title,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Raw       json.RawMessage `json:"raw"`
}

type checkoutSummary struct {
	NonDeliverables     []checkoutItem `json:"non_deliverables"`
	MissingBonus        []checkoutItem `json:"missing_bonus"`
	NonChosen           []checkoutItem `json:"non_chosen"`
	KassaKoopjesCount   int            `json:"kassa_koopjes_count"`
	RecommendedCount    int            `json:"recommended_products_count"`
	SamplesCount        int            `json:"samples_count"`
	ShowMakeCompleet    bool           `json:"show_make_compleet"`
	NonDeliverableCount int            `json:"non_deliverable_count"`
}

const notInOrderModeMessage = "The order is not reopened, so the checkout check is unavailable (AH answers \"Server not in order mode\"). " +
	"Call ah_reopen_order for the order first, then run ah_check_order again."

// isNotInOrderMode reports whether err is AH's 412 refusal for an order that
// is not reopened.
func isNotInOrderMode(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "not in order mode")
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
		}
		items = append(items, item)
	}
	return items
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
				"Lists items that cannot be delivered (non_deliverables), bonus offers the order misses (missing_bonus) and items not yet chosen (non_chosen), "+
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
		return jsonResult(summary)
	})
}
