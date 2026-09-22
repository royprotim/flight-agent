package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"google.golang.org/genai"
)

// -------------------------------------------------------------------
// 1. Domain Schemas & Structs
// -------------------------------------------------------------------

// FlightSearchParams defines the strictly typed input extracted by the LLM.
type FlightSearchParams struct {
	Origin        string `json:"origin"`
	Destination   string `json:"destination"`
	DepartureDate string `json:"departure_date"`
	Passengers    int    `json:"passengers"`
	CabinClass    string `json:"cabin_class"`
	Preference    string `json:"preference,omitempty"`
}

func flightSearchSchema() *genai.Schema {
	return &genai.Schema{
		Type: genai.TypeObject,
		Properties: map[string]*genai.Schema{
			"origin":         {Type: genai.TypeString},
			"destination":    {Type: genai.TypeString},
			"departure_date": {Type: genai.TypeString},
			"passengers":     {Type: genai.TypeInteger},
			"cabin_class":    {Type: genai.TypeString, Enum: []string{"ECONOMY", "PREMIUM_ECONOMY", "BUSINESS", "FIRST"}},
			"preference":     {Type: genai.TypeString},
		},
		Required: []string{"origin", "destination", "departure_date", "passengers", "cabin_class"},
	}
}

func extractFlightSearchParams(ctx context.Context, client *genai.Client, query string) (FlightSearchParams, error) {
	if client == nil {
		return FlightSearchParams{}, fmt.Errorf("GEMINI_API_KEY is required for natural-language searches")
	}
	response, err := client.Models.GenerateContent(ctx, modelName(), []*genai.Content{
		genai.NewContentFromText("Extract the flight search details from this request. Keep any extra constraints in preference. Request: "+query, genai.RoleUser),
	}, &genai.GenerateContentConfig{
		ResponseMIMEType: "application/json",
		ResponseSchema:   flightSearchSchema(),
	})
	if err != nil {
		return FlightSearchParams{}, fmt.Errorf("extract flight query: %w", err)
	}
	var params FlightSearchParams
	if err := json.Unmarshal([]byte(response.Text()), &params); err != nil {
		return FlightSearchParams{}, fmt.Errorf("decode extracted flight query: %w", err)
	}
	return params, nil
}

// BuildFlightQuery combines structured trip details with the user's
// free-form preference into the canonical search intent.
func BuildFlightQuery(params FlightSearchParams) string {
	query := fmt.Sprintf("Find a %s flight for %d passenger(s) from %s to %s on %s.",
		strings.ToLower(params.CabinClass),
		params.Passengers,
		strings.ToUpper(strings.TrimSpace(params.Origin)),
		strings.ToUpper(strings.TrimSpace(params.Destination)),
		strings.TrimSpace(params.DepartureDate),
	)
	if preference := strings.TrimSpace(params.Preference); preference != "" {
		query += " User preference: " + preference
	}
	return query
}

// FlightOffer represents a flight option returned by the provider API.
type FlightOffer struct {
	ID                 string        `json:"id"`
	Airline            string        `json:"airline"`
	FlightNum          string        `json:"flight_number"`
	Origin             string        `json:"origin"`
	Dest               string        `json:"destination"`
	DepTime            string        `json:"departure_time"`
	ArrTime            string        `json:"arrival_time"`
	Duration           string        `json:"duration"`
	PriceINR           float64       `json:"price_inr"`
	Stops              int           `json:"stops"`
	ConnectionAirport  string        `json:"connection_airport,omitempty"`
	ConnectionAirports []string      `json:"connection_airports,omitempty"`
	LayoverHours       float64       `json:"layover_hours,omitempty"`
	Recommended        bool          `json:"recommended"`
	PreferenceReasons  []string      `json:"preference_reasons,omitempty"`
	Rating             *FlightRating `json:"rating,omitempty"`
}

type FlightRating struct {
	Overall              int      `json:"overall"`
	PriceSensitivity     int      `json:"price_sensitivity"`
	Delay                int      `json:"delay"`
	Reliability          int      `json:"reliability"`
	ReliabilityAvailable bool     `json:"reliability_available"`
	Reasons              []string `json:"reasons,omitempty"`
}

