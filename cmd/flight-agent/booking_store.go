package main

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Booking is a completed or confirmed flight associated with a user.
type Booking struct {
	ID            string    `json:"id"`
	UserID        string    `json:"user_id"`
	Airline       string    `json:"airline"`
	Origin        string    `json:"origin"`
	Destination   string    `json:"destination"`
	DepartureTime time.Time `json:"departure_time"`
	CabinClass    string    `json:"cabin_class"`
	PriceINR      float64   `json:"price_inr"`
	Stops         int       `json:"stops"`
	BookedAt      time.Time `json:"booked_at"`
	Status        string    `json:"status"`
}

// UserProfile is derived from a user's recent bookings.
type UserProfile struct {
	UserID                 string   `json:"user_id"`
	PreferredAirlines      []string `json:"preferred_airlines"`
	BudgetVsComfort        string   `json:"budget_vs_comfort"`
	PreferredTimeOfDay     string   `json:"preferred_time_of_day"`
	PreferredCabinClass    string   `json:"preferred_cabin_class"`
	AverageBookingPriceINR float64  `json:"average_booking_price_inr"`
	AverageStops           float64  `json:"average_stops"`
	RecentRoutes           []string `json:"recent_routes"`
	BookingCount           int      `json:"booking_count"`
}

// InMemoryBookingStore stores recent bookings and derived user preferences.
type InMemoryBookingStore struct {
	mu       sync.RWMutex
	bookings map[string][]Booking
}

func NewInMemoryBookingStore() *InMemoryBookingStore {
	store := &InMemoryBookingStore{bookings: make(map[string][]Booking)}
	for _, booking := range sampleBookings() {
		store.AddBooking(booking)
	}
	return store
}

func (s *InMemoryBookingStore) AddBooking(booking Booking) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bookings[booking.UserID] = append(s.bookings[booking.UserID], booking)
}

func (s *InMemoryBookingStore) RecentBookings(userID string, limit int) []Booking {
	s.mu.RLock()
	defer s.mu.RUnlock()

	bookings := append([]Booking(nil), s.bookings[userID]...)
	sort.SliceStable(bookings, func(i, j int) bool {
		return bookings[i].BookedAt.After(bookings[j].BookedAt)
	})
	if limit > 0 && len(bookings) > limit {
		bookings = bookings[:limit]
	}
	return bookings
}

func (s *InMemoryBookingStore) Profile(userID string) (UserProfile, error) {
	bookings := s.RecentBookings(userID, 0)
	if len(bookings) == 0 {
		return UserProfile{}, fmt.Errorf("no booking history found for user %q", userID)
	}

	airlineCounts := make(map[string]int)
	cabinCounts := make(map[string]int)
	timeCounts := map[string]int{"morning": 0, "afternoon": 0, "evening": 0, "night": 0}
	uniqueRoutes := make(map[string]bool)
	var totalPrice float64
	var totalStops float64

	for _, booking := range bookings {
		airlineCounts[booking.Airline]++
		cabinCounts[strings.ToUpper(booking.CabinClass)]++
		timeCounts[timeOfDay(booking.DepartureTime)]++
		uniqueRoutes[booking.Origin+"-"+booking.Destination] = true
		totalPrice += booking.PriceINR
		totalStops += float64(booking.Stops)
	}

	preferredAirlines := sortedKeysByCount(airlineCounts)
	preferredCabin := sortedKeysByCount(cabinCounts)[0]
	preferredTime := sortedKeysByCount(timeCounts)[0]
	averagePrice := totalPrice / float64(len(bookings))
	averageStops := totalStops / float64(len(bookings))

	focus := "balanced"
	if averagePrice < 15000 {
		focus = "budget"
	} else if averagePrice >= 30000 && averageStops < 0.5 {
		focus = "comfort"
	}

	routes := make([]string, 0, len(uniqueRoutes))
	for route := range uniqueRoutes {
		routes = append(routes, route)
	}
	sort.Strings(routes)

	return UserProfile{
		UserID:                 userID,
		PreferredAirlines:      preferredAirlines,
		BudgetVsComfort:        focus,
		PreferredTimeOfDay:     preferredTime,
		PreferredCabinClass:    preferredCabin,
		AverageBookingPriceINR: averagePrice,
		AverageStops:           averageStops,
		RecentRoutes:           routes,
		BookingCount:           len(bookings),
	}, nil
}

