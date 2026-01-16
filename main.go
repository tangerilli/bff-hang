package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"embed"
	"html/template"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/awslabs/aws-lambda-go-api-proxy/httpadapter"
)

const (
	defaultTableName = "bff-hang"
)

type Storage interface {
	CreatePoll(ctx context.Context, poll Poll) error
	GetPoll(ctx context.Context, pollID string) (Poll, []Response, error)
	AddResponse(ctx context.Context, pollID string, response Response) error
	UpdatePollDays(ctx context.Context, pollID string, days []string) error
	DeleteResponse(ctx context.Context, pollID string, responseID string) error
}

type Poll struct {
	ID           string
	Title        string
	Days         []string
	CreatorToken string
	CreatedAt    time.Time
}

type Response struct {
	ID        string
	Name      string
	Days      []string
	UserToken string
	CreatedAt time.Time
}

type DayOption struct {
	Date  string
	Label string
}

type DaySummary struct {
	Date         string
	Label        string
	Names        []string
	AllAvailable bool
}

type PollView struct {
	Poll          Poll
	Responses     []Response
	Summaries     []DaySummary
	TotalResponse int
	Error         string
	ShareURL      string
	ViewerToken   string
	ViewerName    string
	SelectedDays  map[string]bool
	IsCreator     bool
	EditDays      []DayOption
	PollDaySet    map[string]bool
}

type DynamoDBStorage struct {
	client *dynamodb.Client
	Table  string
}

type PollItem struct {
	PK           string   `dynamodbav:"pk"`
	SK           string   `dynamodbav:"sk"`
	Type         string   `dynamodbav:"type"`
	ID           string   `dynamodbav:"id"`
	Title        string   `dynamodbav:"title"`
	Days         []string `dynamodbav:"days"`
	CreatorToken string   `dynamodbav:"creator_token"`
	CreatedAt    string   `dynamodbav:"created_at"`
}

type ResponseItem struct {
	PK        string   `dynamodbav:"pk"`
	SK        string   `dynamodbav:"sk"`
	Type      string   `dynamodbav:"type"`
	ID        string   `dynamodbav:"id"`
	Name      string   `dynamodbav:"name"`
	Days      []string `dynamodbav:"days"`
	UserToken string   `dynamodbav:"user_token"`
	CreatedAt string   `dynamodbav:"created_at"`
}

type MemoryStorage struct {
	polls     map[string]Poll
	responses map[string][]Response
}

type App struct {
	storage         Storage
	templates       *template.Template
	baseURL         string
	reloadTemplates bool
}

//go:embed templates/*.html
var embeddedTemplates embed.FS

func main() {
	storage, err := newStorage(context.Background())
	if err != nil {
		log.Fatalf("failed to initialize storage: %v", err)
	}

	templates, err := parseTemplates()
	if err != nil {
		log.Fatalf("failed to parse templates: %v", err)
	}

	app := &App{
		storage:         storage,
		templates:       templates,
		baseURL:         os.Getenv("APP_BASE_URL"),
		reloadTemplates: os.Getenv("DEV_RELOAD_TEMPLATES") == "true",
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleHome)
	mux.HandleFunc("/polls", app.handleCreatePoll)
	mux.HandleFunc("/poll/", app.handlePoll)

	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		adapter := httpadapter.NewV2(mux)
		lambda.Start(adapter.ProxyWithContext)
		return
	}

	addr := ":8080"
	log.Printf("starting server on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}

func newStorage(ctx context.Context) (Storage, error) {
	if os.Getenv("USE_MEMORY_STORE") == "true" {
		return &MemoryStorage{
			polls:     make(map[string]Poll),
			responses: make(map[string][]Response),
		}, nil
	}

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, err
	}

	client := dynamodb.NewFromConfig(cfg)
	table := os.Getenv("DYNAMODB_TABLE")
	if table == "" {
		table = defaultTableName
	}

	return &DynamoDBStorage{
		client: client,
		Table:  table,
	}, nil
}

func (s *DynamoDBStorage) CreatePoll(ctx context.Context, poll Poll) error {
	item := PollItem{
		PK:           pollPartitionKey(poll.ID),
		SK:           "POLL",
		Type:         "poll",
		ID:           poll.ID,
		Title:        poll.Title,
		Days:         poll.Days,
		CreatorToken: poll.CreatorToken,
		CreatedAt:    poll.CreatedAt.Format(time.RFC3339),
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           &s.Table,
		Item:                av,
		ConditionExpression: awsString("attribute_not_exists(pk)"),
	})
	return err
}

