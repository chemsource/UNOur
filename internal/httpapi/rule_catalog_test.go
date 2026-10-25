package httpapi_test

import (
	"strings"
	"testing"

	"unour/game"
	"unour/internal/room"
)

func TestPublicRuleCatalogAndStaleCreation(t *testing.T) {
	c := setup(t)
	response := c.request("GET", "/api/v1/rules", "", "")
	catalog := decode[game.RuleCatalog](t, response, 200)
	if len(catalog.Modules) < 6 || catalog.Revision == "" {
		t.Fatal("incomplete catalog", catalog)
	}
	if strings.Contains(response.Body.String(), `"steps"`) {
		t.Fatal("internal programs leaked into metadata")
	}
	stale := c.request("POST", "/api/v1/rooms", c.alice, `{"catalog_revision":"outdated"}`)
	if stale.Code != 409 || !strings.Contains(stale.Body.String(), "rule_catalog_changed") {
		t.Fatal("stale catalog was accepted", stale.Body.String())
	}
	created := decode[room.View](t, c.request("POST", "/api/v1/rooms", c.alice, `{"catalog_revision":"`+catalog.Revision+`"}`), 201)
	if len(created.RuleDescriptions) == 0 || created.Rules[0].Version != 1 {
		t.Fatal("canonical room metadata missing")
	}
	a, b := c.start()
	v := decode[room.View](t, c.request("GET", "/api/v1/rooms/table", a.Token, ""), 200)
	if len(v.State.LegalActions) == 0 {
		t.Fatal("actor has no legal choices")
	}
	other := decode[room.View](t, c.request("GET", "/api/v1/rooms/table", b.Token, ""), 200)
	if len(other.State.LegalActions) != 0 || len(other.State.Players[0].Hand) != 0 {
		t.Fatal("opponent action/hand leaked")
	}
}
