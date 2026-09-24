package mappy

import (
	"reflect"
	"testing"
)

func TestProperNounTokens(t *testing.T) {
	cases := []struct {
		query string
		want  []string
	}{
		{"Biarritz Auto", []string{"Biarritz", "Auto"}},
		{"garages à Oloron-Sainte-Marie", []string{"Oloron-Sainte-Marie"}},
		{"trouve et localise des garages auto à Oloron-Sainte-Marie", []string{"Oloron-Sainte-Marie"}},
		{"garage de la gare", nil},
	}
	for _, c := range cases {
		got := properNounTokens(c.query)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("properNounTokens(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestFilterByRelevance(t *testing.T) {
	places := []POI{
		{Name: "Biarritz Auto Rétro"},
		{Name: "Auto Ecole Alpha"},
		{Name: "Garage Piquemal"},
	}

	t.Run("business name query narrows to matching POIs", func(t *testing.T) {
		got := filterByRelevance(places, "Biarritz Auto")
		want := []POI{{Name: "Biarritz Auto Rétro"}, {Name: "Auto Ecole Alpha"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %v, want %v", got, want)
		}
	})

	t.Run("generic category query keeps every POI", func(t *testing.T) {
		got := filterByRelevance(places, "garages à Oloron-Sainte-Marie")
		if !reflect.DeepEqual(got, places) {
			t.Errorf("got %v, want unfiltered %v", got, places)
		}
	})

	t.Run("no POI matches the token falls back to unfiltered", func(t *testing.T) {
		got := filterByRelevance(places, "Karcher Oloron-Sainte-Marie")
		if !reflect.DeepEqual(got, places) {
			t.Errorf("got %v, want unfiltered %v", got, places)
		}
	})
}
