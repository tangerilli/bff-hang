package main

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
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
}

type Poll struct {
	ID        string
	Title     string
	Days      []string
	CreatedAt time.Time
}

type Response struct {
	ID        string
	Name      string
	Days      []string
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
}

type DynamoDBStorage struct {
	client *dynamodb.Client
	Table  string
}

type PollItem struct {
	PK        string   `dynamodbav:"pk"`
	SK        string   `dynamodbav:"sk"`
	Type      string   `dynamodbav:"type"`
	ID        string   `dynamodbav:"id"`
	Title     string   `dynamodbav:"title"`
	Days      []string `dynamodbav:"days"`
	CreatedAt string   `dynamodbav:"created_at"`
}

type ResponseItem struct {
	PK        string   `dynamodbav:"pk"`
	SK        string   `dynamodbav:"sk"`
	Type      string   `dynamodbav:"type"`
	ID        string   `dynamodbav:"id"`
	Name      string   `dynamodbav:"name"`
	Days      []string `dynamodbav:"days"`
	CreatedAt string   `dynamodbav:"created_at"`
}

type MemoryStorage struct {
	polls     map[string]Poll
	responses map[string][]Response
}

type App struct {
	storage   Storage
	templates *template.Template
	baseURL   string
}

func main() {
	storage, err := newStorage(context.Background())
	if err != nil {
		log.Fatalf("failed to initialize storage: %v", err)
	}

	templates := template.Must(template.New("").Funcs(template.FuncMap{
		"formatDate": formatDate,
	}).ParseFS(os.DirFS("."), "templates/*.html"))

	app := &App{
		storage:   storage,
		templates: templates,
		baseURL:   os.Getenv("APP_BASE_URL"),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", app.handleHome)
	mux.HandleFunc("/polls", app.handleCreatePoll)
	mux.HandleFunc("/poll/", app.handlePoll)

	if os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" {
		adapter := httpadapter.New(mux)
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
		PK:        pollPartitionKey(poll.ID),
		SK:        "POLL",
		Type:      "poll",
		ID:        poll.ID,
		Title:     poll.Title,
		Days:      poll.Days,
		CreatedAt: poll.CreatedAt.Format(time.RFC3339),
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
				ID:        pollItem.ID,
				Title:     pollItem.Title,
				Days:      pollItem.Days,
				CreatedAt: parseTime(pollItem.CreatedAt),
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
	s.responses[pollID] = append(s.responses[pollID], response)
	return nil
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	data := struct {
		Upcoming []DayOption
	}{
		Upcoming: upcomingDays(14),
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

	poll := Poll{
		ID:        randomID(),
		Title:     title,
		Days:      selectedDays,
		CreatedAt: time.Now().UTC(),
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
		CreatedAt: time.Now().UTC(),
	}
	if err := a.storage.AddResponse(r.Context(), poll.ID, creatorResponse); err != nil {
		log.Printf("failed to add creator response: %v", err)
		http.Error(w, "unable to create poll", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/poll/"+poll.ID, http.StatusSeeOther)
}

func (a *App) handlePoll(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/poll/")
	if id == "" {
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		poll, responses, err := a.storage.GetPoll(r.Context(), id)
		if err != nil {
			if errors.Is(err, errNotFound) {
				http.NotFound(w, r)
				return
			}
			log.Printf("failed to load poll: %v", err)
			http.Error(w, "unable to load poll", http.StatusInternalServerError)
			return
		}

		view := a.buildPollView(r, poll, responses, "")
		a.render(w, "poll.html", view)
	case http.MethodPost:
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		name := strings.TrimSpace(r.FormValue("name"))
		selectedDays := normalizeDays(r.Form["days"])
		if name == "" || len(selectedDays) == 0 {
			poll, responses, err := a.storage.GetPoll(r.Context(), id)
			if err != nil {
				if errors.Is(err, errNotFound) {
					http.NotFound(w, r)
					return
				}
				log.Printf("failed to load poll: %v", err)
				http.Error(w, "unable to load poll", http.StatusInternalServerError)
				return
			}
			view := a.buildPollView(r, poll, responses, "Please enter your name and at least one available day.")
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
			CreatedAt: time.Now().UTC(),
		}
		if err := a.storage.AddResponse(r.Context(), id, response); err != nil {
			log.Printf("failed to add response: %v", err)
			http.Error(w, "unable to save response", http.StatusInternalServerError)
			return
		}

		poll, responses, err := a.storage.GetPoll(r.Context(), id)
		if err != nil {
			log.Printf("failed to reload poll: %v", err)
			http.Error(w, "unable to load poll", http.StatusInternalServerError)
			return
		}
		view := a.buildPollView(r, poll, responses, "")
		if isHTMX(r) {
			a.render(w, "results.html", view)
			return
		}
		a.render(w, "poll.html", view)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) buildPollView(r *http.Request, poll Poll, responses []Response, errMsg string) PollView {
	summaries := summarizeAvailability(poll.Days, responses)
	baseURL := a.baseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("%s://%s", schemeForRequest(r), r.Host)
	}
	return PollView{
		Poll:          poll,
		Responses:     responses,
		Summaries:     summaries,
		TotalResponse: len(responses),
		Error:         errMsg,
		ShareURL:      fmt.Sprintf("%s/poll/%s", strings.TrimRight(baseURL, "/"), poll.ID),
	}
}

func (a *App) render(w http.ResponseWriter, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.templates.ExecuteTemplate(w, name, data); err != nil {
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

func parseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Now().UTC()
	}
	return parsed
}

func pollPartitionKey(id string) string {
	return "POLL#" + id
}

func isHTMX(r *http.Request) bool {
	return r.Header.Get("HX-Request") == "true"
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

func awsString(value string) *string {
	return &value
}

var (
	errNotFound = errors.New("not found")
	errConflict = errors.New("conflict")
)