func (s *DynamoDBStorage) GetPoll(ctx context.Context, pollID string) (Poll, []Response, error) {
	pk := pollPartitionKey(pollID)
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              &s.Table,
		KeyConditionExpression: awsString("pk = :pk"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": &types.AttributeValueMemberS{Value: pk},
		},
	})
	if err != nil {
		return Poll{}, nil, err
	}
	if len(out.Items) == 0 {
		return Poll{}, nil, errNotFound
	}

	var poll Poll
	var responses []Response
	for _, item := range out.Items {
		var typeHolder struct {
			Type string `dynamodbav:"type"`
		}
		if err := attributevalue.UnmarshalMap(item, &typeHolder); err != nil {
			return Poll{}, nil, err
		}
		switch typeHolder.Type {
		case "poll":
			var pollItem PollItem
			if err := attributevalue.UnmarshalMap(item, &pollItem); err != nil {
				return Poll{}, nil, err
			}
			poll = Poll{
				ID:           pollItem.ID,
				Title:        pollItem.Title,
				Days:         pollItem.Days,
				CreatorToken: pollItem.CreatorToken,
				CreatedAt:    parseTime(pollItem.CreatedAt),
			}
		case "response":
			var respItem ResponseItem
			if err := attributevalue.UnmarshalMap(item, &respItem); err != nil {
				return Poll{}, nil, err
			}
			responses = append(responses, Response{
				ID:        respItem.ID,
				Name:      respItem.Name,
				Days:      respItem.Days,
				UserToken: respItem.UserToken,
				CreatedAt: parseTime(respItem.CreatedAt),
			})
		}
	}

	if poll.ID == "" {
		return Poll{}, nil, errNotFound
	}

	sort.Slice(responses, func(i, j int) bool {
		return responses[i].CreatedAt.Before(responses[j].CreatedAt)
	})

	return poll, responses, nil
}

func (s *DynamoDBStorage) AddResponse(ctx context.Context, pollID string, response Response) error {
	item := ResponseItem{
		PK:        pollPartitionKey(pollID),
		SK:        "RESP#" + response.ID,
		Type:      "response",
		ID:        response.ID,
		Name:      response.Name,
		Days:      response.Days,
		UserToken: response.UserToken,
		CreatedAt: response.CreatedAt.Format(time.RFC3339),
	}
	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return err
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: &s.Table,
		Item:      av,
	})
	return err
}

func (s *DynamoDBStorage) UpdatePollDays(ctx context.Context, pollID string, days []string) error {
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: &s.Table,
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pollPartitionKey(pollID)},
			"sk": &types.AttributeValueMemberS{Value: "POLL"},
		},
		UpdateExpression: awsString("SET days = :days"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":days": &types.AttributeValueMemberL{Value: stringSliceAttribute(days)},
		},
	})
	return err
}

func (s *DynamoDBStorage) DeleteResponse(ctx context.Context, pollID string, responseID string) error {
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: &s.Table,
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pollPartitionKey(pollID)},
			"sk": &types.AttributeValueMemberS{Value: "RESP#" + responseID},
		},
	})
	return err
}

func (s *MemoryStorage) CreatePoll(ctx context.Context, poll Poll) error {
	if _, exists := s.polls[poll.ID]; exists {
		return errConflict
	}
	s.polls[poll.ID] = poll
	return nil
}

func (s *MemoryStorage) GetPoll(ctx context.Context, pollID string) (Poll, []Response, error) {
	poll, ok := s.polls[pollID]
	if !ok {
		return Poll{}, nil, errNotFound
	}
	responses := append([]Response(nil), s.responses[pollID]...)
	sort.Slice(responses, func(i, j int) bool {
		return responses[i].CreatedAt.Before(responses[j].CreatedAt)
	})
	return poll, responses, nil
}

func (s *MemoryStorage) AddResponse(ctx context.Context, pollID string, response Response) error {
	if _, ok := s.polls[pollID]; !ok {
		return errNotFound
	}
	responses := s.responses[pollID]
	for i := range responses {
		if responses[i].ID == response.ID {
			responses[i] = response
			s.responses[pollID] = responses
			return nil
		}
	}
	s.responses[pollID] = append(responses, response)
	return nil
}

func (s *MemoryStorage) UpdatePollDays(ctx context.Context, pollID string, days []string) error {
	poll, ok := s.polls[pollID]
	if !ok {
		return errNotFound
	}
	poll.Days = days
	s.polls[pollID] = poll
	return nil
}

