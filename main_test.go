package main

import (
	"context"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"
)

func testTemplates(t *testing.T) *template.Template {
	t.Helper()
	const templates = `
{{define "home.html"}}home {{.Message}}{{end}}
{{define "poll.html"}}poll {{.Poll.Title}} {{.Error}}{{end}}
{{define "results.html"}}results {{.Poll.Title}} {{.Error}}{{end}}
{{define "stats.html"}}stats {{.PollCount}} {{.ResponseCount}}{{end}}
`
	tmpl, err := template.New("").Funcs(templateFuncs).Parse(templates)
	if err != nil {
		t.Fatalf("failed to parse templates: %v", err)
	}
	return tmpl
}

func newTestApp(t *testing.T) (*App, *MemoryStorage) {
	t.Helper()
	storage := &MemoryStorage{
		polls:     make(map[string]Poll),
		responses: make(map[string][]Response),
	}
	app := &App{
		storage:   storage,
		templates: testTemplates(t),
		baseURL:   "",
	}
	return app, storage
}

func newFormRequest(method, target string, form url.Values) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

func TestNormalizeDays(t *testing.T) {
	input := []string{"2024-01-02", "", "2024-01-01", "2024-01-02", " 2024-01-03 "}
	got := normalizeDays(input)
	want := []string{"2024-01-01", "2024-01-02", "2024-01-03"}
	if !equalDays(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func TestFilterDiffMergeDays(t *testing.T) {
	selected := []string{"2024-01-01", "2024-01-02", "2024-01-03"}
	allowed := []string{"2024-01-01", "2024-01-03"}
	filtered := filterDays(selected, allowed)
	wantFiltered := []string{"2024-01-01", "2024-01-03"}
	if !equalDays(filtered, wantFiltered) {
		t.Fatalf("expected %v, got %v", wantFiltered, filtered)
	}

	added := diffDays([]string{"2024-01-01"}, []string{"2024-01-01", "2024-01-02"})
	if !equalDays(added, []string{"2024-01-02"}) {
		t.Fatalf("expected added day, got %v", added)
	}

	merged := mergeDays([]string{"2024-01-02"}, []string{"2024-01-01", "2024-01-02"})
	if !equalDays(merged, []string{"2024-01-01", "2024-01-02"}) {
		t.Fatalf("expected merged days, got %v", merged)
	}
}

func TestParseVenuesFromForm(t *testing.T) {
	existing := map[string]Venue{
		"existing-id": {ID: "existing-id", Title: "Old"},
	}

	venues, err := parseVenuesFromForm(
		[]string{"existing-id", ""},
		[]string{"Updated title", "Bowling"},
		[]string{"https://example.com/updated", ""},
		[]string{"New description", "Team lane"},
		existing,
	)
	if err != nil {
		t.Fatalf("parse venues: %v", err)
	}
	if len(venues) != 2 {
		t.Fatalf("expected 2 venues, got %d", len(venues))
	}
	if venues[0].ID != "existing-id" {
		t.Fatalf("expected existing id kept, got %s", venues[0].ID)
	}
	if venues[1].ID == "" {
		t.Fatalf("expected generated id for new venue")
	}
	if venues[1].Title != "Bowling" {
		t.Fatalf("unexpected title: %s", venues[1].Title)
	}
}

func TestParseVenuesFromFormRequiresTitle(t *testing.T) {
	_, err := parseVenuesFromForm(
		nil,
		[]string{""},
		[]string{"https://example.com"},
		nil,
		nil,
	)
	if err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestAddVenueWriteIn(t *testing.T) {
	existing := []Venue{{ID: "park", Title: "Park"}}
	updated, venueID, err := addVenueWriteIn(existing, " Arcade ", " https://example.com ", " Games ")
	if err != nil {
		t.Fatalf("add write-in: %v", err)
	}
	if len(updated) != 2 {
		t.Fatalf("expected new venue, got %+v", updated)
	}
	if venueID == "" || venueID == "park" {
		t.Fatalf("expected generated venue id, got %q", venueID)
	}
	if updated[1].Title != "Arcade" || updated[1].URL != "https://example.com" || updated[1].Description != "Games" {
		t.Fatalf("expected trimmed venue fields, got %+v", updated[1])
	}

	updated, venueID, err = addVenueWriteIn(updated, "park", "", "")
	if err != nil {
		t.Fatalf("dedupe write-in: %v", err)
	}
	if len(updated) != 2 || venueID != "park" {
		t.Fatalf("expected existing venue reused, got id=%q venues=%+v", venueID, updated)
	}
}

func TestAddVenueWriteInRequiresTitle(t *testing.T) {
	_, _, err := addVenueWriteIn(nil, "", "https://example.com", "")
	if err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestParsePollPath(t *testing.T) {
	cases := []struct {
		path      string
		pollID    string
		userToken string
	}{
		{path: "/poll/abc", pollID: "abc"},
		{path: "/poll/abc/u/xyz", pollID: "abc", userToken: "xyz"},
		{path: "/poll/abc/u", pollID: "abc"},
		{path: "/other", pollID: ""},
	}

	for _, tc := range cases {
		pollID, token := parsePollPath(tc.path)
		if pollID != tc.pollID || token != tc.userToken {
			t.Fatalf("path %s expected %s/%s got %s/%s", tc.path, tc.pollID, tc.userToken, pollID, token)
		}
	}
}

func TestSummarizeAvailability(t *testing.T) {
	days := []string{"2024-01-01", "2024-01-02"}
	responses := []Response{
		{Name: "B", Days: []string{"2024-01-01", "2024-01-02"}},
		{Name: "A", Days: []string{"2024-01-01"}},
	}
	summaries := summarizeAvailability(days, responses)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 summaries, got %d", len(summaries))
	}
	if !summaries[0].AllAvailable {
		t.Fatalf("expected day 1 to be all available")
	}
	if summaries[1].AllAvailable {
		t.Fatalf("expected day 2 not all available")
	}
	if strings.Join(summaries[0].Names, ",") != "A,B" {
		t.Fatalf("expected sorted names, got %v", summaries[0].Names)
	}
}

func TestSummarizeVenueVotes(t *testing.T) {
	venues := []Venue{
		{ID: "park", Title: "Park"},
		{ID: "coffee", Title: "Coffee"},
	}
	responses := []Response{
		{Name: "B", VenueVotes: []string{"park"}},
		{Name: "A", VenueVotes: []string{"park", "coffee"}},
	}
	summaries := summarizeVenueVotes(venues, responses)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 venue summaries, got %d", len(summaries))
	}
	if summaries[0].Venue.ID != "park" || summaries[0].VoteCount != 2 {
		t.Fatalf("expected park to lead, got %+v", summaries[0])
	}
	if strings.Join(summaries[0].Names, ",") != "A,B" {
		t.Fatalf("expected sorted voter names, got %v", summaries[0].Names)
	}
}

func TestUpcomingDaysFrom(t *testing.T) {
	start := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	options := upcomingDaysFrom(start, 3)
	if len(options) != 3 {
		t.Fatalf("expected 3 options, got %d", len(options))
	}
	if options[0].Date != "2024-02-01" || options[2].Date != "2024-02-03" {
		t.Fatalf("unexpected dates: %+v", options)
	}
}

func TestMemoryStorageCRUD(t *testing.T) {
	storage := &MemoryStorage{
		polls:     make(map[string]Poll),
		responses: make(map[string][]Response),
	}
	poll := Poll{ID: "poll-1", Title: "Title", Days: []string{"2024-01-01"}, CreatorToken: "creator", CreatedAt: time.Now()}
	if err := storage.CreatePoll(context.Background(), poll); err != nil {
		t.Fatalf("create poll: %v", err)
	}

	response := Response{ID: "resp-1", Name: "Alex", Days: []string{"2024-01-01"}, UserToken: "token", CreatedAt: time.Now()}
	if err := storage.AddResponse(context.Background(), poll.ID, response); err != nil {
		t.Fatalf("add response: %v", err)
	}

	loadedPoll, responses, err := storage.GetPoll(context.Background(), poll.ID)
	if err != nil {
		t.Fatalf("get poll: %v", err)
	}
	if loadedPoll.Title != poll.Title || len(responses) != 1 {
		t.Fatalf("expected poll and response")
	}

	if err := storage.UpdatePollDays(context.Background(), poll.ID, []string{"2024-01-01", "2024-01-02"}); err != nil {
		t.Fatalf("update days: %v", err)
	}

	if err := storage.DeleteResponse(context.Background(), poll.ID, response.ID); err != nil {
		t.Fatalf("delete response: %v", err)
	}
	_, responses, _ = storage.GetPoll(context.Background(), poll.ID)
	if len(responses) != 0 {
		t.Fatalf("expected responses deleted")
	}

	stats, err := storage.GetStats(context.Background())
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.PollCount != 1 || stats.ResponseCount != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestHandleHome(t *testing.T) {
	app, _ := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/?invalid=1", nil)
	w := httptest.NewRecorder()
	app.handleHome(w, req)
	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "That link was invalid") {
		t.Fatalf("expected invalid message")
	}
}

func TestHandleCreatePollValidation(t *testing.T) {
	app, _ := newTestApp(t)
	req := newFormRequest(http.MethodPost, "/polls", url.Values{})
	w := httptest.NewRecorder()
	app.handleCreatePoll(w, req)
	if w.Result().StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid form")
	}
}

func TestHandleCreatePollSuccess(t *testing.T) {
	app, storage := newTestApp(t)
	form := url.Values{}
	form.Set("title", "Dinner")
	form.Set("creator", "Sam")
	form.Add("days", "2024-01-02")
	form.Add("days", "2024-01-01")
	req := newFormRequest(http.MethodPost, "/polls", form)
	w := httptest.NewRecorder()
	app.handleCreatePoll(w, req)
	res := w.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", res.StatusCode)
	}
	location := res.Header.Get("Location")
	if !strings.Contains(location, "/poll/") || !strings.Contains(location, "/u/") {
		t.Fatalf("unexpected redirect location: %s", location)
	}
	if len(storage.polls) != 1 {
		t.Fatalf("expected 1 poll stored")
	}
	var poll Poll
	for _, stored := range storage.polls {
		poll = stored
	}
	if poll.Title != "Dinner" {
		t.Fatalf("expected poll title stored")
	}
	responses := storage.responses[poll.ID]
	if len(responses) != 1 {
		t.Fatalf("expected creator response stored")
	}
	if responses[0].UserToken != poll.CreatorToken {
		t.Fatalf("expected creator token to match")
	}
	if !equalDays(responses[0].Days, []string{"2024-01-01", "2024-01-02"}) {
		t.Fatalf("unexpected days: %v", responses[0].Days)
	}
}

