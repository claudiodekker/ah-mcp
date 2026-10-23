package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	appie "github.com/gwillem/appie-go"
)

// appie-go can only fetch the bonus of today's date. The section endpoint takes
// a date, so a later week is fetched by passing a date inside that week. The
// metadata, section and group shapes are unexported in appie-go, so the parts
// used here are mirrored and read through Client.DoRequest and DoGraphQL.
// Based on mrserzhan/ah-mcp#3.

const dateLayout = "2006-01-02"

type bonusPeriod struct {
	BonusStartDate string `json:"bonusStartDate"`
	BonusEndDate   string `json:"bonusEndDate"`
	Tabs           []struct {
		URLMetadataList []struct {
			BonusType   string `json:"bonusType"`
			Description string `json:"description"`
		} `json:"urlMetadataList"`
	} `json:"tabs"`
}

type bonusMetadataResponse struct {
	Periods []bonusPeriod `json:"periods"`
}

// weekSelector says which bonus week the caller wants: "current", "next" or a
// YYYY-MM-DD date inside the wanted week.
type weekSelector string

func parseWeekSelector(v string) (weekSelector, error) {
	switch v {
	case "", "current":
		return "current", nil
	case "next":
		return "next", nil
	}
	if _, err := time.Parse(dateLayout, v); err != nil {
		return "", fmt.Errorf("week must be current, next or a date YYYY-MM-DD, got %q", v)
	}
	return weekSelector(v), nil
}

// selectPeriod picks the metadata period for sel. Periods are matched by date,
// not by position, because AH reorders them while an order is reopened. note
// is set when the first listed period is not the one containing today. found is
// false when the wanted week is not published (yet).
func selectPeriod(periods []bonusPeriod, today time.Time, sel weekSelector) (idx int, note string, found bool) {
	contains := func(p bonusPeriod, d string) bool {
		return p.BonusStartDate != "" && p.BonusStartDate <= d && d <= p.BonusEndDate
	}
	todayStr := today.Format(dateLayout)

	cur := -1
	for i, p := range periods {
		if contains(p, todayStr) {
			cur = i
			break
		}
	}
	if cur > 0 {
		note = "AH lists another period before the one containing today, which happens while an order is reopened; the offers cover the dates in period."
	}

	switch sel {
	case "current":
		if cur >= 0 {
			return cur, note, true
		}
		if len(periods) == 0 {
			return 0, "", false
		}
		return 0, "No listed period contains today; showing the first listed period.", true
	case "next":
		if cur < 0 {
			return 0, "", false
		}
		best := -1
		for i, p := range periods {
			if p.BonusStartDate > periods[cur].BonusEndDate && (best < 0 || p.BonusStartDate < periods[best].BonusStartDate) {
				best = i
			}
		}
		return best, note, best >= 0
	default:
		for i, p := range periods {
			if contains(p, string(sel)) {
				return i, note, true
			}
		}
		return 0, "", false
	}
}

func fetchBonusPeriods(ctx context.Context, c *appie.Client) ([]bonusPeriod, error) {
	var meta bonusMetadataResponse
	if err := c.DoRequest(ctx, http.MethodGet, "/mobile-services/bonuspage/v3/metadata", nil, &meta); err != nil {
		return nil, fmt.Errorf("get bonus metadata failed: %w", err)
	}
	return meta.Periods, nil
}

// getBonusProductsForPeriod mirrors appie-go's GetBonusProducts for one period:
// one section request per NATIONAL category, dated at the period start.
func getBonusProductsForPeriod(ctx context.Context, c *appie.Client, application string, p bonusPeriod) ([]appie.Product, error) {
	if p.BonusStartDate == "" {
		return nil, fmt.Errorf("bonus period has no start date")
	}
	var categories []string
	seenCat := map[string]bool{}
	for _, tab := range p.Tabs {
		for _, m := range tab.URLMetadataList {
			if m.BonusType == "NATIONAL" && m.Description != "" && !seenCat[m.Description] {
				seenCat[m.Description] = true
				categories = append(categories, m.Description)
			}
		}
	}

	seen := map[string]bool{}
	var products []appie.Product
	for _, category := range categories {
		catProducts, err := getBonusSectionForDate(ctx, c, application, category, p.BonusStartDate)
		if err != nil {
			return nil, err
		}
		for _, prod := range catProducts {
			key := fmt.Sprintf("%d:%s", prod.ID, prod.Title)
			if !seen[key] {
				seen[key] = true
				products = append(products, prod)
			}
		}
	}
	return products, nil
}