type LayoverExplanation struct {
	Title               string  `json:"title"`
	RequestedHours      float64 `json:"requested_hours"`
	Airport             string  `json:"airport"`
	Reason              string  `json:"reason"`
	InboundDescription  string  `json:"inbound_description,omitempty"`
	OutboundDescription string  `json:"outbound_description,omitempty"`
}

type layoverNoMatchError struct {
	explanation LayoverExplanation
}

func (e *layoverNoMatchError) Error() string {
	return e.explanation.Reason
}

// -------------------------------------------------------------------
// 2. Flight Service Tool (Duffel)
// -------------------------------------------------------------------

type duffelOfferRequest struct {
	Data duffelOfferRequestData `json:"data"`
}

type duffelOfferRequestData struct {
	Slices         []duffelSliceRequest `json:"slices"`
	Passengers     []duffelPassenger    `json:"passengers"`
	CabinClass     string               `json:"cabin_class"`
	MaxConnections int                  `json:"max_connections,omitempty"`
}

type duffelSliceRequest struct {
	Origin        string `json:"origin"`
	Destination   string `json:"destination"`
	DepartureDate string `json:"departure_date"`
}

type duffelPassenger struct {
	Type string `json:"type"`
}

type duffelOfferResponse struct {
	Data struct {
		Offers []duffelOffer `json:"offers"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type duffelPlaceSuggestionsResponse struct {
	Data []struct {
		Type     string `json:"type"`
		IATACode string `json:"iata_code"`
		Name     string `json:"name"`
		CityName string `json:"city_name"`
	} `json:"data"`
}

type duffelOffer struct {
	ID            string        `json:"id"`
	TotalAmount   string        `json:"total_amount"`
	TotalCurrency string        `json:"total_currency"`
	Slices        []duffelSlice `json:"slices"`
}

type duffelSlice struct {
	Duration string          `json:"duration"`
	Segments []duffelSegment `json:"segments"`
}

type duffelSegment struct {
	DepartingAt      string `json:"departing_at"`
	ArrivingAt       string `json:"arriving_at"`
	MarketingCarrier struct {
		Name string `json:"name"`
	} `json:"marketing_carrier"`
	OperatingCarrier struct {
		Name string `json:"name"`
	} `json:"operating_carrier"`
	MarketingCarrierFlightNumber string `json:"marketing_carrier_flight_number"`
	DepartingAirport             struct {
		IATACode string `json:"iata_code"`
	} `json:"departing_airport"`
	ArrivingAirport struct {
		IATACode string `json:"iata_code"`
	} `json:"arriving_airport"`
	Origin struct {
		IATACode string `json:"iata_code"`
	} `json:"origin"`
	Destination struct {
		IATACode string `json:"iata_code"`
	} `json:"destination"`
}

func segmentOriginCode(segment duffelSegment) string {
	if segment.Origin.IATACode != "" {
		return segment.Origin.IATACode
	}
	return segment.DepartingAirport.IATACode
}

func segmentDestinationCode(segment duffelSegment) string {
	if segment.Destination.IATACode != "" {
		return segment.Destination.IATACode
	}
	return segment.ArrivingAirport.IATACode
}

// SearchFlights creates a Duffel offer request and maps its offers to the
// domain type used by the Gemini tool loop.
func SearchFlights(ctx context.Context, params FlightSearchParams) ([]FlightOffer, error) {
	fmt.Printf("\n[TOOL EXECUTION] Searching flights: %s -> %s on %s (Class: %s, Pax: %d)...\n",
		params.Origin, params.Destination, params.DepartureDate, params.CabinClass, params.Passengers)

	token := os.Getenv("DUFFEL_API_TOKEN")
	if token == "" {
		return nil, fmt.Errorf("DUFFEL_API_TOKEN is not set")
	}
	if params.Passengers < 1 {
		return nil, fmt.Errorf("passengers must be at least 1")
	}
	origin, err := resolveAirportCode(ctx, token, params.Origin)
	if err != nil {
		return nil, err
	}
	destination, err := resolveAirportCode(ctx, token, params.Destination)
	if err != nil {
		return nil, err
	}

	requestBody := duffelOfferRequest{Data: duffelOfferRequestData{
		Slices: []duffelSliceRequest{{
			Origin:        origin,
			Destination:   destination,
			DepartureDate: params.DepartureDate,
		}},
		Passengers: makePassengers(params.Passengers),
		CabinClass: strings.ToLower(params.CabinClass),
	}}
	layover, hasLayover := parseLayoverPreference(params.Preference)
	if hasLayover {
		viaOffers, err := searchViaAirport(ctx, token, origin, destination, layover, params)
		if err != nil {
			return nil, err
		}
		return mapDuffelOffers(viaOffers)
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("encode Duffel request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.duffel.com/air/offer_requests?return_offers=true&supplier_timeout=20000",
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Duffel request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Duffel-Version", "v2")

	response, err := (&http.Client{Timeout: 70 * time.Second}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Duffel API: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read Duffel response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Duffel API returned %s: %s", response.Status, compactResponse(responseBody))
	}

	var result duffelOfferResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode Duffel response: %w", err)
	}
	if len(result.Data.Offers) == 0 {
		return nil, fmt.Errorf("Duffel returned no flight offers")
	}
	if hasLayover {
		allOffers := result.Data.Offers
		matchingOffers := filterLayoverOffers(allOffers, layover)
		if len(matchingOffers) == 0 {
			matchingOffers = filterOffersViaAirport(allOffers, layover.airport)
		}
		result.Data.Offers = matchingOffers
		if len(result.Data.Offers) == 0 {
			return nil, &layoverNoMatchError{explanation: buildLayoverExplanation(allOffers, layover, origin, destination)}
		}
	}

	offers := make([]FlightOffer, 0, len(result.Data.Offers))
	for _, offer := range result.Data.Offers {
		mapped, err := mapDuffelOffer(offer)
		if err != nil {
			continue
		}
		offers = append(offers, mapped)
	}
	if len(offers) == 0 {
		return nil, fmt.Errorf("Duffel returned offers with no usable flight segments")
	}
	return offers, nil
}

func searchViaAirport(ctx context.Context, token, origin, destination string, preference layoverPreference, params FlightSearchParams) ([]duffelOffer, error) {
	inbound, err := fetchDuffelOffers(ctx, token, duffelOfferRequest{Data: duffelOfferRequestData{
		Slices:     []duffelSliceRequest{{Origin: origin, Destination: preference.airport, DepartureDate: params.DepartureDate}},
		Passengers: makePassengers(params.Passengers), CabinClass: strings.ToLower(params.CabinClass), MaxConnections: 0,
	}})
	if err != nil {
		return nil, fmt.Errorf("search inbound leg to %s: %w", preference.airport, err)
	}
	outbound, err := fetchDuffelOffers(ctx, token, duffelOfferRequest{Data: duffelOfferRequestData{
		Slices:     []duffelSliceRequest{{Origin: preference.airport, Destination: destination, DepartureDate: params.DepartureDate}},
		Passengers: makePassengers(params.Passengers), CabinClass: strings.ToLower(params.CabinClass), MaxConnections: 0,
	}})
	if err != nil {
		return nil, fmt.Errorf("search outbound leg from %s: %w", preference.airport, err)
	}

	combined := make([]duffelOffer, 0)
	for _, first := range inbound {
		for _, second := range outbound {
			if len(first.Slices) == 0 || len(second.Slices) == 0 || len(first.Slices[0].Segments) == 0 || len(second.Slices[0].Segments) == 0 {
				continue
			}
			firstSegment := first.Slices[0].Segments[len(first.Slices[0].Segments)-1]
			secondSegment := second.Slices[0].Segments[0]
			arrivedAt, firstErr := time.Parse(time.RFC3339, firstSegment.ArrivingAt)
			departedAt, secondErr := time.Parse(time.RFC3339, secondSegment.DepartingAt)
			if firstErr != nil || secondErr != nil || !departedAt.After(arrivedAt) {
				continue
			}
			layoverHours := departedAt.Sub(arrivedAt).Hours()
			if preference.hours > 0 && (layoverHours < preference.hours-0.5 || layoverHours > preference.hours+0.5) {
				continue
			}
			amount, firstErr := strconv.ParseFloat(first.TotalAmount, 64)
			secondAmount, secondErr := strconv.ParseFloat(second.TotalAmount, 64)
			if firstErr != nil || secondErr != nil || first.TotalCurrency != second.TotalCurrency {
				continue
			}
			segments := append([]duffelSegment{}, first.Slices[0].Segments...)
			segments = append(segments, second.Slices[0].Segments...)
			combined = append(combined, duffelOffer{
				ID:          "combined_" + first.ID + "_" + second.ID,
				TotalAmount: strconv.FormatFloat(amount+secondAmount, 'f', 2, 64), TotalCurrency: first.TotalCurrency,
				Slices: []duffelSlice{{Duration: first.Slices[0].Duration + " + " + second.Slices[0].Duration, Segments: segments}},
			})
		}
	}
	if len(combined) == 0 {
		return nil, &layoverNoMatchError{explanation: buildLayoverExplanation(nil, preference, origin, destination)}
	}
	return combined, nil
}

func fetchDuffelOffers(ctx context.Context, token string, requestBody duffelOfferRequest) ([]duffelOffer, error) {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("encode Duffel request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.duffel.com/air/offer_requests?return_offers=true&supplier_timeout=20000", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create Duffel request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Duffel-Version", "v2")
	response, err := (&http.Client{Timeout: 70 * time.Second}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Duffel API: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("Duffel API returned %s: %s", response.Status, compactResponse(responseBody))
	}
	var result duffelOfferResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return nil, fmt.Errorf("decode Duffel response: %w", err)
	}
	return result.Data.Offers, nil
}

func mapDuffelOffers(rawOffers []duffelOffer) ([]FlightOffer, error) {
	offers := make([]FlightOffer, 0, len(rawOffers))
	for _, raw := range rawOffers {
		mapped, err := mapDuffelOffer(raw)
		if err == nil {
			offers = append(offers, mapped)
		}
	}
	if len(offers) == 0 {
		return nil, fmt.Errorf("Duffel returned no usable flight offers")
	}
	return offers, nil
}

func buildLayoverExplanation(offers []duffelOffer, preference layoverPreference, origin, destination string) LayoverExplanation {
	title := fmt.Sprintf("No flight combinations include a connection in %s", preference.airport)
	reason := fmt.Sprintf("No returned itinerary from %s to %s includes a connection in %s.", origin, destination, preference.airport)
	if preference.hours > 0 {
		title = fmt.Sprintf("No flight combinations match a %.0f-hour layover in %s", preference.hours, preference.airport)
		reason = fmt.Sprintf("No returned itinerary from %s to %s includes a connection in %s within the requested layover window.", origin, destination, preference.airport)
	}
	explanation := LayoverExplanation{
		Title:          title,
		RequestedHours: preference.hours,
		Airport:        preference.airport,
		Reason:         reason,
	}
	var latestArrival, earliestDeparture time.Time
	var latestArrivalText, earliestDepartureText string
	for _, offer := range offers {
		for _, slice := range offer.Slices {
			for index, segment := range slice.Segments {
				if index+1 < len(slice.Segments) && segment.ArrivingAirport.IATACode == preference.airport {
					if value, err := time.Parse(time.RFC3339, segment.ArrivingAt); err == nil && (latestArrival.IsZero() || value.After(latestArrival)) {
						latestArrival, latestArrivalText = value, segment.ArrivingAt
					}
				}
				if index > 0 && segment.DepartingAirport.IATACode == preference.airport {
					if value, err := time.Parse(time.RFC3339, segment.DepartingAt); err == nil && (earliestDeparture.IsZero() || value.Before(earliestDeparture)) {
						earliestDeparture, earliestDepartureText = value, segment.DepartingAt
					}
				}
			}
		}
	}
	if latestArrivalText != "" {
		explanation.InboundDescription = fmt.Sprintf("Latest observed arrival into %s: %s.", preference.airport, latestArrivalText)
	}
	if earliestDepartureText != "" {
		explanation.OutboundDescription = fmt.Sprintf("Earliest observed departure from %s: %s.", preference.airport, earliestDepartureText)
	}
	return explanation
}

type layoverPreference struct {
	airport string
	hours   float64
}

func parseLayoverPreference(preference string) (layoverPreference, bool) {
	preferenceLower := strings.ToLower(preference)
	if !strings.Contains(preferenceLower, "layover") && !strings.Contains(preferenceLower, "stopover") {
		return layoverPreference{}, false
	}
	match := regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(?:hours?|hrs?)`).FindStringSubmatch(preference)
	hours := 0.0
	if len(match) == 2 {
		parsedHours, err := strconv.ParseFloat(match[1], 64)
		if err == nil && parsedHours > 0 {
			hours = parsedHours
		}
	}
	airport := ""
	for _, candidate := range []struct{ name, code string }{{"kolkata", "CCU"}, {"calcutta", "CCU"}, {"delhi", "DEL"}, {"mumbai", "BOM"}, {"bangalore", "BLR"}, {"bengaluru", "BLR"}, {"chennai", "MAA"}, {"hyderabad", "HYD"}} {
		if strings.Contains(preferenceLower, candidate.name) {
			airport = candidate.code
			break
		}
	}
	if airport == "" {
		return layoverPreference{}, false
	}
	return layoverPreference{airport: airport, hours: hours}, true
}

