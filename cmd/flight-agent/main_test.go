package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMakePassengers(t *testing.T) {
	passengers := makePassengers(3)
	if len(passengers) != 3 {
		t.Fatalf("got %d passengers, want 3", len(passengers))
	}
	for index, passenger := range passengers {
		if passenger.Type != "adult" {
			t.Errorf("passenger %d type = %q, want adult", index, passenger.Type)
		}
	}
}

func TestMapDuffelOffer(t *testing.T) {
	offer := duffelOffer{
		ID:            "off_test",
		TotalAmount:   "125.50",
		TotalCurrency: "INR",
		Slices: []duffelSlice{{
			Duration: "PT5H30M",
			Segments: []duffelSegment{
				{
					DepartingAt: "2026-12-19T08:00:00Z",
					ArrivingAt:  "2026-12-19T10:00:00Z",
					OperatingCarrier: struct {
						Name string `json:"name"`
					}{Name: "Air India"},
					MarketingCarrierFlightNumber: "AI101",
					DepartingAirport: struct {
						IATACode string `json:"iata_code"`
					}{IATACode: "DEL"},
					ArrivingAirport: struct {
						IATACode string `json:"iata_code"`
					}{IATACode: "MAA"},
				},
				{
					DepartingAt: "2026-12-19T11:00:00Z",
					ArrivingAt:  "2026-12-19T13:30:00Z",
					OperatingCarrier: struct {
						Name string `json:"name"`
					}{Name: "Air India"},
					MarketingCarrierFlightNumber: "AI202",
					ArrivingAirport: struct {
						IATACode string `json:"iata_code"`
					}{IATACode: "IXZ"},
				},
			},
		}},
	}

	mapped, err := mapDuffelOffer(offer)
	if err != nil {
		t.Fatalf("mapDuffelOffer returned error: %v", err)
	}
	if mapped.ID != "off_test" || mapped.Airline != "Air India" {
		t.Errorf("mapped identity = %+v", mapped)
	}
	if mapped.Origin != "DEL" || mapped.Dest != "IXZ" {
		t.Errorf("mapped route = %s -> %s, want DEL -> IXZ", mapped.Origin, mapped.Dest)
	}
	if mapped.FlightNum != "AI101" || mapped.Stops != 1 {
		t.Errorf("mapped flight = %s with %d stops, want AI101 with 1 stop", mapped.FlightNum, mapped.Stops)
	}
	if mapped.PriceINR != 125.50 {
		t.Errorf("mapped price = %.2f, want 125.50", mapped.PriceINR)
	}
}

func TestMapDuffelOfferUsesOriginAndDestinationFields(t *testing.T) {
	offer := duffelOffer{
		ID: "offer_places", TotalAmount: "100", TotalCurrency: "INR",
		Slices: []duffelSlice{{Segments: []duffelSegment{{
			Origin: struct {
				IATACode string `json:"iata_code"`
			}{IATACode: "DEL"},
			Destination: struct {
				IATACode string `json:"iata_code"`
			}{IATACode: "IXZ"},
		}}}},
	}
	mapped, err := mapDuffelOffer(offer)
	if err != nil {
		t.Fatalf("mapDuffelOffer returned error: %v", err)
	}
	if mapped.Origin != "DEL" || mapped.Dest != "IXZ" {
		t.Fatalf("route = %s -> %s, want DEL -> IXZ", mapped.Origin, mapped.Dest)
	}
}

func TestMapDuffelOfferIncludesConnectingAirportFromSegmentPlaces(t *testing.T) {
	offer := duffelOffer{ID: "offer_connection", TotalAmount: "100", TotalCurrency: "INR", Slices: []duffelSlice{{Segments: []duffelSegment{
		{Origin: struct {
			IATACode string `json:"iata_code"`
		}{IATACode: "DEL"}, Destination: struct {
			IATACode string `json:"iata_code"`
		}{IATACode: "CCU"}},
		{Origin: struct {
			IATACode string `json:"iata_code"`
		}{IATACode: "CCU"}, Destination: struct {
			IATACode string `json:"iata_code"`
		}{IATACode: "IXZ"}},
	}}}}
	mapped, err := mapDuffelOffer(offer)
	if err != nil {
		t.Fatalf("mapDuffelOffer returned error: %v", err)
	}
	if mapped.ConnectionAirport != "CCU" || len(mapped.ConnectionAirports) != 1 || mapped.ConnectionAirports[0] != "CCU" {
		t.Fatalf("connections = %q / %v, want CCU", mapped.ConnectionAirport, mapped.ConnectionAirports)
	}
}