type bonusSectionProduct struct {
	WebshopID        int           `json:"webshopId"`
	Title            string        `json:"title"`
	Brand            string        `json:"brand"`
	SalesUnitSize    string        `json:"salesUnitSize"`
	Images           []appie.Image `json:"images"`
	CurrentPrice     float64       `json:"currentPrice"`
	PriceBeforeBonus float64       `json:"priceBeforeBonus"`
	IsBonus          bool          `json:"isBonus"`
	BonusMechanism   string        `json:"bonusMechanism"`
	MainCategory     string        `json:"mainCategory"`
	SubCategory      string        `json:"subCategory"`
}

func (p *bonusSectionProduct) toProduct() appie.Product {
	price := p.CurrentPrice
	if price == 0 {
		price = p.PriceBeforeBonus
	}
	return appie.Product{
		ID:             p.WebshopID,
		WebshopID:      strconv.Itoa(p.WebshopID),
		Title:          p.Title,
		Brand:          p.Brand,
		Category:       p.MainCategory,
		SubCategory:    p.SubCategory,
		Price:          appie.Price{Now: price, Was: p.PriceBeforeBonus},
		Images:         p.Images,
		IsBonus:        p.IsBonus,
		BonusMechanism: p.BonusMechanism,
		UnitSize:       p.SalesUnitSize,
	}
}

type bonusSectionGroup struct {
	ID                  string                `json:"id"`
	SegmentDescription  string                `json:"segmentDescription"`
	DiscountDescription string                `json:"discountDescription"`
	Category            string                `json:"category"`
	Images              []appie.Image         `json:"images"`
	Products            []bonusSectionProduct `json:"products"`
	ExampleFromPrice    float64               `json:"exampleFromPrice"`
	ExampleForPrice     float64               `json:"exampleForPrice"`
}

func (g *bonusSectionGroup) toProduct() appie.Product {
	return appie.Product{
		Title:          g.SegmentDescription,
		Category:       g.Category,
		BonusMechanism: g.DiscountDescription,
		IsBonus:        true,
		BonusSegmentID: g.ID,
		Price:          appie.Price{Now: g.ExampleForPrice, Was: g.ExampleFromPrice},
		Images:         g.Images,
	}
}

type bonusSectionResponse struct {
	BonusGroupOrProducts []struct {
		Product    *bonusSectionProduct `json:"product,omitempty"`
		BonusGroup *bonusSectionGroup   `json:"bonusGroup,omitempty"`
	} `json:"bonusGroupOrProducts"`
}

func getBonusSectionForDate(ctx context.Context, c *appie.Client, application, category, date string) ([]appie.Product, error) {
	params := url.Values{}
	params.Set("application", application)
	params.Set("date", date)
	params.Set("promotionType", "NATIONAL")
	params.Set("category", category)

	var result bonusSectionResponse
	if err := c.DoRequest(ctx, http.MethodGet, "/mobile-services/bonuspage/v2/section?"+params.Encode(), nil, &result); err != nil {
		return nil, fmt.Errorf("get bonus products failed (category=%s): %w", category, err)
	}

	var products []appie.Product
	for _, item := range result.BonusGroupOrProducts {
		if item.Product != nil {
			products = append(products, item.Product.toProduct())
		}
		if item.BonusGroup != nil {
			if len(item.BonusGroup.Products) > 0 {
				for i := range item.BonusGroup.Products {
					products = append(products, item.BonusGroup.Products[i].toProduct())
				}
			} else {
				products = append(products, item.BonusGroup.toProduct())
			}
		}
	}
	return products, nil
}