func filterLayoverOffers(offers []duffelOffer, preference layoverPreference) []duffelOffer {
	if preference.hours == 0 {
		return filterOffersViaAirport(offers, preference.airport)
	}
	filtered := make([]duffelOffer, 0, len(offers))
	for _, offer := range offers {
		for _, slice := range offer.Slices {
			for index := 0; index+1 < len(slice.Segments); index++ {
				arriving := slice.Segments[index]
				departing := slice.Segments[index+1]
				if arriving.ArrivingAirport.IATACode != preference.airport && departing.DepartingAirport.IATACode != preference.airport {
					continue
				}
				arrivedAt, err := time.Parse(time.RFC3339, arriving.ArrivingAt)
				if err != nil {
					continue
				}
				departsAt, err := time.Parse(time.RFC3339, departing.DepartingAt)
				if err != nil {
					continue
				}
				actualHours := departsAt.Sub(arrivedAt).Hours()
				if actualHours >= preference.hours-0.5 && actualHours <= preference.hours+0.5 {
					filtered = append(filtered, offer)
				}
			}
		}
	}
	return filtered
}

func filterOffersViaAirport(offers []duffelOffer, airport string) []duffelOffer {
	filtered := make([]duffelOffer, 0, len(offers))
	for _, offer := range offers {
		for _, slice := range offer.Slices {
			for index := 0; index+1 < len(slice.Segments); index++ {
				if slice.Segments[index].ArrivingAirport.IATACode == airport || slice.Segments[index+1].DepartingAirport.IATACode == airport {
					filtered = append(filtered, offer)
					break
				}
			}
		}
	}
	return filtered
}