func TestHandleCreatePollWithVenues(t *testing.T) {
	app, storage := newTestApp(t)
	form := url.Values{}
	form.Set("title", "Dinner")
	form.Set("creator", "Sam")
	form.Add("days", "2024-01-01")
	form.Add("venue_title", "Sushi place")
	form.Add("venue_url", "https://example.com/sushi")
	form.Add("venue_description", "Close to downtown")
	req := newFormRequest(http.MethodPost, "/polls", form)
	w := httptest.NewRecorder()
	app.handleCreatePoll(w, req)
	if w.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", w.Result().StatusCode)
	}
	var poll Poll
	for _, stored := range storage.polls {
		poll = stored
	}
	if len(poll.Venues) != 1 {
		t.Fatalf("expected one venue option, got %d", len(poll.Venues))
	}
	if poll.Venues[0].Title != "Sushi place" {
		t.Fatalf("unexpected venue title: %s", poll.Venues[0].Title)
	}
}

func TestHandlePollGetRedirect(t *testing.T) {
	app, storage := newTestApp(t)
	poll := Poll{ID: "poll-1", Title: "Hang", Days: []string{"2024-01-01"}, CreatorToken: "creator"}
	storage.polls[poll.ID] = poll
	req := httptest.NewRequest(http.MethodGet, "/poll/"+poll.ID, nil)
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	res := w.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", res.StatusCode)
	}
	if !strings.Contains(res.Header.Get("Location"), "/poll/"+poll.ID+"/u/") {
		t.Fatalf("unexpected location: %s", res.Header.Get("Location"))
	}
}

