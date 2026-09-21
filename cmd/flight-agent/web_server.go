package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
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
}

func runWebServer() error {
	app := newWebApp()
	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handlePage)
	mux.HandleFunc("/login", app.handleLogin)
	mux.HandleFunc("/logout", app.handleLogout)
	mux.HandleFunc("/api/me", app.requireLogin(app.handleMe))
	mux.HandleFunc("/api/search", app.requireLogin(app.handleSearch))

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
	return &webApp{
		bookings: NewInMemoryBookingStore(),
		sessions: &webSessionStore{sessions: make(map[string]webUser)},
		users:    users,
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
	var params FlightSearchParams
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid search request")
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
		writeJSONError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"offers": offers})
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
*{box-sizing:border-box}body{margin:0;background:radial-gradient(circle at 80% 0%,#f1d6bd 0,transparent 32%),linear-gradient(135deg,#f7f5ef,#edf3ef);color:var(--ink);font:15px/1.5 Georgia,serif;min-height:100vh}button,input,select{font:inherit}button{cursor:pointer}.shell{max-width:1180px;margin:auto;padding:30px 22px 50px}.topbar{display:flex;align-items:center;justify-content:space-between;margin-bottom:44px}.brand{font-size:22px;font-weight:bold;letter-spacing:.02em}.brand span{color:var(--orange)}.eyebrow{font:11px/1.2 Arial,sans-serif;letter-spacing:.16em;text-transform:uppercase;color:var(--teal);font-weight:bold}.login-wrap{max-width:430px;margin:8vh auto}.login-card,.panel{background:rgba(255,253,248,.88);border:1px solid var(--line);box-shadow:var(--shadow);border-radius:8px}.login-card{padding:38px}.login-card h1{font-size:42px;line-height:1.05;margin:10px 0 14px}.sub{color:var(--muted);margin:0 0 25px}.field{display:grid;gap:7px;margin:16px 0}.field label{font:12px Arial,sans-serif;text-transform:uppercase;letter-spacing:.08em;color:var(--muted)}input,select{width:100%;padding:12px 13px;border:1px solid #d6d1c6;border-radius:5px;background:#fff}.primary{border:0;border-radius:5px;background:var(--teal);color:white;padding:13px 18px;font-weight:bold}.primary:hover{background:var(--teal-dark)}.error{color:#a9392b;margin-top:12px}.demo{background:#edf4ef;padding:12px;border-radius:5px;color:#45665d;font:13px Arial,sans-serif;margin-top:18px}.app{display:none}.hero{display:flex;justify-content:space-between;gap:24px;align-items:end;margin-bottom:25px}.hero h1{font-size:50px;line-height:1.02;margin:9px 0}.welcome{color:var(--muted)}.logout{border:1px solid var(--line);background:transparent;border-radius:5px;padding:10px 14px;color:var(--ink)}.grid{display:grid;grid-template-columns:1.4fr .8fr;gap:20px}.panel{padding:24px}.panel h2{font-size:23px;margin:0 0 18px}.search-grid{display:grid;grid-template-columns:1fr 1fr;gap:12px}.search-grid .wide{grid-column:1/-1}.search-actions{display:flex;align-items:center;gap:15px;margin-top:18px}.status{color:var(--muted);font:13px Arial,sans-serif}.profile-list{display:grid;gap:13px;margin:0}.profile-list div{border-bottom:1px solid var(--line);padding-bottom:10px}.profile-list dt{font:11px Arial,sans-serif;text-transform:uppercase;letter-spacing:.08em;color:var(--muted)}.profile-list dd{margin:3px 0 0;font-size:17px}.bookings{margin-top:20px}.booking{padding:12px 0;border-bottom:1px solid var(--line)}.booking strong{display:block}.booking small{color:var(--muted);font-family:Arial,sans-serif}.results{display:none;margin-top:20px}.result{padding:17px 0;border-top:1px solid var(--line);display:grid;grid-template-columns:1fr auto;gap:10px}.result h3{margin:0 0 3px;font-size:19px}.result p{margin:2px 0;color:var(--muted);font-family:Arial,sans-serif;font-size:13px}.price{font-size:21px;color:var(--teal);font-weight:bold;white-space:nowrap}.empty{color:var(--muted)}@media(max-width:800px){.grid{grid-template-columns:1fr}.hero{align-items:start;flex-direction:column}.hero h1{font-size:40px}.search-grid{grid-template-columns:1fr}.search-grid .wide{grid-column:auto}}
</style>
</head>
<body>
<div id="loginView" class="login-wrap"><div class="login-card"><div class="eyebrow">Skyline / personal flight desk</div><h1>Travel with a little more memory.</h1><p class="sub">Search live fares and keep your preferences close at hand.</p><form id="loginForm"><div class="field"><label>Email</label><input id="email" type="email" value="alice@example.com" required></div><div class="field"><label>Password</label><input id="password" type="password" value="demo" required></div><button class="primary" type="submit">Log in</button><div id="loginError" class="error"></div></form><div class="demo">Demo accounts: alice@example.com, ben@example.com, carla@example.com, dev@example.com<br>Password: demo</div></div></div>
<div id="appView" class="app"><div class="shell"><div class="topbar"><div class="brand">flight<span>concierge</span></div><button id="logoutButton" class="logout">Log out</button></div><div class="hero"><div><div class="eyebrow">Your personal flight desk</div><h1 id="greeting">Good to see you.</h1><div id="emailLabel" class="welcome"></div></div></div><div class="grid"><main><section class="panel"><h2>Find your next flight</h2><form id="searchForm"><div class="search-grid"><div class="field"><label>From</label><input id="origin" placeholder="Delhi or DEL" required></div><div class="field"><label>To</label><input id="destination" placeholder="Port Blair or IXZ" required></div><div class="field"><label>Departure</label><input id="departureDate" type="date" required></div><div class="field"><label>Passengers</label><input id="passengers" type="number" min="1" value="1" required></div><div class="field wide"><label>Cabin</label><select id="cabin"><option>ECONOMY</option><option>PREMIUM_ECONOMY</option><option>BUSINESS</option><option>FIRST</option></select></div></div><div class="search-actions"><button class="primary" type="submit">Search live fares</button><span id="searchStatus" class="status"></span></div></form><div id="results" class="results"><h2>Available flights</h2><div id="resultList"></div></div></section></main><aside><section class="panel"><h2>Your travel profile</h2><dl id="profile" class="profile-list"></dl></section><section class="panel bookings"><h2>Recent bookings</h2><div id="bookings"></div></section></aside></div></div></div>
<script>
const $=id=>document.getElementById(id);const loginView=$('loginView'),appView=$('appView');
async function request(url,options={}){const r=await fetch(url,{headers:{'Content-Type':'application/json',...(options.headers||{})},...options});const data=await r.json().catch(()=>({}));if(!r.ok)throw new Error(data.error||'Request failed');return data}
function showApp(data){loginView.style.display='none';appView.style.display='block';$('greeting').textContent='Welcome back, '+data.user.name.split(' ')[0]+'.';$('emailLabel').textContent=data.user.email;const p=data.profile;$('profile').innerHTML=[['Focus',p.budget_vs_comfort],['Preferred airlines',p.preferred_airlines.join(', ')],['Usual departure',p.preferred_time_of_day],['Cabin',p.preferred_cabin_class],['Average spend','₹'+p.average_booking_price_inr.toLocaleString('en-IN',{maximumFractionDigits:0})],['Routes',p.recent_routes.join(', ')]].map(x=>'<div><dt>'+x[0]+'</dt><dd>'+x[1]+'</dd></div>').join('');$('bookings').innerHTML=data.bookings.map(b=>'<div class="booking"><strong>'+b.airline+' · '+b.origin+' → '+b.destination+'</strong><small>'+b.cabin_class+' · ₹'+b.price_inr.toLocaleString('en-IN')+'</small></div>').join('')||'<div class="empty">No recent bookings yet.</div>'}
async function loadProfile(){try{showApp(await request('/api/me'))}catch(e){loginView.style.display='block';appView.style.display='none'}}
$('loginForm').addEventListener('submit',async e=>{e.preventDefault();$('loginError').textContent='';try{await request('/login',{method:'POST',body:JSON.stringify({email:$('email').value,password:$('password').value})});await loadProfile()}catch(e){$('loginError').textContent=e.message}});
$('logoutButton').addEventListener('click',async()=>{await request('/logout',{method:'POST'});loginView.style.display='block';appView.style.display='none'});
$('searchForm').addEventListener('submit',async e=>{e.preventDefault();$('searchStatus').textContent='Searching Duffel...';$('results').style.display='none';try{const d=await request('/api/search',{method:'POST',body:JSON.stringify({origin:$('origin').value,destination:$('destination').value,departure_date:$('departureDate').value,passengers:Number($('passengers').value),cabin_class:$('cabin').value})});$('resultList').innerHTML=d.offers.map(o=>'<article class="result"><div><h3>'+o.airline+' · '+o.flight_number+'</h3><p>'+o.origin+' → '+o.destination+' · '+o.departure_time+' to '+o.arrival_time+'</p><p>'+o.duration+' · '+(o.stops===0?'Non-stop':o.stops+' stop(s)')+'</p></div><div class="price">₹'+o.price_inr.toLocaleString('en-IN',{maximumFractionDigits:2})+'</div></article>').join('');$('results').style.display='block';$('searchStatus').textContent=d.offers.length+' fares found'}catch(e){$('searchStatus').textContent=e.message}});
loadProfile();
</script></body></html>`