func resolveAirportCode(ctx context.Context, token, value string) (string, error) {
	cleaned := strings.ToUpper(strings.TrimSpace(value))
	if len(cleaned) == 3 && cleaned >= "AAA" && cleaned <= "ZZZ" {
		return cleaned, nil
	}
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("airport or city is required")
	}

	endpoint := "https://api.duffel.com/places/suggestions?query=" + url.QueryEscape(strings.TrimSpace(value))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("create Duffel place lookup: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Duffel-Version", "v2")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return "", fmt.Errorf("lookup %q with Duffel: %w", value, err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read Duffel place lookup: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Duffel place lookup returned %s: %s", response.Status, compactResponse(responseBody))
	}
	var result duffelPlaceSuggestionsResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", fmt.Errorf("decode Duffel place lookup: %w", err)
	}
	for _, place := range result.Data {
		if place.IATACode != "" {
			return place.IATACode, nil
		}
	}
	return "", fmt.Errorf("Duffel found no airport or city for %q", value)
}

func makePassengers(count int) []duffelPassenger {
	passengers := make([]duffelPassenger, count)
	for i := range passengers {
		passengers[i].Type = "adult"
	}
	return passengers
}

func mapDuffelOffer(offer duffelOffer) (FlightOffer, error) {
	if len(offer.Slices) == 0 || len(offer.Slices[0].Segments) == 0 {
		return FlightOffer{}, fmt.Errorf("offer %s has no segments", offer.ID)
	}
	slice := offer.Slices[0]
	first := slice.Segments[0]
	last := slice.Segments[len(slice.Segments)-1]
	price, err := strconv.ParseFloat(offer.TotalAmount, 64)
	if err != nil {
		return FlightOffer{}, fmt.Errorf("parse offer %s price: %w", offer.ID, err)
	}
	priceINR, err := convertToINR(price, offer.TotalCurrency)
	if err != nil {
		return FlightOffer{}, fmt.Errorf("convert offer %s price: %w", offer.ID, err)
	}
	airline := first.OperatingCarrier.Name
	if airline == "" {
		airline = first.MarketingCarrier.Name
	}
	mapped := FlightOffer{
		ID:        offer.ID,
		Airline:   airline,
		FlightNum: first.MarketingCarrierFlightNumber,
		Origin:    segmentOriginCode(first),
		Dest:      segmentDestinationCode(last),
		DepTime:   first.DepartingAt,
		ArrTime:   last.ArrivingAt,
		Duration:  slice.Duration,
		PriceINR:  priceINR,
		Stops:     len(slice.Segments) - 1,
	}
	if len(slice.Segments) > 1 {
		for index := 0; index+1 < len(slice.Segments); index++ {
			connection := slice.Segments[index]
			next := slice.Segments[index+1]
			airport := segmentDestinationCode(connection)
			if airport == "" {
				airport = segmentOriginCode(next)
			}
			if airport != "" {
				mapped.ConnectionAirports = append(mapped.ConnectionAirports, airport)
				if mapped.ConnectionAirport == "" {
					mapped.ConnectionAirport = airport
				}
			}
			if index == 0 {
				mapped.LayoverHours = layoverHoursBetween(connection.ArrivingAt, next.DepartingAt)
			}
		}
	}
	return mapped, nil
}