func TestHandlePollGetNotFound(t *testing.T) {
	app, _ := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/poll/missing/u/token", nil)
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	if w.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", w.Result().StatusCode)
	}
	if w.Result().Header.Get("Location") != "/?invalid=1" {
		t.Fatalf("expected invalid redirect")
	}
}

func TestHandlePollPostHTMXValidation(t *testing.T) {
	app, storage := newTestApp(t)
	poll := Poll{ID: "poll-1", Title: "Hang", Days: []string{"2024-01-01"}, CreatorToken: "creator"}
	storage.polls[poll.ID] = poll
	storage.responses[poll.ID] = []Response{{ID: "resp-1", Name: "Creator", Days: poll.Days, UserToken: poll.CreatorToken}}
	form := url.Values{}
	form.Set("name", "")
	form.Add("days", "2024-01-01")
	req := newFormRequest(http.MethodPost, "/poll/"+poll.ID+"/u/"+poll.CreatorToken, form)
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	res := w.Result()
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "results") {
		t.Fatalf("expected results template")
	}
}

func TestHandlePollPostAddResponse(t *testing.T) {
	app, storage := newTestApp(t)
	poll := Poll{
		ID:           "poll-1",
		Title:        "Hang",
		Days:         []string{"2024-01-01", "2024-01-02"},
		Venues:       []Venue{{ID: "park", Title: "Park"}, {ID: "movie", Title: "Movie"}},
		CreatorToken: "creator",
	}
	storage.polls[poll.ID] = poll
	storage.responses[poll.ID] = []Response{{ID: "resp-1", Name: "Creator", Days: poll.Days, UserToken: poll.CreatorToken}}
	form := url.Values{}
	form.Set("name", "Jamie")
	form.Add("days", "2024-01-02")
	form.Add("venues", "movie")
	req := newFormRequest(http.MethodPost, "/poll/"+poll.ID+"/u/user-token", form)
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Result().StatusCode)
	}
	responses := storage.responses[poll.ID]
	if len(responses) != 2 {
		t.Fatalf("expected response saved")
	}
	var names []string
	for _, response := range responses {
		names = append(names, response.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != "Creator,Jamie" {
		t.Fatalf("unexpected responses: %v", names)
	}
	var jamie Response
	for _, response := range responses {
		if response.Name == "Jamie" {
			jamie = response
			break
		}
	}
	if !equalDays(jamie.VenueVotes, []string{"movie"}) {
		t.Fatalf("unexpected venue votes: %v", jamie.VenueVotes)
	}
}

func TestHandlePollPostWriteInVenue(t *testing.T) {
	app, storage := newTestApp(t)
	poll := Poll{
		ID:           "poll-1",
		Title:        "Hang",
		Days:         []string{"2024-01-01"},
		CreatorToken: "creator",
	}
	storage.polls[poll.ID] = poll
	storage.responses[poll.ID] = []Response{{ID: "resp-1", Name: "Creator", Days: poll.Days, UserToken: poll.CreatorToken}}
	form := url.Values{}
	form.Set("name", "Jamie")
	form.Add("days", "2024-01-01")
	form.Set("write_in_venue_title", "Arcade")
	form.Set("write_in_venue_url", "https://example.com/arcade")
	form.Set("write_in_venue_description", "Open late")
	req := newFormRequest(http.MethodPost, "/poll/"+poll.ID+"/u/user-token", form)
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Result().StatusCode)
	}
	updatedPoll := storage.polls[poll.ID]
	if len(updatedPoll.Venues) != 1 {
		t.Fatalf("expected write-in venue stored, got %+v", updatedPoll.Venues)
	}
	venue := updatedPoll.Venues[0]
	if venue.Title != "Arcade" || venue.URL != "https://example.com/arcade" || venue.Description != "Open late" {
		t.Fatalf("unexpected write-in venue: %+v", venue)
	}
	var jamie Response
	for _, response := range storage.responses[poll.ID] {
		if response.Name == "Jamie" {
			jamie = response
			break
		}
	}
	if !equalDays(jamie.VenueVotes, []string{venue.ID}) {
		t.Fatalf("expected write-in venue vote, got %v", jamie.VenueVotes)
	}
}