func TestLayoverHoursBetweenParsesDuffelTimes(t *testing.T) {
	hours := layoverHoursBetween("2026-12-19T08:00:00+00:00", "2026-12-19T12:30:00+00:00")
	if hours != 4.5 {
		t.Fatalf("layover hours = %.2f, want 4.5", hours)
	}
}

func TestMapDuffelOfferRejectsInvalidOffer(t *testing.T) {
	_, err := mapDuffelOffer(duffelOffer{ID: "off_empty", TotalAmount: "10", TotalCurrency: "INR"})
	if err == nil || !strings.Contains(err.Error(), "no segments") {
		t.Fatalf("error = %v, want no segments error", err)
	}
}

func TestConvertToINR(t *testing.T) {
	t.Run("INR is unchanged", func(t *testing.T) {
		amount, err := convertToINR(125.5, "inr")
		if err != nil || amount != 125.5 {
			t.Fatalf("got %.2f, %v; want 125.50, nil", amount, err)
		}
	})

	t.Run("foreign currency uses configured rate", func(t *testing.T) {
		t.Setenv("DUFFEL_TO_INR_RATE", "86.5")
		amount, err := convertToINR(100, "USD")
		if err != nil || amount != 8650 {
			t.Fatalf("got %.2f, %v; want 8650, nil", amount, err)
		}
	})

	t.Run("missing rate returns error", func(t *testing.T) {
		t.Setenv("DUFFEL_TO_INR_RATE", "")
		if _, err := convertToINR(100, "USD"); err == nil {
			t.Fatal("expected missing-rate error")
		}
	})
}

func TestCompactResponse(t *testing.T) {
	if got := compactResponse([]byte("  error  ")); got != "error" {
		t.Fatalf("got %q, want error", got)
	}

	got := compactResponse([]byte(strings.Repeat("x", 1100)))
	if len(got) != 1003 || !strings.HasSuffix(got, "...") {
		t.Fatalf("long response length/suffix = %d/%q", len(got), got[len(got)-3:])
	}
}

func TestBuildFlightQueryIncludesArbitraryPreference(t *testing.T) {
	query := BuildFlightQuery(FlightSearchParams{
		Origin:        " Delhi ",
		Destination:   "Port Blair",
		DepartureDate: "2026-12-19",
		Passengers:    1,
		CabinClass:    "ECONOMY",
		Preference:    "I need extra legroom and would like a vegetarian meal.",
	})

	want := "Find a economy flight for 1 passenger(s) from DELHI to PORT BLAIR on 2026-12-19. User preference: I need extra legroom and would like a vegetarian meal."
	if query != want {
		t.Fatalf("query = %q, want %q", query, want)
	}
}

func TestBuildFlightQueryAllowsEmptyPreference(t *testing.T) {
	query := BuildFlightQuery(FlightSearchParams{
		Origin:        "DEL",
		Destination:   "IXZ",
		DepartureDate: "2026-12-19",
		Passengers:    2,
		CabinClass:    "BUSINESS",
	})
	if strings.Contains(query, "User preference:") {
		t.Fatalf("query unexpectedly contains an empty preference: %q", query)
	}
}

func TestResolveAirportCode(t *testing.T) {
	got, err := resolveAirportCode(context.Background(), "", " ixz ")
	if err != nil {
		t.Fatalf("resolveAirportCode returned error: %v", err)
	}
	if got != "IXZ" {
		t.Fatalf("resolveAirportCode = %q, want IXZ", got)
	}
}