func layoverHoursBetween(arrival, departure string) float64 {
	arrivedAt, arrivalErr := parseDuffelTime(arrival)
	departedAt, departureErr := parseDuffelTime(departure)
	if arrivalErr != nil || departureErr != nil || !departedAt.After(arrivedAt) {
		return 0
	}
	return departedAt.Sub(arrivedAt).Hours()
}

func parseDuffelTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05-07:00"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported Duffel time %q", value)
}

func convertToINR(amount float64, currency string) (float64, error) {
	currency = strings.ToUpper(strings.TrimSpace(currency))
	if currency == "INR" {
		return amount, nil
	}

	rateText := os.Getenv("DUFFEL_TO_INR_RATE")
	if rateText == "" {
		return 0, fmt.Errorf("offer currency is %s; set DUFFEL_TO_INR_RATE to convert it to INR", currency)
	}
	rate, err := strconv.ParseFloat(rateText, 64)
	if err != nil || rate <= 0 {
		return 0, fmt.Errorf("DUFFEL_TO_INR_RATE must be a positive number")
	}
	return amount * rate, nil
}

func compactResponse(body []byte) string {
	const maxResponseLength = 1000
	response := strings.TrimSpace(string(body))
	if len(response) > maxResponseLength {
		return response[:maxResponseLength] + "..."
	}
	return response
}