func TestHandlePollPostWriteInVenueRequiresTitle(t *testing.T) {
	app, storage := newTestApp(t)
	poll := Poll{
		ID:           "poll-1",
		Title:        "Hang",
		Days:         []string{"2024-01-01"},
		CreatorToken: "creator",
	}
	storage.polls[poll.ID] = poll
	form := url.Values{}
	form.Set("name", "Jamie")
	form.Add("days", "2024-01-01")
	form.Set("write_in_venue_url", "https://example.com/arcade")
	req := newFormRequest(http.MethodPost, "/poll/"+poll.ID+"/u/user-token", form)
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	if w.Result().StatusCode != http.StatusOK {
		t.Fatalf("expected rendered validation response, got %d", w.Result().StatusCode)
	}
	if len(storage.polls[poll.ID].Venues) != 0 {
		t.Fatalf("expected no venue stored")
	}
	if len(storage.responses[poll.ID]) != 0 {
		t.Fatalf("expected no response stored")
	}
}

func TestHandlePollPostUpdateDates(t *testing.T) {
	app, storage := newTestApp(t)
	poll := Poll{ID: "poll-1", Title: "Hang", Days: []string{"2024-01-01"}, CreatorToken: "creator"}
	storage.polls[poll.ID] = poll
	storage.responses[poll.ID] = []Response{{ID: "resp-1", Name: "Creator", Days: []string{"2024-01-01"}, UserToken: poll.CreatorToken}}
	form := url.Values{}
	form.Set("action", "update-dates")
	form.Add("days", "2024-01-01")
	form.Add("days", "2024-01-02")
	req := newFormRequest(http.MethodPost, "/poll/"+poll.ID+"/u/"+poll.CreatorToken, form)
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	res := w.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", res.StatusCode)
	}
	updated := storage.polls[poll.ID]
	if !equalDays(updated.Days, []string{"2024-01-01", "2024-01-02"}) {
		t.Fatalf("expected updated days, got %v", updated.Days)
	}
	responses := storage.responses[poll.ID]
	if len(responses) != 1 {
		t.Fatalf("expected creator response")
	}
	if !equalDays(responses[0].Days, []string{"2024-01-01", "2024-01-02"}) {
		t.Fatalf("expected creator auto-marked, got %v", responses[0].Days)
	}
}