func (s *InMemoryBookingStore) ProfileSummary(userID string) string {
	profile, err := s.Profile(userID)
	if err != nil {
		return fmt.Sprintf("No stored booking profile is available for user %q.", userID)
	}
	return fmt.Sprintf(
		"User %s has %d recent bookings. Preferred airlines: %s. Travel focus: %s. Preferred time: %s. Preferred cabin: %s. Average spend: ₹%.2f. Average stops: %.1f. Recent routes: %s.",
		profile.UserID,
		profile.BookingCount,
		strings.Join(profile.PreferredAirlines, ", "),
		profile.BudgetVsComfort,
		profile.PreferredTimeOfDay,
		profile.PreferredCabinClass,
		profile.AverageBookingPriceINR,
		profile.AverageStops,
		strings.Join(profile.RecentRoutes, ", "),
	)
}

func timeOfDay(departure time.Time) string {
	switch hour := departure.Hour(); {
	case hour >= 5 && hour < 12:
		return "morning"
	case hour >= 12 && hour < 17:
		return "afternoon"
	case hour >= 17 && hour < 21:
		return "evening"
	default:
		return "night"
	}
}

func sortedKeysByCount(counts map[string]int) []string {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if counts[keys[i]] == counts[keys[j]] {
			return keys[i] < keys[j]
		}
		return counts[keys[i]] > counts[keys[j]]
	})
	return keys
}