func TestResolveAirportCodeRejectsUnknownLocation(t *testing.T) {
	if _, err := resolveAirportCode(context.Background(), "", ""); err == nil {
		t.Fatal("expected empty location error")
	}
}

func TestParseLayoverPreference(t *testing.T) {
	layover, ok := parseLayoverPreference("I need a layover of 4 hrs in Kolkata")
	if !ok {
		t.Fatal("expected layover preference to be parsed")
	}
	if layover.airport != "CCU" || layover.hours != 4 {
		t.Fatalf("parsed layover = %+v, want CCU and 4 hours", layover)
	}
}

func TestParseLayoverPreferenceAllowsUnspecifiedDuration(t *testing.T) {
	layover, ok := parseLayoverPreference("Need a layover in Kolkata")
	if !ok || layover.airport != "CCU" || layover.hours != 0 {
		t.Fatalf("parsed layover = %+v, ok=%v; want CCU with unspecified duration", layover, ok)
	}
}

func TestBuildLayoverExplanation(t *testing.T) {
	layover := layoverPreference{airport: "CCU", hours: 4}
	explanation := buildLayoverExplanation([]duffelOffer{
		{Slices: []duffelSlice{{Segments: []duffelSegment{
			{ArrivingAt: "2026-12-19T15:10:00Z", ArrivingAirport: struct {
				IATACode string `json:"iata_code"`
			}{IATACode: "CCU"}},
			{DepartingAt: "2026-12-19T13:56:00Z", DepartingAirport: struct {
				IATACode string `json:"iata_code"`
			}{IATACode: "CCU"}},
		}}}}}, layover, "DEL", "IXZ")

	if !strings.Contains(explanation.Title, "4-hour layover") || !strings.Contains(explanation.Reason, "DEL to IXZ") {
		t.Fatalf("unexpected explanation: %+v", explanation)
	}
}

func TestBuildLayoverExplanationWithoutDuration(t *testing.T) {
	explanation := buildLayoverExplanation(nil, layoverPreference{airport: "CCU"}, "DEL", "IXZ")
	if explanation.Title != "No flight combinations include a connection in CCU" {
		t.Fatalf("title = %q", explanation.Title)
	}
	if strings.Contains(explanation.Title, "0-hour") {
		t.Fatalf("title incorrectly contains zero-hour duration: %q", explanation.Title)
	}
}

func TestFilterLayoverOffersAllowsAnyDurationWhenUnspecified(t *testing.T) {
	offer := duffelOffer{ID: "via-ccu", Slices: []duffelSlice{{Segments: []duffelSegment{
		{ArrivingAirport: struct {
			IATACode string `json:"iata_code"`
		}{IATACode: "CCU"}, ArrivingAt: "2026-10-25T08:00:00Z"},
		{DepartingAirport: struct {
			IATACode string `json:"iata_code"`
		}{IATACode: "CCU"}, DepartingAt: "2026-10-25T15:00:00Z"},
	}}}}

	filtered := filterLayoverOffers([]duffelOffer{offer}, layoverPreference{airport: "CCU"})
	if len(filtered) != 1 || filtered[0].ID != "via-ccu" {
		t.Fatalf("filtered offers = %+v, want the CCU connection regardless of duration", filtered)
	}
}

func TestSortOffersByPreferredAirlines(t *testing.T) {
	offers := []FlightOffer{
		{ID: "air-india", Airline: "Air India"},
		{ID: "other", Airline: "Other Airways"},
		{ID: "indigo", Airline: "IndiGo"},
	}
	sortOffersByPreferredAirlines(offers, []string{"IndiGo", "Air India"})
	if offers[0].ID != "indigo" || offers[1].ID != "air-india" || offers[2].ID != "other" {
		t.Fatalf("sorted offers = %+v", offers)
	}
}