func (s *MemoryStorage) DeleteResponse(ctx context.Context, pollID string, responseID string) error {
	if _, ok := s.polls[pollID]; !ok {
		return errNotFound
	}
	responses := s.responses[pollID]
	for i := range responses {
		if responses[i].ID == responseID {
			s.responses[pollID] = append(responses[:i], responses[i+1:]...)
			return nil
		}
	}
	return nil
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data := struct {
		Upcoming []DayOption
		Message  string
	}{
		Upcoming: upcomingDays(14),
		Message:  homeMessage(r),
	}

	a.render(w, "home.html", data)
}

func (a *App) handleCreatePoll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	creator := strings.TrimSpace(r.FormValue("creator"))
	selectedDays := normalizeDays(r.Form["days"])
	if title == "" || creator == "" || len(selectedDays) == 0 {
		http.Error(w, "title, name, and at least one day are required", http.StatusBadRequest)
		return
	}

	creatorToken := randomID()
	poll := Poll{
		ID:           randomID(),
		Title:        title,
		Days:         selectedDays,
		CreatorToken: creatorToken,
		CreatedAt:    time.Now().UTC(),
	}

	if err := a.storage.CreatePoll(r.Context(), poll); err != nil {
		log.Printf("failed to create poll: %v", err)
		http.Error(w, "unable to create poll", http.StatusInternalServerError)
		return
	}

	creatorResponse := Response{
		ID:        randomID(),
		Name:      creator,
		Days:      selectedDays,
		UserToken: creatorToken,
		CreatedAt: time.Now().UTC(),
	}
	if err := a.storage.AddResponse(r.Context(), poll.ID, creatorResponse); err != nil {
		log.Printf("failed to add creator response: %v", err)
		http.Error(w, "unable to create poll", http.StatusInternalServerError)
		return
	}

	setUserTokenCookie(w, r, poll.ID, creatorToken)
	http.Redirect(w, r, fmt.Sprintf("/poll/%s/u/%s", poll.ID, creatorToken), http.StatusSeeOther)
}