func sampleBookings() []Booking {
	date := func(value string) time.Time {
		parsed, _ := time.Parse(time.RFC3339, value)
		return parsed
	}
	return []Booking{
		{ID: "bk_001", UserID: "user_001", Airline: "IndiGo", Origin: "DEL", Destination: "BOM", DepartureTime: date("2026-08-12T07:30:00+05:30"), CabinClass: "ECONOMY", PriceINR: 6500, Stops: 0, BookedAt: date("2026-07-20T10:00:00+05:30")},
		{ID: "bk_002", UserID: "user_001", Airline: "IndiGo", Origin: "BOM", Destination: "DEL", DepartureTime: date("2026-08-18T08:15:00+05:30"), CabinClass: "ECONOMY", PriceINR: 7200, Stops: 0, BookedAt: date("2026-07-22T10:00:00+05:30")},
		{ID: "bk_003", UserID: "user_001", Airline: "Air India", Origin: "DEL", Destination: "GOI", DepartureTime: date("2026-09-05T14:20:00+05:30"), CabinClass: "ECONOMY", PriceINR: 9800, Stops: 1, BookedAt: date("2026-08-01T10:00:00+05:30")},
		{ID: "bk_011", UserID: "user_001", Airline: "IndiGo", Origin: "DEL", Destination: "BLR", DepartureTime: date("2026-08-25T06:45:00+05:30"), CabinClass: "ECONOMY", PriceINR: 5900, Stops: 0, BookedAt: date("2026-08-05T10:00:00+05:30")},
		{ID: "bk_012", UserID: "user_001", Airline: "IndiGo", Origin: "BLR", Destination: "DEL", DepartureTime: date("2026-08-29T09:00:00+05:30"), CabinClass: "ECONOMY", PriceINR: 6100, Stops: 0, BookedAt: date("2026-08-06T10:00:00+05:30")},
		{ID: "bk_013", UserID: "user_001", Airline: "SpiceJet", Origin: "DEL", Destination: "CCU", DepartureTime: date("2026-09-10T11:20:00+05:30"), CabinClass: "ECONOMY", PriceINR: 5400, Stops: 0, BookedAt: date("2026-08-10T10:00:00+05:30")},
		{ID: "bk_014", UserID: "user_001", Airline: "IndiGo", Origin: "HYD", Destination: "DEL", DepartureTime: date("2026-09-15T07:10:00+05:30"), CabinClass: "ECONOMY", PriceINR: 6800, Stops: 0, BookedAt: date("2026-08-15T10:00:00+05:30")},
		{ID: "bk_015", UserID: "user_001", Airline: "IndiGo", Origin: "DEL", Destination: "HYD", DepartureTime: date("2026-09-18T08:40:00+05:30"), CabinClass: "ECONOMY", PriceINR: 6300, Stops: 0, BookedAt: date("2026-08-18T10:00:00+05:30")},
		{ID: "bk_016", UserID: "user_001", Airline: "Air India", Origin: "DEL", Destination: "MAA", DepartureTime: date("2026-09-20T13:15:00+05:30"), CabinClass: "ECONOMY", PriceINR: 8900, Stops: 1, BookedAt: date("2026-08-20T10:00:00+05:30")},
		{ID: "bk_017", UserID: "user_001", Airline: "IndiGo", Origin: "BOM", Destination: "GOI", DepartureTime: date("2026-09-22T10:30:00+05:30"), CabinClass: "ECONOMY", PriceINR: 4800, Stops: 0, BookedAt: date("2026-08-22T10:00:00+05:30")},

		{ID: "bk_004", UserID: "user_002", Airline: "Air India", Origin: "DEL", Destination: "LHR", DepartureTime: date("2026-06-10T21:00:00+05:30"), CabinClass: "BUSINESS", PriceINR: 145000, Stops: 0, BookedAt: date("2026-04-10T10:00:00+05:30")},
		{ID: "bk_005", UserID: "user_002", Airline: "Air India", Origin: "LHR", Destination: "DEL", DepartureTime: date("2026-06-20T20:30:00+01:00"), CabinClass: "BUSINESS", PriceINR: 152000, Stops: 0, BookedAt: date("2026-04-12T10:00:00+05:30")},
		{ID: "bk_006", UserID: "user_002", Airline: "Vistara", Origin: "DEL", Destination: "SIN", DepartureTime: date("2026-08-14T19:00:00+05:30"), CabinClass: "PREMIUM_ECONOMY", PriceINR: 52000, Stops: 0, BookedAt: date("2026-06-15T10:00:00+05:30")},
		{ID: "bk_018", UserID: "user_002", Airline: "Air India", Origin: "DEL", Destination: "DXB", DepartureTime: date("2026-07-05T20:00:00+05:30"), CabinClass: "BUSINESS", PriceINR: 98000, Stops: 0, BookedAt: date("2026-05-05T10:00:00+05:30")},
		{ID: "bk_019", UserID: "user_002", Airline: "Air India", Origin: "DXB", Destination: "DEL", DepartureTime: date("2026-07-14T22:15:00+04:00"), CabinClass: "BUSINESS", PriceINR: 101000, Stops: 0, BookedAt: date("2026-05-14T10:00:00+05:30")},
		{ID: "bk_020", UserID: "user_002", Airline: "Vistara", Origin: "DEL", Destination: "BKK", DepartureTime: date("2026-08-22T18:30:00+05:30"), CabinClass: "PREMIUM_ECONOMY", PriceINR: 61000, Stops: 0, BookedAt: date("2026-06-22T10:00:00+05:30")},
		{ID: "bk_021", UserID: "user_002", Airline: "Air India", Origin: "BOM", Destination: "FRA", DepartureTime: date("2026-08-30T19:45:00+05:30"), CabinClass: "BUSINESS", PriceINR: 132000, Stops: 0, BookedAt: date("2026-06-30T10:00:00+05:30")},
		{ID: "bk_022", UserID: "user_002", Airline: "Air India", Origin: "FRA", Destination: "BOM", DepartureTime: date("2026-09-08T20:30:00+02:00"), CabinClass: "BUSINESS", PriceINR: 129000, Stops: 0, BookedAt: date("2026-07-08T10:00:00+05:30")},
		{ID: "bk_023", UserID: "user_002", Airline: "Vistara", Origin: "DEL", Destination: "HKG", DepartureTime: date("2026-09-15T17:40:00+05:30"), CabinClass: "PREMIUM_ECONOMY", PriceINR: 74000, Stops: 0, BookedAt: date("2026-07-15T10:00:00+05:30")},
		{ID: "bk_024", UserID: "user_002", Airline: "Air India", Origin: "DEL", Destination: "SYD", DepartureTime: date("2026-09-20T21:30:00+05:30"), CabinClass: "BUSINESS", PriceINR: 178000, Stops: 1, BookedAt: date("2026-07-20T10:00:00+05:30")},

		{ID: "bk_007", UserID: "user_003", Airline: "Emirates", Origin: "BOM", Destination: "DXB", DepartureTime: date("2026-07-02T23:00:00+05:30"), CabinClass: "ECONOMY", PriceINR: 24000, Stops: 0, BookedAt: date("2026-05-02T10:00:00+05:30")},
		{ID: "bk_008", UserID: "user_003", Airline: "Emirates", Origin: "DEL", Destination: "JFK", DepartureTime: date("2026-07-15T22:00:00+05:30"), CabinClass: "ECONOMY", PriceINR: 68000, Stops: 1, BookedAt: date("2026-05-15T10:00:00+05:30")},
		{ID: "bk_025", UserID: "user_003", Airline: "Emirates", Origin: "BOM", Destination: "LHR", DepartureTime: date("2026-07-25T23:30:00+05:30"), CabinClass: "ECONOMY", PriceINR: 59000, Stops: 1, BookedAt: date("2026-05-25T10:00:00+05:30")},
		{ID: "bk_026", UserID: "user_003", Airline: "Emirates", Origin: "LHR", Destination: "BOM", DepartureTime: date("2026-08-02T22:00:00+01:00"), CabinClass: "ECONOMY", PriceINR: 61000, Stops: 1, BookedAt: date("2026-06-02T10:00:00+05:30")},
		{ID: "bk_027", UserID: "user_003", Airline: "Qatar Airways", Origin: "DEL", Destination: "DOH", DepartureTime: date("2026-08-11T22:30:00+05:30"), CabinClass: "ECONOMY", PriceINR: 27000, Stops: 0, BookedAt: date("2026-06-11T10:00:00+05:30")},
		{ID: "bk_028", UserID: "user_003", Airline: "Emirates", Origin: "BOM", Destination: "NRT", DepartureTime: date("2026-08-19T23:15:00+05:30"), CabinClass: "ECONOMY", PriceINR: 72000, Stops: 1, BookedAt: date("2026-06-19T10:00:00+05:30")},
		{ID: "bk_029", UserID: "user_003", Airline: "Emirates", Origin: "DEL", Destination: "MEL", DepartureTime: date("2026-08-28T21:45:00+05:30"), CabinClass: "ECONOMY", PriceINR: 75000, Stops: 1, BookedAt: date("2026-06-28T10:00:00+05:30")},
		{ID: "bk_030", UserID: "user_003", Airline: "Qatar Airways", Origin: "BOM", Destination: "CDG", DepartureTime: date("2026-09-05T22:15:00+05:30"), CabinClass: "ECONOMY", PriceINR: 64000, Stops: 1, BookedAt: date("2026-07-05T10:00:00+05:30")},
		{ID: "bk_031", UserID: "user_003", Airline: "Emirates", Origin: "DXB", Destination: "BOM", DepartureTime: date("2026-09-14T23:00:00+04:00"), CabinClass: "ECONOMY", PriceINR: 23000, Stops: 0, BookedAt: date("2026-07-14T10:00:00+05:30")},
		{ID: "bk_032", UserID: "user_003", Airline: "Emirates", Origin: "DEL", Destination: "SEA", DepartureTime: date("2026-09-20T22:45:00+05:30"), CabinClass: "ECONOMY", PriceINR: 81000, Stops: 1, BookedAt: date("2026-07-20T10:00:00+05:30")},

		{ID: "bk_009", UserID: "user_004", Airline: "IndiGo", Origin: "BLR", Destination: "HYD", DepartureTime: date("2026-09-01T13:00:00+05:30"), CabinClass: "ECONOMY", PriceINR: 4200, Stops: 0, BookedAt: date("2026-08-10T10:00:00+05:30")},
		{ID: "bk_010", UserID: "user_004", Airline: "Air India", Origin: "BLR", Destination: "CCU", DepartureTime: date("2026-09-12T11:30:00+05:30"), CabinClass: "ECONOMY", PriceINR: 7800, Stops: 0, BookedAt: date("2026-08-12T10:00:00+05:30")},
		{ID: "bk_033", UserID: "user_004", Airline: "IndiGo", Origin: "BLR", Destination: "DEL", DepartureTime: date("2026-08-15T06:30:00+05:30"), CabinClass: "ECONOMY", PriceINR: 5100, Stops: 0, BookedAt: date("2026-07-15T10:00:00+05:30")},
		{ID: "bk_034", UserID: "user_004", Airline: "Air India", Origin: "DEL", Destination: "BLR", DepartureTime: date("2026-08-20T10:00:00+05:30"), CabinClass: "ECONOMY", PriceINR: 6900, Stops: 0, BookedAt: date("2026-07-20T10:00:00+05:30")},
		{ID: "bk_035", UserID: "user_004", Airline: "IndiGo", Origin: "BLR", Destination: "BOM", DepartureTime: date("2026-08-25T14:00:00+05:30"), CabinClass: "ECONOMY", PriceINR: 4700, Stops: 0, BookedAt: date("2026-07-25T10:00:00+05:30")},
		{ID: "bk_036", UserID: "user_004", Airline: "Air India", Origin: "BOM", Destination: "BLR", DepartureTime: date("2026-08-30T12:15:00+05:30"), CabinClass: "ECONOMY", PriceINR: 5500, Stops: 0, BookedAt: date("2026-07-30T10:00:00+05:30")},
		{ID: "bk_037", UserID: "user_004", Airline: "IndiGo", Origin: "BLR", Destination: "MAA", DepartureTime: date("2026-09-05T09:45:00+05:30"), CabinClass: "ECONOMY", PriceINR: 3900, Stops: 0, BookedAt: date("2026-08-05T10:00:00+05:30")},
		{ID: "bk_038", UserID: "user_004", Airline: "Air India", Origin: "BLR", Destination: "DEL", DepartureTime: date("2026-09-10T11:00:00+05:30"), CabinClass: "ECONOMY", PriceINR: 6200, Stops: 0, BookedAt: date("2026-08-10T10:00:00+05:30")},
		{ID: "bk_039", UserID: "user_004", Airline: "IndiGo", Origin: "HYD", Destination: "BLR", DepartureTime: date("2026-09-15T13:30:00+05:30"), CabinClass: "ECONOMY", PriceINR: 4100, Stops: 0, BookedAt: date("2026-08-15T10:00:00+05:30")},
		{ID: "bk_040", UserID: "user_004", Airline: "Air India", Origin: "BLR", Destination: "GOI", DepartureTime: date("2026-09-20T15:45:00+05:30"), CabinClass: "ECONOMY", PriceINR: 7300, Stops: 0, BookedAt: date("2026-08-20T10:00:00+05:30")},
	}
}