// -------------------------------------------------------------------
// 3. Tool Definition for the LLM
// -------------------------------------------------------------------

func getFlightSearchToolDefinition() *genai.Tool {
	return &genai.Tool{
		FunctionDeclarations: []*genai.FunctionDeclaration{{
			Name:        "search_flights",
			Description: "Search for available one-way or round-trip airline flights based on route and date constraints.",
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"origin": {
						Type:        genai.TypeString,
						Description: "The 3-letter IATA origin airport code (e.g., SFO, JFK, LHR).",
					},
					"destination": {
						Type:        genai.TypeString,
						Description: "The 3-letter IATA destination airport code (e.g., LHR, CDG, NRT).",
					},
					"departure_date": {
						Type:        genai.TypeString,
						Description: "Date of departure in YYYY-MM-DD format.",
					},
					"passengers": {
						Type:        genai.TypeInteger,
						Description: "Number of passengers travelling. Defaults to 1.",
					},
					"cabin_class": {
						Type:        genai.TypeString,
						Description: "Cabin class: ECONOMY, PREMIUM_ECONOMY, BUSINESS, or FIRST.",
						Enum:        []string{"ECONOMY", "PREMIUM_ECONOMY", "BUSINESS", "FIRST"},
					},
				},
				Required: []string{"origin", "destination", "departure_date"},
			},
		}},
	}
}

// -------------------------------------------------------------------
// 4. Agentic Execution Loop
// -------------------------------------------------------------------

