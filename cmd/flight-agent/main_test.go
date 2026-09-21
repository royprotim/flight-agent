package main

import (
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

func TestSampleBookingProfiles(t *testing.T) {
	store := NewInMemoryBookingStore()

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