func (a *App) handlePoll(w http.ResponseWriter, r *http.Request) {
	pollID, userToken := parsePollPath(r.URL.Path)
	if pollID == "" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		if userToken == "" {
			token := userTokenFromCookie(r, pollID)
			if token == "" {
				token = randomID()
				setUserTokenCookie(w, r, pollID, token)
			}
			http.Redirect(w, r, fmt.Sprintf("/poll/%s/u/%s", pollID, token), http.StatusSeeOther)
			return
		}

		setUserTokenCookie(w, r, pollID, userToken)
		poll, responses, err := a.storage.GetPoll(r.Context(), pollID)
		if err != nil {
			if errors.Is(err, errNotFound) {
				http.Redirect(w, r, "/?invalid=1", http.StatusSeeOther)
				return
			}
			log.Printf("failed to load poll: %v", err)
			http.Error(w, "unable to load poll", http.StatusInternalServerError)
			return
		}

		view := a.buildPollView(r, poll, responses, "", userToken)
		a.render(w, "poll.html", view)
	case http.MethodPost:
		if userToken == "" {
			http.Redirect(w, r, "/poll/"+pollID, http.StatusSeeOther)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		poll, responses, err := a.storage.GetPoll(r.Context(), pollID)
		if err != nil {
			if errors.Is(err, errNotFound) {
				http.Redirect(w, r, "/?invalid=1", http.StatusSeeOther)
				return
			}
			log.Printf("failed to load poll: %v", err)
			http.Error(w, "unable to load poll", http.StatusInternalServerError)
			return
		}

		if action := r.FormValue("action"); action != "" {
			if !isCreator(poll, userToken) {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			switch action {
			case "delete-response":
				responseID := strings.TrimSpace(r.FormValue("response_id"))
				if responseID == "" {
					http.Error(w, "missing response", http.StatusBadRequest)
					return
				}
				if err := a.storage.DeleteResponse(r.Context(), pollID, responseID); err != nil {
					log.Printf("failed to delete response: %v", err)
					http.Error(w, "unable to delete response", http.StatusInternalServerError)
					return
				}
				http.Redirect(w, r, fmt.Sprintf("/poll/%s/u/%s", pollID, userToken), http.StatusSeeOther)
				return
			case "update-dates":
				updatedDays := normalizeDays(r.Form["days"])
				if len(updatedDays) == 0 {
					http.Error(w, "at least one day is required", http.StatusBadRequest)
					return
				}
				if err := a.storage.UpdatePollDays(r.Context(), pollID, updatedDays); err != nil {
					log.Printf("failed to update poll days: %v", err)
					http.Error(w, "unable to update poll", http.StatusInternalServerError)
					return
				}
				for _, response := range responses {
					filtered := filterDays(response.Days, updatedDays)
					if !equalDays(response.Days, filtered) {
						response.Days = filtered
						if err := a.storage.AddResponse(r.Context(), pollID, response); err != nil {
							log.Printf("failed to update response days: %v", err)
							http.Error(w, "unable to update poll", http.StatusInternalServerError)
							return
						}
					}
				}
				http.Redirect(w, r, fmt.Sprintf("/poll/%s/u/%s", pollID, userToken), http.StatusSeeOther)
				return
			default:
				http.Error(w, "unknown action", http.StatusBadRequest)
				return
			}
		}

		name := strings.TrimSpace(r.FormValue("name"))
		selectedDays := filterDays(normalizeDays(r.Form["days"]), poll.Days)
		if name == "" || len(selectedDays) == 0 {
			view := a.buildPollView(r, poll, responses, "Please enter your name and at least one available day.", userToken)
			if isHTMX(r) {
				w.WriteHeader(http.StatusBadRequest)
				a.render(w, "results.html", view)
				return
			}
			a.render(w, "poll.html", view)
			return
		}

		response := Response{
			ID:        randomID(),
			Name:      name,
			Days:      selectedDays,
			UserToken: userToken,
			CreatedAt: time.Now().UTC(),
		}
		if existing := findResponseByToken(responses, userToken); existing != nil {
			response.ID = existing.ID
			response.CreatedAt = existing.CreatedAt
		}
		if err := a.storage.AddResponse(r.Context(), pollID, response); err != nil {
			log.Printf("failed to add response: %v", err)
			http.Error(w, "unable to save response", http.StatusInternalServerError)
			return
		}

		poll, responses, err = a.storage.GetPoll(r.Context(), pollID)
		if err != nil {
			log.Printf("failed to reload poll: %v", err)
			http.Error(w, "unable to load poll", http.StatusInternalServerError)
			return
		}
		view := a.buildPollView(r, poll, responses, "", userToken)
		if isHTMX(r) {
			a.render(w, "results.html", view)
			return
		}
		a.render(w, "poll.html", view)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) buildPollView(r *http.Request, poll Poll, responses []Response, errMsg string, viewerToken string) PollView {
	summaries := summarizeAvailability(poll.Days, responses)
	baseURL := a.baseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("%s://%s", schemeForRequest(r), r.Host)
	}
	selectedDays := make(map[string]bool)
	viewerName := ""
	pollDaySet := makeDaySet(poll.Days)
	if viewerToken != "" {
		if response := findResponseByToken(responses, viewerToken); response != nil {
			viewerName = response.Name
			for _, day := range response.Days {
				if pollDaySet[day] {
					selectedDays[day] = true
				}
			}
		}
	}

	return PollView{
		Poll:          poll,
		Responses:     responses,
		Summaries:     summaries,
		TotalResponse: len(responses),
		Error:         errMsg,
		ShareURL:      fmt.Sprintf("%s/poll/%s", strings.TrimRight(baseURL, "/"), poll.ID),
		ViewerToken:   viewerToken,
		ViewerName:    viewerName,
		SelectedDays:  selectedDays,
		IsCreator:     isCreator(poll, viewerToken),
		EditDays:      pollEditDays(poll.Days),
		PollDaySet:    pollDaySet,
	}
}

func (a *App) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	tmpl := a.templates
	if a.reloadTemplates {
		loaded, err := template.New("").Funcs(templateFuncs).ParseFS(os.DirFS("."), "templates/*.html")
		if err != nil {
			log.Printf("template reload failed: %v", err)
		} else {
			tmpl = loaded
		}
	}
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("template error: %v", err)
	}
}

func summarizeAvailability(days []string, responses []Response) []DaySummary {
	nameByDay := make(map[string][]string)
	for _, response := range responses {
		for _, day := range response.Days {
			nameByDay[day] = append(nameByDay[day], response.Name)
		}
	}

	var summaries []DaySummary
	for _, day := range days {
		names := append([]string(nil), nameByDay[day]...)
		sort.Strings(names)
		summaries = append(summaries, DaySummary{
			Date:         day,
			Label:        formatDate(day),
			Names:        names,
			AllAvailable: len(responses) > 0 && len(names) == len(responses),
		})
	}

	return summaries
}