func runAgent(ctx context.Context, client *genai.Client, contents *[]*genai.Content, systemPrompt string) error {
	maxIterations := 5
	temperature := float32(0.1)
	tools := []*genai.Tool{getFlightSearchToolDefinition()}
	config := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemPrompt, ""),
		Temperature:       &temperature,
		Tools:             tools,
	}

	for i := 0; i < maxIterations; i++ {
		resp, err := client.Models.GenerateContent(ctx, modelName(), *contents, config)
		if err != nil {
			return fmt.Errorf("failed to get completion: %w", err)
		}
		if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
			return fmt.Errorf("Gemini returned no candidate response")
		}

		modelContent := resp.Candidates[0].Content
		*contents = append(*contents, modelContent)
		functionCalls := resp.FunctionCalls()
		if len(functionCalls) == 0 {
			fmt.Printf("\nAgent: %s\n", resp.Text())
			return nil
		}

		for _, functionCall := range functionCalls {
			if functionCall.Name == "search_flights" {
				var params FlightSearchParams
				arguments, err := json.Marshal(functionCall.Args)
				if err != nil {
					return fmt.Errorf("failed to encode function arguments: %w", err)
				}
				if err := json.Unmarshal(arguments, &params); err != nil {
					return fmt.Errorf("failed to unmarshal parameters: %w", err)
				}

				// Apply defaults
				if params.Passengers == 0 {
					params.Passengers = 1
				}
				if params.CabinClass == "" {
					params.CabinClass = "ECONOMY"
				}

				// Execute tool
				offers, err := SearchFlights(ctx, params)
				if err != nil {
					return fmt.Errorf("tool execution failed: %w", err)
				}

				*contents = append(*contents, genai.NewContentFromFunctionResponse(
					functionCall.Name,
					map[string]any{"offers": offers},
					genai.RoleUser,
				))
			}
		}
	}

	return fmt.Errorf("agent exceeded max iterations without completing task")
}

func modelName() string {
	if model := os.Getenv("GEMINI_MODEL"); model != "" {
		return model
	}
	return "gemini-3.5-flash-lite"
}

func main() {
	if os.Getenv("WEB_MODE") == "1" {
		if err := runWebServer(); err != nil {
			log.Fatal(err)
		}
		return
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		log.Fatal("Please set the GEMINI_API_KEY environment variable")
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{APIKey: apiKey})
	if err != nil {
		log.Fatalf("Failed to create Gemini client: %v", err)
	}
	bookingStore := NewInMemoryBookingStore()
	activeUserID := os.Getenv("USER_ID")
	if activeUserID == "" {
		activeUserID = "user_001"
	}

	// Base prompt seeding the agent's identity and operational constraints
	systemPrompt := `You are an expert AI Flight Concierge.
Your job is to assist passengers in planning, searching, and booking flights.
Guidelines:
1. Identify airport IATA codes accurately (e.g., San Francisco -> SFO, London Heathrow -> LHR).
2. Always verify that departure dates and locations are known before triggering tools.
3. Once flight options are received, present them cleanly with pricing, durations, departure times, and stops.
4. Show all flight prices in Indian rupees (INR), using the price_inr value from the tool.
	5. Keep the conversation context. Users may ask follow-up questions about the offers you just presented.
	6. When a message contains an origin, destination, departure date, passenger count, and cabin class, call search_flights immediately.
	7. Keep answers concise, factual, and customer-friendly.`
	systemPrompt += "\n\nRelevant booking profile for the active user:\n" + bookingStore.ProfileSummary(activeUserID)

	reader := bufio.NewReader(os.Stdin)
	conversation := make([]*genai.Content, 0, 16)
	fmt.Println("Ask for a flight or follow up on the current results. Type 'exit' to quit.")
	for {
		fmt.Print("\nYou: ")
		userRequest, readErr := reader.ReadString('\n')
		if readErr != nil && len(userRequest) == 0 {
			if readErr == io.EOF {
				fmt.Println("\nGoodbye.")
				return
			}
			log.Printf("Failed to read your request: %v", readErr)
			continue
		}

		userRequest = strings.TrimSpace(userRequest)
		if strings.EqualFold(userRequest, "exit") || strings.EqualFold(userRequest, "quit") {
			fmt.Println("Goodbye.")
			return
		}
		if userRequest == "" {
			fmt.Println("Please enter a flight request.")
			continue
		}

		conversation = append(conversation, genai.NewContentFromText(userRequest, genai.RoleUser))
		fmt.Printf("User: %s\n", userRequest)
		requestContext, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		runErr := runAgent(requestContext, client, &conversation, systemPrompt)
		cancel()
		if runErr != nil {
			log.Printf("Agent run failed: %v", runErr)
		}
	}
}