func TestPersonalizeOffersMarksAndRanksPreferredFlights(t *testing.T) {
	offers := []FlightOffer{
		{ID: "other", Airline: "Other Airways", PriceINR: 5000, Stops: 1},
		{ID: "preferred", Airline: "IndiGo", PriceINR: 7000, Stops: 0},
	}
	personalizeOffers(offers, UserProfile{PreferredAirlines: []string{"IndiGo"}, BudgetVsComfort: "budget", AverageBookingPriceINR: 8000})
	if offers[0].ID != "preferred" || !offers[0].Recommended {
		t.Fatalf("personalized offers = %+v", offers)
	}
	if len(offers[0].PreferenceReasons) == 0 {
		t.Fatal("expected recommendation reasons")
	}
}

func TestRateOfferUsesPriceAndMarksUnavailableReliability(t *testing.T) {
	rating := rateOffer(FlightOffer{PriceINR: 5000, Stops: 0}, UserProfile{AverageBookingPriceINR: 10000})
	if rating.Overall <= 0 || rating.PriceSensitivity != 50 {
		t.Fatalf("rating = %+v, want neutral single-offer price score and positive overall score", rating)
	}
	if rating.Delay != 50 || rating.ReliabilityAvailable {
		t.Fatalf("rating delay/reliability = %+v", rating)
	}
	if len(rating.Reasons) == 0 {
		t.Fatal("expected rating reasons")
	}
}

func TestSampleBookingProfiles(t *testing.T) {
	store := NewInMemoryBookingStore()
	for _, userID := range []string{"user_001", "user_002", "user_003", "user_004"} {
		if count := len(store.RecentBookings(userID, 0)); count < 10 {
			t.Fatalf("%s has %d sample bookings, want at least 10", userID, count)
		}
	}

	tests := []struct {
		userID    string
		airline   string
		focus     string
		timeOfDay string
		cabin     string
	}{
		{userID: "user_001", airline: "IndiGo", focus: "budget", timeOfDay: "morning", cabin: "ECONOMY"},
		{userID: "user_002", airline: "Air India", focus: "comfort", timeOfDay: "evening", cabin: "BUSINESS"},
		{userID: "user_003", airline: "Emirates", focus: "balanced", timeOfDay: "night", cabin: "ECONOMY"},
	}

	for _, test := range tests {
		profile, err := store.Profile(test.userID)
		if err != nil {
			t.Fatalf("Profile(%q) returned error: %v", test.userID, err)
		}
		if len(profile.PreferredAirlines) == 0 || profile.PreferredAirlines[0] != test.airline {
			t.Errorf("%s preferred airline = %v, want %s", test.userID, profile.PreferredAirlines, test.airline)
		}
		if profile.BudgetVsComfort != test.focus {
			t.Errorf("%s focus = %q, want %q", test.userID, profile.BudgetVsComfort, test.focus)
		}
		if profile.PreferredTimeOfDay != test.timeOfDay {
			t.Errorf("%s preferred time = %q, want %q", test.userID, profile.PreferredTimeOfDay, test.timeOfDay)
		}
		if profile.PreferredCabinClass != test.cabin {
			t.Errorf("%s preferred cabin = %q, want %q", test.userID, profile.PreferredCabinClass, test.cabin)
		}
	}
}

func TestRecentBookingsAreNewestFirstAndLimited(t *testing.T) {
	store := NewInMemoryBookingStore()
	store.AddBooking(Booking{ID: "older", UserID: "test", BookedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
	store.AddBooking(Booking{ID: "newer", UserID: "test", BookedAt: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)})

	bookings := store.RecentBookings("test", 1)
	if len(bookings) != 1 || bookings[0].ID != "newer" {
		t.Fatalf("recent bookings = %+v, want only newer booking", bookings)
	}
}

func TestUnknownProfile(t *testing.T) {
	store := NewInMemoryBookingStore()
	if _, err := store.Profile("missing"); err == nil {
		t.Fatal("expected an error for an unknown user")
	}
	if summary := store.ProfileSummary("missing"); !strings.Contains(summary, "No stored booking profile") {
		t.Fatalf("unknown profile summary = %q", summary)
	}
}
