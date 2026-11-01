package fakewallapop

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestUserItemsNegativeCursorReturnsFirstPage(t *testing.T) {
	s := New()
	defer s.Close()
	s.UserItemsPageSize = 1
	s.AddItem(Item{Hash: "hashactive00", Title: "Active", Seller: UserHash})
	req, err := http.NewRequest(http.MethodGet, s.URL+"/api/v3/user/items?since=-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer access-token")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || len(body.Data) != 1 || body.Data[0].ID != "hashactive00" {
		t.Fatalf("expected first page, got status %d body %+v", res.StatusCode, body)
	}
}
