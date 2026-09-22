package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"
)

type webUser struct {
	ID       string
	Name     string
	Email    string
	Password string
}

type webSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]webUser
}

type webApp struct {
	bookings *InMemoryBookingStore
	sessions *webSessionStore
	users    map[string]webUser
	gemini   *genai.Client
}

func runWebServer() error {
	app := newWebApp()
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handlePage)
	mux.HandleFunc("/login", app.handleLogin)
	mux.HandleFunc("/logout", app.handleLogout)
	mux.HandleFunc("/api/me", app.requireLogin(app.handleMe))
	mux.HandleFunc("/api/search", app.requireLogin(app.handleSearch))
	mux.HandleFunc("/api/book", app.requireLogin(app.handleBook))

	address := os.Getenv("WEB_ADDR")
	if address == "" {
		address = ":8080"
	}
	fmt.Printf("Flight Concierge UI running at http://localhost%s\n", address)
	return http.ListenAndServe(address, mux)
}

func newWebApp() *webApp {
	users := map[string]webUser{
		"alice@example.com": {ID: "user_001", Name: "Aarav Sharma", Email: "alice@example.com", Password: "demo"},
		"ben@example.com":   {ID: "user_002", Name: "Ben Thomas", Email: "ben@example.com", Password: "demo"},
		"carla@example.com": {ID: "user_003", Name: "Carla Mendes", Email: "carla@example.com", Password: "demo"},
		"dev@example.com":   {ID: "user_004", Name: "Devika Rao", Email: "dev@example.com", Password: "demo"},
	}
	var gemini *genai.Client
	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		gemini, _ = genai.NewClient(context.Background(), &genai.ClientConfig{APIKey: key})
	}
	return &webApp{
		bookings: NewInMemoryBookingStore(),
		sessions: &webSessionStore{sessions: make(map[string]webUser)},
		users:    users,
		gemini:   gemini,
	}
}

func (app *webApp) handlePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(webPage))
}

func (app *webApp) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid login request")
		return
	}
	user, ok := app.users[strings.ToLower(strings.TrimSpace(input.Email))]
	if !ok || input.Password != user.Password {
		writeJSONError(w, http.StatusUnauthorized, "invalid email or password")
		return
	}

	sessionID, err := newSessionID()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	app.sessions.mu.Lock()
	app.sessions.sessions[sessionID] = user
	app.sessions.mu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     "flight_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"name": user.Name})
}

func (app *webApp) handleLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("flight_session"); err == nil {
		app.sessions.mu.Lock()
		delete(app.sessions.sessions, cookie.Value)
		app.sessions.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: "flight_session", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

func (app *webApp) handleMe(w http.ResponseWriter, r *http.Request) {
	user, _ := app.currentUser(r)
	profile, err := app.bookings.Profile(user.ID)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"user":     userResponse{ID: user.ID, Name: user.Name, Email: user.Email},
		"profile":  profile,
		"bookings": app.bookings.RecentBookings(user.ID, 5),
	})
}