func TestHandlePollPostUpdateVenuesFiltersVotes(t *testing.T) {
	app, storage := newTestApp(t)
	poll := Poll{
		ID:           "poll-1",
		Title:        "Hang",
		Days:         []string{"2024-01-01"},
		Venues:       []Venue{{ID: "park", Title: "Park"}, {ID: "movie", Title: "Movie"}},
		CreatorToken: "creator",
	}
	storage.polls[poll.ID] = poll
	storage.responses[poll.ID] = []Response{
		{ID: "resp-1", Name: "Creator", Days: []string{"2024-01-01"}, VenueVotes: []string{"park"}, UserToken: poll.CreatorToken},
		{ID: "resp-2", Name: "Sam", Days: []string{"2024-01-01"}, VenueVotes: []string{"park", "movie"}, UserToken: "user"},
	}
	form := url.Values{}
	form.Set("action", "update-venues")
	form.Add("venue_id", "movie")
	form.Add("venue_title", "Movie")
	form.Add("venue_url", "")
	form.Add("venue_description", "")
	req := newFormRequest(http.MethodPost, "/poll/"+poll.ID+"/u/"+poll.CreatorToken, form)
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	if w.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", w.Result().StatusCode)
	}
	updated := storage.polls[poll.ID]
	if len(updated.Venues) != 1 || updated.Venues[0].ID != "movie" {
		t.Fatalf("expected only movie venue, got %+v", updated.Venues)
	}
	for _, response := range storage.responses[poll.ID] {
		if response.Name == "Creator" && len(response.VenueVotes) != 0 {
			t.Fatalf("expected creator votes cleared, got %v", response.VenueVotes)
		}
		if response.Name == "Sam" && !equalDays(response.VenueVotes, []string{"movie"}) {
			t.Fatalf("expected sam votes filtered, got %v", response.VenueVotes)
		}
	}
}

