package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	openai "github.com/sashabaranov/go-openai"
	"github.com/sashabaranov/go-openai/jsonschema"
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
	PriceUSD  float64 `json:"price_usd"`
	Stops     int     `json:"stops"`
}

// -------------------------------------------------------------------
// 2. Flight Service Tool (Mocking Duffel / Amadeus)
// -------------------------------------------------------------------

// SearchFlights executes the flight search against external APIs.
func SearchFlights(params FlightSearchParams) ([]FlightOffer, error) {
	// In production, invoke Amadeus or Duffel HTTP endpoints here.
	fmt.Printf("\n[TOOL EXECUTION] Searching flights: %s -> %s on %s (Class: %s, Pax: %d)...\n",
		params.Origin, params.Destination, params.DepartureDate, params.CabinClass, params.Passengers)

	// Simulated response
	return []FlightOffer{
		{
			ID:        "fl_9281",
			Airline:   "United Airlines",
			FlightNum: "UA 412",
			Origin:    params.Origin,
			Dest:      params.Destination,
			DepTime:   fmt.Sprintf("%sT08:30:00Z", params.DepartureDate),
			ArrTime:   fmt.Sprintf("%sT11:45:00Z", params.DepartureDate),
			Duration:  "6h 15m",
			PriceUSD:  349.50,
			Stops:     0,
		},
		{
			ID:        "fl_7104",
			Airline:   "Delta Air Lines",
			FlightNum: "DL 890",
			Origin:    params.Origin,
			Dest:      params.Destination,
			DepTime:   fmt.Sprintf("%sT14:15:00Z", params.DepartureDate),
			ArrTime:   fmt.Sprintf("%sT17:40:00Z", params.DepartureDate),
			Duration:  "6h 25m",
			PriceUSD:  312.00,
			Stops:     0,
		},
	}, nil
}

// -------------------------------------------------------------------
// 3. Tool Definition for the LLM
// -------------------------------------------------------------------

func getFlightSearchToolDefinition() openai.Tool {
	return openai.Tool{
		Type: openai.ToolTypeFunction,
		Function: &openai.FunctionDefinition{
			Name:        "search_flights",
			Description: "Search for available one-way or round-trip airline flights based on route and date constraints.",
			Parameters: jsonschema.Definition{
				Type: jsonschema.Object,
				Properties: map[string]jsonschema.Definition{
					"origin": {
						Type:        jsonschema.String,
						Description: "The 3-letter IATA origin airport code (e.g., SFO, JFK, LHR).",
					},
					"destination": {
						Type:        jsonschema.String,
						Description: "The 3-letter IATA destination airport code (e.g., LHR, CDG, NRT).",
					},
					"departure_date": {
						Type:        jsonschema.String,
						Description: "Date of departure in YYYY-MM-DD format.",
					},
					"passengers": {
						Type:        jsonschema.Integer,
						Description: "Number of passengers travelling. Defaults to 1.",
					},
					"cabin_class": {
						Type:        jsonschema.String,
						Description: "Cabin class: ECONOMY, PREMIUM_ECONOMY, BUSINESS, or FIRST.",
						Enum:        []string{"ECONOMY", "PREMIUM_ECONOMY", "BUSINESS", "FIRST"},
					},
				},
				Required: []string{"origin", "destination", "departure_date"},
			},
		},
	}
}

// -------------------------------------------------------------------
// 4. Agentic Execution Loop
// -------------------------------------------------------------------

func runAgent(ctx context.Context, client *openai.Client, messages []openai.ChatCompletionMessage) error {
	tools := []openai.Tool{getFlightSearchToolDefinition()}
	maxIterations := 5

	for i := 0; i < maxIterations; i++ {
		req := openai.ChatCompletionRequest{
			Model:       openai.GPT4o,
			Messages:    messages,
			Tools:       tools,
			Temperature: 0.1,
		}

		resp, err := client.CreateChatCompletion(ctx, req)
		if err != nil {
			return fmt.Errorf("failed to get completion: %w", err)
		}

		choice := resp.Choices[0]
		messages = append(messages, choice.Message)

		// If no tools are requested, the agent has generated its final response
		if len(choice.Message.ToolCalls) == 0 {
			fmt.Printf("\nAgent: %s\n", choice.Message.Content)
			return nil
		}

		// Process requested tool calls
		for _, toolCall := range choice.Message.ToolCalls {
			if toolCall.Function.Name == "search_flights" {
				var params FlightSearchParams
				if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &params); err != nil {
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
				offers, err := SearchFlights(params)
				if err != nil {
					return fmt.Errorf("tool execution failed: %w", err)
				}

				offersJSON, _ := json.Marshal(offers)

				// Append tool result message back to dialogue state
				messages = append(messages, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    string(offersJSON),
					ToolCallID: toolCall.ID,
				})
			}
		}
	}

	return fmt.Errorf("agent exceeded max iterations without completing task")
}

func main() {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		log.Fatal("Please set the OPENAI_API_KEY environment variable")
	}

	client := openai.NewClient(apiKey)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Base prompt seeding the agent's identity and operational constraints
	systemPrompt := `You are an expert AI Flight Concierge.
Your job is to assist passengers in planning, searching, and booking flights.
Guidelines:
1. Identify airport IATA codes accurately (e.g., San Francisco -> SFO, London Heathrow -> LHR).
2. Always verify that departure dates and locations are known before triggering tools.
3. Once flight options are received, present them cleanly with pricing, durations, departure times, and stops.
4. Keep answers concise, factual, and customer-friendly.`

	dialogue := []openai.ChatCompletionMessage{
		{
			Role:    openai.ChatMessageRoleSystem,
			Content: systemPrompt,
		},
		{
			Role:    openai.ChatMessageRoleUser,
			Content: "Hi, I need a one-way economy flight for 1 person from San Francisco to London on November 15, 2026.",
		},
	}

	fmt.Println("User: Hi, I need a one-way economy flight for 1 person from San Francisco to London on November 15, 2026.")
	if err := runAgent(ctx, client, dialogue); err != nil {
		log.Fatalf("Agent run failed: %v", err)
	}
}