type userResponse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func (app *webApp) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	user, _ := app.currentUser(r)
	var params FlightSearchParams
	var input struct {
		FlightSearchParams
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid search request")
		return
	}
	params = input.FlightSearchParams
	query := strings.TrimSpace(input.Query)
	if query == "" {
		query = BuildFlightQuery(params)
	}
	var err error
	params, err = extractFlightSearchParams(r.Context(), app.gemini, query)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if params.Passengers < 1 {
		params.Passengers = 1
	}
	if params.CabinClass == "" {
		params.CabinClass = "ECONOMY"
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	offers, err := SearchFlights(ctx, params)
	if err != nil {
		var noMatch *layoverNoMatchError
		if errors.As(err, &noMatch) {
			writeJSON(w, http.StatusOK, map[string]any{
				"query":       query,
				"offers":      []FlightOffer{},
				"explanation": noMatch.explanation,
			})
			return
		}
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	if profile, profileErr := app.bookings.Profile(user.ID); profileErr == nil {
		personalizeOffers(offers, profile)
	}
	writeJSON(w, http.StatusOK, map[string]any{"query": query, "offers": offers})
}

func sortOffersByPreferredAirlines(offers []FlightOffer, preferredAirlines []string) {
	rank := make(map[string]int, len(preferredAirlines))
	for index, airline := range preferredAirlines {
		rank[strings.ToLower(strings.TrimSpace(airline))] = index
	}
	sort.SliceStable(offers, func(i, j int) bool {
		rankI, preferredI := rank[strings.ToLower(strings.TrimSpace(offers[i].Airline))]
		rankJ, preferredJ := rank[strings.ToLower(strings.TrimSpace(offers[j].Airline))]
		if preferredI != preferredJ {
			return preferredI
		}
		if preferredI {
			return rankI < rankJ
		}
		return false
	})
}

func personalizeOffers(offers []FlightOffer, profile UserProfile) {
	preferredAirlines := make(map[string]bool, len(profile.PreferredAirlines))
	scores := make(map[string]int, len(offers))
	for _, airline := range profile.PreferredAirlines {
		preferredAirlines[strings.ToLower(strings.TrimSpace(airline))] = true
	}
	ratings := rateOffers(offers, profile)
	for index := range offers {
		offer := &offers[index]
		offer.Rating = ratings[index]
		score := 0
		reasons := make([]string, 0, 3)
		if preferredAirlines[strings.ToLower(strings.TrimSpace(offer.Airline))] {
			score += 100
			reasons = append(reasons, "preferred airline")
		}
		if profile.BudgetVsComfort == "budget" && offer.PriceINR <= profile.AverageBookingPriceINR {
			score += 25
			reasons = append(reasons, "within usual budget")
		}
		if profile.BudgetVsComfort == "comfort" && offer.Stops == 0 {
			score += 25
			reasons = append(reasons, "non-stop comfort")
		}
		if score > 0 {
			offer.Recommended = true
			offer.PreferenceReasons = reasons
		}
		scores[offer.ID] = score
	}
	sort.SliceStable(offers, func(i, j int) bool {
		return scores[offers[i].ID] > scores[offers[j].ID]
	})
}

func rateOffer(offer FlightOffer, profile UserProfile) *FlightRating {
	return rateOffers([]FlightOffer{offer}, profile)[0]
}

func rateOffers(offers []FlightOffer, profile UserProfile) []*FlightRating {
	if len(offers) == 0 {
		return nil
	}
	minPrice, maxPrice := offers[0].PriceINR, offers[0].PriceINR
	minDuration, maxDuration := offerDurationMinutes(offers[0]), offerDurationMinutes(offers[0])
	for _, offer := range offers[1:] {
		price, duration := offer.PriceINR, offerDurationMinutes(offer)
		minPrice, maxPrice = minFloat(minPrice, price), maxFloat(maxPrice, price)
		minDuration, maxDuration = minFloat(minDuration, duration), maxFloat(maxDuration, duration)
	}
	ratings := make([]*FlightRating, len(offers))
	for index, offer := range offers {
		priceScore := relativeScore(offer.PriceINR, minPrice, maxPrice, true)
		delayScore := relativeScore(offerDurationMinutes(offer), minDuration, maxDuration, true)
		if offer.Stops > 0 {
			delayScore = maxInt(0, delayScore-offer.Stops*15)
		}
		reasons := []string{"historical reliability data is unavailable from the provider response"}
		if offer.PriceINR == minPrice {
			reasons = append(reasons, "lowest price in this result set")
		}
		if offer.Stops == 0 {
			reasons = append(reasons, "non-stop itinerary lowers delay risk")
		}
		reliabilityScore := 50
		overall := int(float64(priceScore)*0.4 + float64(delayScore)*0.35 + float64(reliabilityScore)*0.25)
		ratings[index] = &FlightRating{Overall: overall, PriceSensitivity: priceScore, Delay: delayScore, Reliability: reliabilityScore, ReliabilityAvailable: false, Reasons: reasons}
	}
	return ratings
}

func offerDurationMinutes(offer FlightOffer) float64 {
	match := regexp.MustCompile(`(?i)^PT(?:(\d+)H)?(?:(\d+)M)?`).FindStringSubmatch(offer.Duration)
	if len(match) == 0 {
		return 0
	}
	hours, _ := strconv.ParseFloat(match[1], 64)
	minutes, _ := strconv.ParseFloat(match[2], 64)
	return hours*60 + minutes
}

func relativeScore(value, minimum, maximum float64, invert bool) int {
	if maximum == minimum {
		return 50
	}
	score := (value - minimum) / (maximum - minimum) * 100
	if invert {
		score = 100 - score
	}
	return int(score + 0.5)
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (app *webApp) handleBook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	user, _ := app.currentUser(r)
	var input struct {
		Offer         FlightOffer `json:"offer"`
		PassengerName string      `json:"passenger_name"`
		ContactEmail  string      `json:"contact_email"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Offer.ID == "" || strings.TrimSpace(input.PassengerName) == "" || strings.TrimSpace(input.ContactEmail) == "" {
		writeJSONError(w, http.StatusBadRequest, "offer, passenger name, and contact email are required")
		return
	}
	if strings.TrimSpace(input.Offer.Origin) == "" || strings.TrimSpace(input.Offer.Dest) == "" {
		writeJSONError(w, http.StatusBadRequest, "selected offer is missing origin or destination")
		return
	}
	booking := Booking{
		ID:            "pending_" + input.Offer.ID,
		UserID:        user.ID,
		Airline:       input.Offer.Airline,
		Origin:        input.Offer.Origin,
		Destination:   input.Offer.Dest,
		DepartureTime: parseBookingTime(input.Offer.DepTime),
		CabinClass:    "",
		PriceINR:      input.Offer.PriceINR,
		Stops:         input.Offer.Stops,
		BookedAt:      time.Now(),
		Status:        "pending_confirmation",
	}
	app.bookings.AddBooking(booking)
	writeJSON(w, http.StatusOK, map[string]any{
		"booking": booking,
		"message": "Booking details saved. Payment and final Duffel order confirmation are still required.",
	})
}

func parseBookingTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339, value)
	return parsed
}

func (app *webApp) requireLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := app.currentUser(r); !ok {
			writeJSONError(w, http.StatusUnauthorized, "please log in")
			return
		}
		next(w, r)
	}
}

func (app *webApp) currentUser(r *http.Request) (webUser, bool) {
	cookie, err := r.Cookie("flight_session")
	if err != nil {
		return webUser{}, false
	}
	app.sessions.mu.RLock()
	user, ok := app.sessions.sessions[cookie.Value]
	app.sessions.mu.RUnlock()
	return user, ok
}

func newSessionID() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

const webPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Flight Concierge</title>
<style>
:root{--ink:#17202a;--muted:#68737d;--paper:#f7f5ef;--panel:#fffdf8;--line:#e3ded2;--teal:#0d7770;--teal-dark:#07534f;--orange:#e86d39;--shadow:0 18px 45px rgba(46,42,32,.10)}
*{box-sizing:border-box}body{margin:0;background:radial-gradient(circle at 80% 0%,#f1d6bd 0,transparent 32%),linear-gradient(135deg,#f7f5ef,#edf3ef);color:var(--ink);font:15px/1.5 Georgia,serif;min-height:100vh}button,input,select,textarea{font:inherit}button{cursor:pointer}.shell{max-width:1180px;margin:auto;padding:30px 22px 50px}.topbar{display:flex;align-items:center;justify-content:space-between;margin-bottom:44px}.brand{font-size:22px;font-weight:bold;letter-spacing:.02em}.brand span{color:var(--orange)}.eyebrow{font:11px/1.2 Arial,sans-serif;letter-spacing:.16em;text-transform:uppercase;color:var(--teal);font-weight:bold}.login-wrap{max-width:430px;margin:8vh auto}.login-card,.panel{background:rgba(255,253,248,.88);border:1px solid var(--line);box-shadow:var(--shadow);border-radius:8px}.login-card{padding:38px}.login-card h1{font-size:42px;line-height:1.05;margin:10px 0 14px}.sub{color:var(--muted);margin:0 0 25px}.field{display:grid;gap:7px;margin:16px 0}.field label{font:12px Arial,sans-serif;text-transform:uppercase;letter-spacing:.08em;color:var(--muted)}input,select,textarea{width:100%;padding:12px 13px;border:1px solid #d6d1c6;border-radius:5px;background:#fff}textarea{min-height:82px;resize:vertical}.primary{border:0;border-radius:5px;background:var(--teal);color:white;padding:13px 18px;font-weight:bold}.primary:hover{background:var(--teal-dark)}.error{color:#a9392b;margin-top:12px}.demo{background:#edf4ef;padding:12px;border-radius:5px;color:#45665d;font:13px Arial,sans-serif;margin-top:18px}.app{display:none}.hero{display:flex;justify-content:space-between;gap:24px;align-items:end;margin-bottom:25px}.hero h1{font-size:50px;line-height:1.02;margin:9px 0}.welcome{color:var(--muted)}.logout{border:1px solid var(--line);background:transparent;border-radius:5px;padding:10px 14px;color:var(--ink)}.grid{display:grid;grid-template-columns:1.4fr .8fr;gap:20px}.panel{padding:24px}.panel h2{font-size:23px;margin:0 0 18px}.search-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.search-grid .wide{grid-column:1/-1}.search-actions{display:flex;align-items:center;gap:15px;margin-top:18px}.status{color:var(--muted);font:13px Arial,sans-serif}.profile-list{display:grid;gap:13px;margin:0}.profile-list div{border-bottom:1px solid var(--line);padding-bottom:10px}.profile-list dt{font:11px Arial,sans-serif;text-transform:uppercase;letter-spacing:.08em;color:var(--muted)}.profile-list dd{margin:3px 0 0;font-size:17px}.bookings{margin-top:20px}.booking{padding:12px 0;border-bottom:1px solid var(--line)}.booking strong{display:block}.booking small{color:var(--muted);font-family:Arial,sans-serif}.results{display:none;margin-top:20px}.result{padding:17px 0;border-top:1px solid var(--line);display:grid;grid-template-columns:1fr auto;gap:10px}.result h3{margin:0 0 3px;font-size:19px}.result p{margin:2px 0;color:var(--muted);font-family:Arial,sans-serif;font-size:13px}.price{font-size:21px;color:var(--teal);font-weight:bold;white-space:nowrap}.empty{color:var(--muted)}@media(max-width:800px){.grid{grid-template-columns:1fr}.hero{align-items:start;flex-direction:column}.hero h1{font-size:40px}.search-grid{grid-template-columns:1fr}.search-grid .wide{grid-column:auto}}
</style>
</head>
<body>
<div id="loginView" class="login-wrap"><div class="login-card"><div class="eyebrow">Skyline / personal flight desk</div><h1>Travel with a little more memory.</h1><p class="sub">Search live fares and keep your preferences close at hand.</p><form id="loginForm"><div class="field"><label>Email</label><input id="email" type="email" value="alice@example.com" required></div><div class="field"><label>Password</label><input id="password" type="password" value="demo" required></div><button class="primary" type="submit">Log in</button><div id="loginError" class="error"></div></form><div class="demo">Demo accounts: alice@example.com, ben@example.com, carla@example.com, dev@example.com<br>Password: demo</div></div></div>
<div id="appView" class="app"><div class="shell"><div class="topbar"><div class="brand">flight<span>concierge</span></div><button id="logoutButton" class="logout">Log out</button></div><div class="hero"><div><div class="eyebrow">Your personal flight desk</div><h1 id="greeting">Good to see you.</h1><div id="emailLabel" class="welcome"></div></div></div><div class="grid"><main><section class="panel"><h2>Find your next flight</h2><form id="searchForm"><div class="search-grid"><div class="field"><label>From</label><input id="origin" placeholder="Delhi or DEL" required></div><div class="field"><label>To</label><input id="destination" placeholder="Port Blair or IXZ" required></div><div class="field"><label>Departure</label><input id="departureDate" type="date" required></div><div class="field"><label>Passengers</label><input id="passengers" type="number" min="1" value="1" required></div><div class="field wide"><label>Cabin</label><select id="cabin"><option>ECONOMY</option><option>PREMIUM_ECONOMY</option><option>BUSINESS</option><option>FIRST</option></select></div></div><div class="search-actions"><button class="primary" type="submit">Search live fares</button><span id="searchStatus" class="status"></span></div></form><div id="results" class="results"><h2>Available flights</h2><div id="resultList"></div></div></section></main><aside><section class="panel"><h2>Your travel profile</h2><dl id="profile" class="profile-list"></dl></section><section class="panel bookings"><h2>Recent bookings</h2><div id="bookings"></div></section></aside></div></div></div>
<script>
const $=id=>document.getElementById(id);const loginView=$('loginView'),appView=$('appView');const preferenceField=document.createElement('div');preferenceField.className='field wide';preferenceField.innerHTML='<label>Additional preference</label><textarea id="preference" placeholder="Anything else you care about"></textarea>';document.querySelector('#searchForm .search-actions').before(preferenceField);const nativeFetch=window.fetch;window.fetch=(url,options={})=>{if(url==='/api/search'&&options.body){const body=JSON.parse(options.body);const preference=$('preference');body.preference=preference?preference.value.trim():'';if(preference)localStorage.setItem('flight_preference',body.preference);options.body=JSON.stringify(body)}return nativeFetch(url,options).then(response=>{if(url==='/api/search'&&response.ok){response.clone().json().then(data=>{setTimeout(()=>{if(data.explanation){const x=data.explanation;$('resultList').innerHTML='<article class="explanation"><h3>'+x.title+'</h3><p>'+x.reason+'</p><p>'+[x.inbound_description,x.outbound_description].filter(Boolean).join(' ')+'</p></article>';$('results').style.display='block';$('searchStatus').textContent='No matching flights found'}else{$('searchStatus').textContent=data.offers.length+' fares found. Query: '+data.query}},0)})}return response})};
async function request(url,options={}){const r=await fetch(url,{headers:{'Content-Type':'application/json',...(options.headers||{})},...options});const data=await r.json().catch(()=>({}));if(!r.ok)throw new Error(data.error||'Request failed');return data}
function showApp(data){loginView.style.display='none';appView.style.display='block';$('greeting').textContent='Welcome back, '+data.user.name.split(' ')[0]+'.';$('emailLabel').textContent=data.user.email;const p=data.profile;$('profile').innerHTML=[['Focus',p.budget_vs_comfort],['Preferred airlines',p.preferred_airlines.join(', ')],['Usual departure',p.preferred_time_of_day],['Cabin',p.preferred_cabin_class],['Average spend','₹'+p.average_booking_price_inr.toLocaleString('en-IN',{maximumFractionDigits:0})],['Routes',p.recent_routes.join(', ')]].map(x=>'<div><dt>'+x[0]+'</dt><dd>'+x[1]+'</dd></div>').join('');$('bookings').innerHTML=data.bookings.map(b=>'<div class="booking"><strong>'+b.airline+' · '+b.origin+' → '+b.destination+'</strong><small>'+b.cabin_class+' · ₹'+b.price_inr.toLocaleString('en-IN')+'</small></div>').join('')||'<div class="empty">No recent bookings yet.</div>'}
async function loadProfile(){try{const data=await request('/api/me');showApp(data);const preference=$('preference');if(preference)preference.value=localStorage.getItem('flight_preference')||''}catch(e){loginView.style.display='block';appView.style.display='none';$('loginError').textContent=e.message}}
$('loginForm').addEventListener('submit',async e=>{e.preventDefault();$('loginError').textContent='';try{await request('/login',{method:'POST',body:JSON.stringify({email:$('email').value,password:$('password').value})});await loadProfile()}catch(e){$('loginError').textContent=e.message}});
$('logoutButton').addEventListener('click',async()=>{await request('/logout',{method:'POST'});loginView.style.display='block';appView.style.display='none'});
if($('searchForm'))$('searchForm').addEventListener('submit',async e=>{e.preventDefault();$('searchStatus').textContent='Searching Duffel...';$('results').style.display='none';try{const d=await request('/api/search',{method:'POST',body:JSON.stringify({origin:$('origin').value,destination:$('destination').value,departure_date:$('departureDate').value,passengers:Number($('passengers').value),cabin_class:$('cabin').value})});$('resultList').innerHTML=d.offers.map(o=>'<article class="result"><div><h3>'+o.airline+' · '+o.flight_number+'</h3><p>'+o.origin+' → '+o.destination+' · '+o.departure_time+' to '+o.arrival_time+'</p><p>'+o.duration+' · '+(o.stops===0?'Non-stop':o.stops+' stop(s)')+'</p></div><div class="price">₹'+o.price_inr.toLocaleString('en-IN',{maximumFractionDigits:2})+'</div></article>').join('');$('results').style.display='block';$('searchStatus').textContent=d.offers.length+' fares found'}catch(e){$('searchStatus').textContent=e.message}});
function showBookingDetails(result){let modal=document.getElementById('bookingModal');if(!modal){modal=document.createElement('div');modal.id='bookingModal';modal.style='position:fixed;inset:0;background:rgba(23,32,42,.48);display:grid;place-items:center;padding:20px;z-index:10';document.body.appendChild(modal)}modal.innerHTML='<section class="panel" style="max-width:560px;width:100%;max-height:90vh;overflow:auto"><h2>Flight details</h2><p>'+result.innerText.replace(/\n/g,'<br>')+'</p><form id="bookingForm"><div class="field"><label>Passenger full name</label><input id="bookingName" required></div><div class="field"><label>Contact email</label><input id="bookingEmail" type="email" required></div><div class="search-actions"><button class="primary" type="submit">Continue to booking</button><button type="button" class="logout" id="closeBooking">Close</button></div><p id="bookingStatus" class="status"></p></form></section>';modal.style.display='grid';document.getElementById('closeBooking').onclick=()=>modal.remove();document.getElementById('bookingForm').onsubmit=e=>{e.preventDefault();document.getElementById('bookingStatus').textContent='Booking details captured. Connect Duffel Orders and payment before placing a live order.'}}
document.addEventListener('click',e=>{const result=e.target.closest('.result');if(result){result.style.cursor='pointer';showBookingDetails(result)}});
let currentOffers=[];const previousFetch=window.fetch;window.fetch=(url,options={})=>previousFetch(url,options).then(response=>{if(url==='/api/search'&&response.ok){response.clone().json().then(data=>{currentOffers=data.offers||[]})}return response});
function showBookingDetails(result){const index=Array.from(document.querySelectorAll('.result')).indexOf(result);const offer=currentOffers[index];if(!offer)return;let modal=document.getElementById('bookingModal');if(!modal){modal=document.createElement('div');modal.id='bookingModal';modal.style='position:fixed;inset:0;background:rgba(23,32,42,.48);display:grid;place-items:center;padding:20px;z-index:10';document.body.appendChild(modal)}modal.innerHTML='<section class="panel" style="max-width:560px;width:100%;max-height:90vh;overflow:auto"><h2>Flight details</h2><p><strong>'+offer.airline+' · '+offer.flight_number+'</strong><br>'+offer.origin+' → '+offer.destination+'<br>Departure: '+offer.departure_time+'<br>Arrival: '+offer.arrival_time+'<br>Duration: '+offer.duration+'<br>Stops: '+(offer.stops===0?'Non-stop':offer.stops+' stop(s)')+(offer.connection_airport?'<br>Connection: '+offer.connection_airport+' · '+offer.layover_hours.toFixed(1)+' hours':'')+'<br>Price: ₹'+offer.price_inr.toLocaleString('en-IN',{maximumFractionDigits:2})+'</p><form id="bookingForm"><div class="field"><label>Passenger full name</label><input id="bookingName" required></div><div class="field"><label>Contact email</label><input id="bookingEmail" type="email" required></div><div class="search-actions"><button class="primary" type="submit">Continue to booking</button><button type="button" class="logout" id="closeBooking">Close</button></div><p id="bookingStatus" class="status"></p></form></section>';modal.style.display='grid';document.getElementById('closeBooking').onclick=()=>modal.remove();document.getElementById('bookingForm').onsubmit=async e=>{e.preventDefault();const status=document.getElementById('bookingStatus');status.textContent='Saving booking...';try{const data=await request('/api/book',{method:'POST',body:JSON.stringify({offer,passenger_name:document.getElementById('bookingName').value,contact_email:document.getElementById('bookingEmail').value})});status.textContent=data.message;await loadProfile()}catch(error){status.textContent=error.message}}}
function showConnectionLabels(){document.querySelectorAll('.result').forEach((result,index)=>{const offer=currentOffers[index];if(!offer)return;if(offer.recommended&&!result.dataset.recommendedShown){const badge=document.createElement('span');badge.textContent='Recommended for you';badge.style='display:inline-block;background:#e86d39;color:white;padding:3px 8px;border-radius:4px;font:11px Arial,sans-serif;margin-bottom:6px';result.querySelector('div').prepend(badge);result.dataset.recommendedShown='true'}if(offer.rating&&!result.dataset.ratingShown){const line=document.createElement('p');line.textContent='Rating: '+offer.rating.overall+'/100 · Price '+offer.rating.price_sensitivity+'/100 · Delay '+offer.rating.delay+'/100 · Reliability '+offer.rating.reliability+'/100';result.querySelector('div').appendChild(line);result.dataset.ratingShown='true'}if(offer.connection_airport&&!result.dataset.connectionShown){const line=document.createElement('p');line.textContent='Connection: '+offer.connection_airport+' · '+Number(offer.layover_hours||0).toFixed(1)+' hours';result.querySelector('div').appendChild(line);result.dataset.connectionShown='true'}})}
new MutationObserver(()=>{showConnectionLabels();addOfferSortControl();setTimeout(showConnectionLabels,100)}).observe($('resultList'),{childList:true,subtree:true});
const previousShowBookingDetails=showBookingDetails;showBookingDetails=function(result){const index=Array.from(document.querySelectorAll('.result')).indexOf(result);const offer=currentOffers[index];if(!offer){return}const airports=offer.connection_airports&&offer.connection_airports.length?offer.connection_airports:(offer.connection_airport?[offer.connection_airport]:[]);previousShowBookingDetails(result);const modal=document.getElementById('bookingModal');if(modal&&airports.length){const details=modal.querySelector('p');if(details&&!details.innerHTML.includes('Connecting airport')){details.innerHTML+='<br>Connecting airport(s): '+airports.join(', ')}}};
let bookingChat={offer:null,step:'idle',name:'',email:''};function setupBookingChat(){const panel=document.createElement('section');panel.id='bookingChat';panel.className='panel';panel.style='margin-top:20px;display:block';panel.innerHTML='<h2>Booking assistant</h2><div id="chatMessages" style="display:grid;gap:8px;max-height:220px;overflow:auto;margin-bottom:12px"></div><form id="chatForm" style="display:flex;gap:8px"><input id="chatInput" style="flex:1" placeholder="Describe the flight you need" autocomplete="off" required><button class="primary" type="submit">Send</button></form>';document.querySelector('main').appendChild(panel);const addMessage=(text,who)=>{const message=document.createElement('div');message.textContent=(who==='bot'?'Assistant: ':'You: ')+text;message.style=who==='bot'?'background:#edf4ef;padding:9px;border-radius:5px':'background:#f3eee5;padding:9px;border-radius:5px';$('chatMessages').appendChild(message);$('chatMessages').scrollTop=$('chatMessages').scrollHeight};addMessage('Tell me the route, date, cabin, passengers, and any preferences.','bot');window.startBookingChat=index=>{const offer=currentOffers[index];if(!offer)return;bookingChat={offer,step:'name',name:'',email:''};$('chatMessages').innerHTML='';addMessage('I can help book '+offer.airline+' flight '+offer.flight_number+' from '+offer.origin+' to '+offer.destination+' for ₹'+offer.price_inr.toLocaleString('en-IN')+'. What is the passenger full name?','bot');$('chatInput').placeholder='Type your response';$('chatInput').focus()};$('chatForm').onsubmit=async event=>{event.preventDefault();const input=$('chatInput'),value=input.value.trim();if(!value)return;addMessage(value,'user');input.value='';if(bookingChat.step==='idle'){event.stopImmediatePropagation();await searchFromChat(value);return}if(bookingChat.step==='name'){bookingChat.name=value;bookingChat.step='email';addMessage('Thanks. What contact email should I use?','bot');return}if(bookingChat.step==='email'){if(!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value)){addMessage('Please enter a valid email address.','bot');return}bookingChat.email=value;bookingChat.step='confirm';addMessage('Please confirm: book this flight for '+bookingChat.name+' using '+bookingChat.email+'? Type yes or no.','bot');return}if(bookingChat.step==='confirm'){if(value.toLowerCase()!=='yes'&&value.toLowerCase()!=='y'){bookingChat.step='idle';addMessage('Booking cancelled. You can start a new flight search.','bot');return}bookingChat.step='saving';addMessage('Saving your booking details...','bot');try{const data=await request('/api/book',{method:'POST',body:JSON.stringify({offer:bookingChat.offer,passenger_name:bookingChat.name,contact_email:bookingChat.email})});addMessage(data.message,'bot');bookingChat.step='done';await loadProfile()}catch(error){bookingChat.step='confirm';addMessage(error.message,'bot')}}};}setupBookingChat();
document.querySelector('#searchForm')?.remove();let chatSearchBusy=false;document.querySelector('#chatForm').addEventListener('submit',async event=>{if(bookingChat.step!=='idle'||chatSearchBusy)return;event.preventDefault();const input=$('chatInput'),query=input.value.trim();if(!query)return;const message=document.createElement('div');message.textContent='You: '+query;message.style='background:#f3eee5;padding:9px;border-radius:5px';$('chatMessages').appendChild(message);input.value='';chatSearchBusy=true;const status=document.createElement('div');status.textContent='Assistant: Searching live fares...';status.style='background:#edf4ef;padding:9px;border-radius:5px';$('chatMessages').appendChild(status);try{const data=await request('/api/search',{method:'POST',body:JSON.stringify({query})});currentOffers=data.offers||[];if(data.explanation){status.textContent='Assistant: '+data.explanation.title+' '+data.explanation.reason}else{status.textContent='Assistant: I found '+currentOffers.length+' flight option(s). Select one to continue booking.';$('resultList').innerHTML=currentOffers.map(o=>'<article class="result"><div><h3>'+o.airline+' · '+o.flight_number+'</h3><p>'+o.origin+' → '+o.destination+' · '+o.departure_time+' to '+o.arrival_time+'</p><p>'+o.duration+' · '+(o.stops===0?'Non-stop':o.stops+' stop(s)')+(o.connection_airport?' · via '+o.connection_airport+' ('+Number(o.layover_hours||0).toFixed(1)+'h layover)':'')+'</p></div><div class="price">₹'+o.price_inr.toLocaleString('en-IN',{maximumFractionDigits:2})+'</div></article>').join('');$('results').style.display='block'}}catch(error){status.textContent='Assistant: '+error.message}finally{chatSearchBusy=false}});document.addEventListener('click',event=>{const result=event.target.closest('.result');if(result){const index=Array.from(document.querySelectorAll('.result')).indexOf(result);setTimeout(()=>window.startBookingChat(index),0)}});
async function searchFromChat(query){if(chatSearchBusy)return;chatSearchBusy=true;const status=document.createElement('div');status.textContent='Assistant: Searching live fares...';status.style='background:#edf4ef;padding:9px;border-radius:5px';$('chatMessages').appendChild(status);try{const data=await request('/api/search',{method:'POST',body:JSON.stringify({query})});currentOffers=data.offers||[];if(data.explanation){status.textContent='Assistant: '+data.explanation.title+' '+data.explanation.reason;return}status.textContent='Assistant: I found '+currentOffers.length+' flight option(s). Select one to continue booking.';$('resultList').innerHTML=currentOffers.map(o=>'<article class="result"><div><h3>'+o.airline+' · '+o.flight_number+'</h3><p>'+o.origin+' → '+o.destination+' · '+o.departure_time+' to '+o.arrival_time+'</p><p>'+o.duration+' · '+(o.stops===0?'Non-stop':o.stops+' stop(s)')+(o.connection_airport?' · via '+o.connection_airport+' ('+Number(o.layover_hours||0).toFixed(1)+'h layover)':'')+'</p></div><div class="price">₹'+o.price_inr.toLocaleString('en-IN',{maximumFractionDigits:2})+'</div></article>').join('');$('results').style.display='block'}catch(error){status.textContent='Assistant: '+error.message}finally{chatSearchBusy=false}}
function durationMinutes(value){const match=String(value).match(/PT(?:(\d+)H)?(?:(\d+)M)?/i);return match?Number(match[1]||0)*60+Number(match[2]||0):Number.MAX_SAFE_INTEGER}function addOfferSortControl(){const results=$('results');if(!results||$('sortOffers'))return;const field=document.createElement('div');field.className='field';field.innerHTML='<label>Sort results</label><select id="sortOffers"><option value="recommended">Recommended</option><option value="price">Lowest price</option><option value="duration">Shortest duration</option></select>';results.insertBefore(field,$('resultList'));$('sortOffers').onchange=()=>{const mode=$('sortOffers').value;const sorted=[...currentOffers];if(mode==='price')sorted.sort((a,b)=>a.price_inr-b.price_inr);if(mode==='duration')sorted.sort((a,b)=>durationMinutes(a.duration)-durationMinutes(b.duration));$('resultList').innerHTML=sorted.map(o=>'<article class="result"><div><h3>'+o.airline+' · '+o.flight_number+'</h3><p>'+o.origin+' → '+o.destination+' · '+o.departure_time+' to '+o.arrival_time+'</p><p>'+o.duration+' · '+(o.stops===0?'Non-stop':o.stops+' stop(s)')+(o.connection_airport?' · via '+o.connection_airport+' ('+Number(o.layover_hours||0).toFixed(1)+'h layover)':'')+'</p></div><div class="price">₹'+o.price_inr.toLocaleString('en-IN',{maximumFractionDigits:2})+'</div></article>').join('')}}
document.querySelector('#results').before(document.getElementById('bookingChat'));const safeShowBookingDetails=showBookingDetails;showBookingDetails=function(result){const index=Array.from(document.querySelectorAll('.result')).indexOf(result);if(currentOffers[index])currentOffers[index].layover_hours=Number(currentOffers[index].layover_hours||0);safeShowBookingDetails(result)};document.addEventListener('click',event=>{const result=event.target.closest('.result');if(!result||!currentOffers.length)return;const heading=result.querySelector('h3');if(!heading)return;const parts=heading.textContent.split('·').map(value=>value.trim());const selected=currentOffers.find(offer=>offer.airline===parts[0]&&offer.flight_number===parts[1]);if(!selected)return;const visibleOffers=[...document.querySelectorAll('.result')].map(card=>{const cardParts=card.querySelector('h3')?.textContent.split('·').map(value=>value.trim());return currentOffers.find(offer=>cardParts&&offer.airline===cardParts[0]&&offer.flight_number===cardParts[1])}).filter(Boolean);if(visibleOffers.length===document.querySelectorAll('.result').length)currentOffers=visibleOffers},true);loadProfile();
</script></body></html>`