// Copy of appie-go's fetchBonusGroupProductsQuery, which is unexported and
// pinned to the current period.
const bonusGroupProductsQuery = `query FetchBonusPromotionWithProducts(
  $id: String,
  $periodStart: String,
  $periodEnd: String,
  $filterUnavailableProducts: Boolean,
  $forcePromotionVisibility: Boolean = true,
  $showAllPromotionSegments: Boolean = true
) {
  bonusPromotions(
    input: {
      id: $id
      periodStart: $periodStart
      periodEnd: $periodEnd
      filterUnavailableProducts: $filterUnavailableProducts
      forcePromotionVisibility: $forcePromotionVisibility
      showAllPromotionSegments: $showAllPromotionSegments
    }
  ) {
    id
    title
    productCount
    products {
      id
      title
      brand
      category
      salesUnitSize
      availability { isOrderable }
      priceV2(
        periodStart: $periodStart
        periodEnd: $periodEnd
        filterUnavailableProducts: $filterUnavailableProducts
        forcePromotionVisibility: true
      ) {
        now { amount }
        was { amount }
        promotionLabel {
          tiers {
            mechanism
            description
          }
        }
      }
      imagePack {
        large { url width height }
      }
    }
  }
}`

type bonusGroupProductsResponse struct {
	BonusPromotions []struct {
		Products []struct {
			ID            int    `json:"id"`
			Title         string `json:"title"`
			Brand         string `json:"brand"`
			Category      string `json:"category"`
			SalesUnitSize string `json:"salesUnitSize"`
			PriceV2       struct {
				Now            struct{ Amount float64 } `json:"now"`
				Was            struct{ Amount float64 } `json:"was"`
				PromotionLabel *struct {
					Tiers []struct {
						Description string `json:"description"`
					} `json:"tiers"`
				} `json:"promotionLabel"`
			} `json:"priceV2"`
			ImagePack []struct {
				Large *appie.Image `json:"large"`
			} `json:"imagePack"`
		} `json:"products"`
	} `json:"bonusPromotions"`
}

// getBonusGroupProductsForPeriod is GetBonusGroupProducts for an explicit period.
func getBonusGroupProductsForPeriod(ctx context.Context, c *appie.Client, segmentID, start, end string) ([]appie.Product, error) {
	vars := map[string]any{
		"id":                        segmentID,
		"periodStart":               start,
		"periodEnd":                 end,
		"filterUnavailableProducts": true,
		"forcePromotionVisibility":  true,
		"showAllPromotionSegments":  true,
	}
	var resp bonusGroupProductsResponse
	if err := c.DoGraphQL(ctx, bonusGroupProductsQuery, vars, &resp); err != nil {
		return nil, fmt.Errorf("get bonus group products failed: %w", err)
	}
	if len(resp.BonusPromotions) == 0 {
		return nil, nil
	}
	out := make([]appie.Product, 0, len(resp.BonusPromotions[0].Products))
	for _, p := range resp.BonusPromotions[0].Products {
		prod := appie.Product{
			ID:        p.ID,
			WebshopID: strconv.Itoa(p.ID),
			Title:     p.Title,
			Brand:     p.Brand,
			Category:  p.Category,
			Price:     appie.Price{Now: p.PriceV2.Now.Amount, Was: p.PriceV2.Was.Amount},
			IsBonus:   true,
			UnitSize:  p.SalesUnitSize,
		}
		if l := p.PriceV2.PromotionLabel; l != nil && len(l.Tiers) > 0 {
			prod.BonusMechanism = l.Tiers[0].Description
		}
		for _, pack := range p.ImagePack {
			if pack.Large != nil {
				prod.Images = append(prod.Images, *pack.Large)
			}
		}
		out = append(out, prod)
	}
	return out, nil
}