func TestHandlePollPostDeleteResponse(t *testing.T) {
	app, storage := newTestApp(t)
	poll := Poll{ID: "poll-1", Title: "Hang", Days: []string{"2024-01-01"}, CreatorToken: "creator"}
	storage.polls[poll.ID] = poll
	storage.responses[poll.ID] = []Response{{ID: "resp-1", Name: "Creator", Days: []string{"2024-01-01"}, UserToken: poll.CreatorToken}}
	form := url.Values{}
	form.Set("action", "delete-response")
	form.Set("response_id", "resp-1")
	req := newFormRequest(http.MethodPost, "/poll/"+poll.ID+"/u/"+poll.CreatorToken, form)
	w := httptest.NewRecorder()
	app.handlePoll(w, req)
	if w.Result().StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", w.Result().StatusCode)
	}
	if len(storage.responses[poll.ID]) != 0 {
		t.Fatalf("expected response deleted")
	}
}

func TestHandlePollPostDuplicatePoll(t *testing.T) {
	app, storage := newTestApp(t)
	original := Poll{
		ID:           "poll-1",
		Title:        "Hang",
		Days:         []string{"2024-01-01", "2024-01-02"},
		Venues:       []Venue{{ID: "park", Title: "Park", URL: "https://example.com/park", Description: "Picnic tables"}},
		CreatorToken: "creator",
	}
	storage.polls[original.ID] = original
	storage.responses[original.ID] = []Response{
		{ID: "resp-1", Name: "Creator", Days: original.Days, UserToken: original.CreatorToken},
		{ID: "resp-2", Name: "Sam", Days: []string{"2024-01-02"}, UserToken: "user"},
	}

	form := url.Values{}
	form.Set("action", "duplicate-poll")
	req := newFormRequest(http.MethodPost, "/poll/"+original.ID+"/u/"+original.CreatorToken, form)
	w := httptest.NewRecorder()

	app.handlePoll(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d", res.StatusCode)
	}
	location := res.Header.Get("Location")
	if !strings.Contains(location, "/poll/") || !strings.Contains(location, "/u/") {
		t.Fatalf("unexpected redirect location: %s", location)
	}

	if len(storage.polls) != 2 {
		t.Fatalf("expected 2 polls after duplication, got %d", len(storage.polls))
	}

	var duplicated Poll
	for _, poll := range storage.polls {
		if poll.ID != original.ID {
			duplicated = poll
			break
		}
	}
	if duplicated.ID == "" {
		t.Fatalf("expected duplicated poll to be stored")
	}
	if duplicated.Title != original.Title {
		t.Fatalf("expected title copied, got %q", duplicated.Title)
	}
	if len(duplicated.Days) != 0 {
		t.Fatalf("expected duplicated poll to start with no dates, got %v", duplicated.Days)
	}
	if duplicated.CreatorToken == "" || duplicated.CreatorToken == original.CreatorToken {
		t.Fatalf("expected a fresh creator token, got %q", duplicated.CreatorToken)
	}
	if len(duplicated.Venues) != 1 || duplicated.Venues[0] != original.Venues[0] {
		t.Fatalf("expected venues copied, got %+v", duplicated.Venues)
	}
	if len(storage.responses[duplicated.ID]) != 0 {
		t.Fatalf("expected duplicated poll to have no responses")
	}
	if !strings.Contains(location, "/poll/"+duplicated.ID+"/u/"+duplicated.CreatorToken) {
		t.Fatalf("expected redirect to duplicated poll, got %s", location)
	}
}

func TestHandleStats(t *testing.T) {
	app, storage := newTestApp(t)
	storage.polls["poll-1"] = Poll{ID: "poll-1"}
	storage.responses["poll-1"] = []Response{{ID: "resp-1"}}
	req := httptest.NewRequest(http.MethodGet, "/admin/stats", nil)
	w := httptest.NewRecorder()
	app.handleStats(w, req)
	res := w.Result()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "stats") {
		t.Fatalf("expected stats template")
	}
}
