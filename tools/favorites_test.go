package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	appie "github.com/gwillem/appie-go"
)

func TestFetchFavoriteListsQueriesAllListsAndMapsFields(t *testing.T) {
	var body struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		io.WriteString(w, `{"data":{"favoriteListV2":[
			{"id":"abc","description":"Weekend","totalSize":3,"imageUrl":"x"},
			{"id":"def","description":"Feest","totalSize":0}]}}`)
	}))
	defer srv.Close()
	c := appie.New(appie.WithBaseURL(srv.URL), appie.WithTokens("a", "r"))

	got, err := fetchFavoriteLists(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	want := []favoriteListEntry{{"abc", "Weekend", 3}, {"def", "Feest", 0}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !strings.Contains(body.Query, "favoriteListV2") {
		t.Errorf("query = %q", body.Query)
	}
	for _, k := range []string{"ids", "productIds"} {
		if v, ok := body.Variables[k].([]any); !ok || len(v) != 0 {
			t.Errorf("variable %s = %v, want empty list", k, body.Variables[k])
		}
	}
}

func TestParseFavoriteListsEmptyMarshalsAsArray(t *testing.T) {
	got, err := parseFavoriteLists([]byte(`{"favoriteListV2":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if string(b) != "[]" {
		t.Errorf("marshalled = %s, want []", b)
	}
}

func TestFetchFavoriteListsSurfacesGraphQLErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"errors":[{"message":"boom"}]}`)
	}))
	defer srv.Close()
	c := appie.New(appie.WithBaseURL(srv.URL), appie.WithTokens("a", "r"))
	if _, err := fetchFavoriteLists(context.Background(), c); err == nil {
		t.Fatal("want error")
	}
}
