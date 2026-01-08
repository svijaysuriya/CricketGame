package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	RATE_LIMIT_SECONDS = 2 // Minimum seconds between hits per student
	CACHE_TTL_SECONDS  = 2 // Scoreboard cache TTL
)

var (
	mongoClient *mongo.Client
	collection  *mongo.Collection

	// Rate limiting: map of rollNumber -> last hit time
	rateLimitMap   = make(map[string]time.Time)
	rateLimitMutex sync.RWMutex

	// Scoreboard cache
	cachedScoreboard     []Student
	cacheLastUpdated     time.Time
	scoreboardCacheMutex sync.RWMutex
)

type Student struct {
	RollNumber string    `json:"rollNumber" bson:"rollNumber"`
	Name       string    `json:"name" bson:"name"`
	Score      int       `json:"score" bson:"score"`
	LastPlayed time.Time `json:"lastPlayed" bson:"lastPlayed"`
}

func initDB() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	mongoURI := os.Getenv("MONGODB_URI")
	if mongoURI == "" {
		panic("MONGODB_URI environment variable is required")
	}

	var err error
	mongoClient, err = mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
	if err != nil {
		fmt.Println(err.Error())
		panic(err)
	}

	// Ping the database to verify connection
	err = mongoClient.Ping(ctx, nil)
	if err != nil {
		fmt.Println(err.Error())
		panic(err)
	}

	collection = mongoClient.Database("cricket_db").Collection("students_performance")

	// Create unique index on rollNumber
	indexModel := mongo.IndexModel{
		Keys:    bson.D{{Key: "rollNumber", Value: 1}},
		Options: options.Index().SetUnique(true),
	}
	_, err = collection.Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		// fmt.Println("Index creation:", err.Error())
	}

	// fmt.Println("Connected to MongoDB successfully!")
}

// validateRollNumber checks if the roll number is exactly 10 digits
func validateRollNumber(rollNumber string) bool {
	matched, _ := regexp.MatchString(`^\d{10}$`, rollNumber)
	return matched
}

// Check rate limit for a roll number
func isRateLimited(rollNumber string) bool {
	rateLimitMutex.RLock()
	lastHit, exists := rateLimitMap[rollNumber]
	rateLimitMutex.RUnlock()

	if exists && time.Since(lastHit).Seconds() < RATE_LIMIT_SECONDS {
		return true
	}
	return false
}

// Update rate limit timestamp
func updateRateLimit(rollNumber string) {
	rateLimitMutex.Lock()
	rateLimitMap[rollNumber] = time.Now()
	rateLimitMutex.Unlock()
}

func hitShot(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json")

	var input struct {
		RollNumber string `json:"rollNumber"`
		Name       string `json:"name"`
		Shot       int    `json:"shot"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		fmt.Println(err.Error())
		http.Error(w, "Invalid input", http.StatusBadRequest)
		return
	}

	// Validate roll number (must be 10 digits)
	if !validateRollNumber(input.RollNumber) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Roll number must be exactly 10 digits"})
		return
	}

	// Validate name
	if input.Name == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Name is required"})
		return
	}

	// Check rate limit
	if isRateLimited(input.RollNumber) {
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{"error": "Too many requests. Please wait a few seconds."})
		return
	}

	// Update rate limit
	updateRateLimit(input.RollNumber)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Upsert: update if exists, insert if not
	filter := bson.M{"rollNumber": input.RollNumber}
	update := bson.M{
		"$inc":         bson.M{"score": input.Shot},
		"$set":         bson.M{"lastPlayed": time.Now(), "name": input.Name},
		"$setOnInsert": bson.M{"rollNumber": input.RollNumber},
	}
	opts := options.Update().SetUpsert(true)

	_, err := collection.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		fmt.Println(err.Error())
		http.Error(w, "Error updating score", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Shot recorded successfully"})
}

func getScoreboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json; charset=UTF-8")

	// Check if cache is valid
	scoreboardCacheMutex.RLock()
	if time.Since(cacheLastUpdated).Seconds() < CACHE_TTL_SECONDS && cachedScoreboard != nil {
		json.NewEncoder(w).Encode(cachedScoreboard)
		scoreboardCacheMutex.RUnlock()
		return
	}
	scoreboardCacheMutex.RUnlock()

	// Cache miss - fetch from database
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Sort by score descending
	opts := options.Find().SetSort(bson.D{{Key: "score", Value: -1}})
	cursor, err := collection.Find(ctx, bson.M{}, opts)
	if err != nil {
		fmt.Println(err.Error())
		http.Error(w, "Error fetching scoreboard", http.StatusInternalServerError)
		return
	}
	defer cursor.Close(ctx)

	var students []Student
	if err := cursor.All(ctx, &students); err != nil {
		fmt.Println(err.Error())
		http.Error(w, "Error decoding data", http.StatusInternalServerError)
		return
	}

	// Update cache
	scoreboardCacheMutex.Lock()
	cachedScoreboard = students
	cacheLastUpdated = time.Now()
	scoreboardCacheMutex.Unlock()

	json.NewEncoder(w).Encode(students)
}

// CORS middleware function
func enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")

		// Handle preflight OPTIONS request
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func main() {
	initDB()

	r := mux.NewRouter()

	// API routes
	r.HandleFunc("/hit", hitShot).Methods("POST", "OPTIONS")
	r.HandleFunc("/scoreboard", getScoreboard).Methods("GET", "OPTIONS")

	// Serve static files from UI directory
	r.PathPrefix("/").Handler(http.FileServer(http.Dir("./UI")))

	handler := enableCORS(r)

	// Use PORT environment variable for Railway, default to 9000
	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}

	fmt.Printf("Cricket Battle League API running on port %s...\n", port)

	err := http.ListenAndServe(":"+port, handler)
	if err != nil {
		log.Fatal(err)
	}
}