func upcomingDays(count int) []DayOption {
	start := time.Now().UTC()
	return upcomingDaysFrom(start, count)
}

func upcomingDaysFrom(start time.Time, count int) []DayOption {
	start = startOfDayUTC(start)
	options := make([]DayOption, 0, count)
	for i := 0; i < count; i++ {
		day := start.AddDate(0, 0, i)
		options = append(options, DayOption{
			Date:  day.Format("2006-01-02"),
			Label: day.Format("Mon, Jan 2"),
		})
	}
	return options
}

func normalizeDays(input []string) []string {
	seen := make(map[string]struct{})
	var days []string
	for _, day := range input {
		day = strings.TrimSpace(day)
		if day == "" {
			continue
		}
		if _, ok := seen[day]; ok {
			continue
		}
		seen[day] = struct{}{}
		days = append(days, day)
	}
	sort.Strings(days)
	return days
}

var templateFuncs = template.FuncMap{
	"formatDate": formatDate,
}

func findResponseByToken(responses []Response, token string) *Response {
	target := strings.TrimSpace(token)
	if target == "" {
		return nil
	}
	for i := range responses {
		if strings.TrimSpace(responses[i].UserToken) == target {
			return &responses[i]
		}
	}
	return nil
}

func makeDaySet(days []string) map[string]bool {
	set := make(map[string]bool, len(days))
	for _, day := range days {
		set[day] = true
	}
	return set
}

func filterDays(selected []string, allowed []string) []string {
	if len(selected) == 0 {
		return selected
	}
	allowedSet := makeDaySet(allowed)
	filtered := make([]string, 0, len(selected))
	for _, day := range selected {
		if allowedSet[day] {
			filtered = append(filtered, day)
		}
	}
	return filtered
}

func equalDays(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func pollEditDays(days []string) []DayOption {
	start := startOfDayUTC(time.Now().UTC())
	maxDay := start
	for _, day := range days {
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil {
			continue
		}
		parsed = startOfDayUTC(parsed)
		if parsed.After(maxDay) {
			maxDay = parsed
		}
	}
	count := 14
	if maxDay.After(start.AddDate(0, 0, count-1)) {
		diff := int(maxDay.Sub(start).Hours()/24) + 1
		if diff > count {
			count = diff
		}
	}
	return upcomingDaysFrom(start, count)
}

func startOfDayUTC(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, time.UTC)
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return strings.TrimRight(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf), "=")
}

func formatDate(date string) string {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return parsed.Format("Mon, Jan 2")
}

func parseTemplates() (*template.Template, error) {
	return template.New("").Funcs(templateFuncs).ParseFS(embeddedTemplates, "templates/*.html")
}

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Now().UTC()
	}
	return parsed
}

func homeMessage(r *http.Request) string {
	if r.URL.Query().Get("invalid") == "1" {
		return "That link was invalid. Start a new poll below."
	}
	return ""
}

func parsePollPath(path string) (string, string) {
	trimmed := strings.TrimPrefix(path, "/poll/")
	if trimmed == "" || trimmed == path {
		return "", ""
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) == 1 {
		return parts[0], ""
	}
	if len(parts) >= 3 && parts[1] == "u" {
		return parts[0], parts[2]
	}
	return parts[0], ""
}

func pollPartitionKey(id string) string {
	return "POLL#" + id
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
}

func isCreator(poll Poll, token string) bool {
	return poll.CreatorToken != "" && poll.CreatorToken == token
}

func schemeForRequest(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		return proto
	}
	return "http"
}

func pollCookieName(pollID string) string {
	return "bffhang_" + pollID
}

func userTokenFromCookie(r *http.Request, pollID string) string {
	cookie, err := r.Cookie(pollCookieName(pollID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func setUserTokenCookie(w http.ResponseWriter, r *http.Request, pollID string, token string) {
	if token == "" {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     pollCookieName(pollID),
		Value:    token,
		Path:     "/poll/" + pollID,
		MaxAge:   60 * 60 * 24 * 365,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   schemeForRequest(r) == "https",
	})
}

func awsString(value string) *string {
	return &value
}

func stringSliceAttribute(values []string) []types.AttributeValue {
	attrs := make([]types.AttributeValue, 0, len(values))
	for _, value := range values {
		attrs = append(attrs, &types.AttributeValueMemberS{Value: value})
	}
	return attrs
}

var (
	errNotFound = errors.New("not found")
	errConflict = errors.New("conflict")
)
