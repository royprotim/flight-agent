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
	"os"
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
}

// FlightOffer represents a flight option returned by the provider API.
type FlightOffer struct {
	ID        string  `json:"id"`
	Airline   string  `json:"airline"`
	FlightNum string  `json:"flight_number"`
	Origin    string  `json:"origin"`
	Dest      string  `json:"destination"`
	DepTime   string  `json:"departure_time"`
	ArrTime   string  `json:"arrival_time"`
	Duration  string  `json:"duration"`
	PriceINR  float64 `json:"price_inr"`
	Stops     int     `json:"stops"`
}

// -------------------------------------------------------------------
// 2. Flight Service Tool (Duffel)
// -------------------------------------------------------------------

type duffelOfferRequest struct {
	Data duffelOfferRequestData `json:"data"`
}

type duffelOfferRequestData struct {
	Slices     []duffelSliceRequest `json:"slices"`
	Passengers []duffelPassenger    `json:"passengers"`
	CabinClass string               `json:"cabin_class"`
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

	requestBody := duffelOfferRequest{Data: duffelOfferRequestData{
		Slices: []duffelSliceRequest{{
			Origin:        strings.ToUpper(params.Origin),
			Destination:   strings.ToUpper(params.Destination),
			DepartureDate: params.DepartureDate,
		}},
		Passengers: makePassengers(params.Passengers),
		CabinClass: strings.ToLower(params.CabinClass),
	}}
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
	return FlightOffer{
		ID:        offer.ID,
		Airline:   airline,
		FlightNum: first.MarketingCarrierFlightNumber,
		Origin:    first.DepartingAirport.IATACode,
		Dest:      last.ArrivingAirport.IATACode,
		DepTime:   first.DepartingAt,
		ArrTime:   last.ArrivingAt,
		Duration:  slice.Duration,
		PriceINR:  priceINR,
		Stops:     len(slice.Segments) - 1,
	}, nil
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
	return "gemini-3.6-flash"
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
