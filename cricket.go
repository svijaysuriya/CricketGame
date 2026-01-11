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

	_ "net/http/pprof" // pprof for profiling

	"github.com/gorilla/mux"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

const (
	RATE_LIMIT_SECONDS = 2 // Minimum seconds between hits per student
	CACHE_TTL_SECONDS  = 2 // Scoreboard cache TTL
)

// // CONNECTION POOLING - COMMENTED OUT
// type ConnectionPool struct {
// 	pool chan *mongo.Client
// 	size int
// }

// func (cp ConnectionPool) Get() *mongo.Client {
// 	return <-cp.pool // Blocks until a connection is available
// }

// func (cp ConnectionPool) Put(c *mongo.Client) {
// 	cp.pool <- c // Returns connection to pool
// }

// SINGLE CONNECTION - Uses MongoDB's built-in connection pooling (default 100)
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

// // CONNECTION POOLING initDB - COMMENTED OUT
// func initDB(n int) {
// 	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
// 	defer cancel()

// 	mongoURI := os.Getenv("MONGODB_URI")
// 	if mongoURI == "" {
// 		panic("MONGODB_URI environment variable is required")
// 	}

// 	cp = ConnectionPool{
// 		pool: make(chan *mongo.Client, n),
// 		size: n,
// 	}

// 	for i := 0; i < n; i++ {
// 		fmt.Println("Creating connection", i+1, "of", n)
// 		mongoClient, err := mongo.Connect(ctx, options.Client().ApplyURI(mongoURI))
// 		if err != nil {
// 			fmt.Println(err.Error())
// 			panic(err)
// 		}
// 		err = mongoClient.Ping(ctx, nil)
// 		if err != nil {
// 			fmt.Println(err.Error())
// 			panic(err)
// 		}
// 		cp.pool <- mongoClient
// 	}

// 	mongoClient := cp.Get()
// 	indexModel := mongo.IndexModel{
// 		Keys:    bson.D{{Key: "rollNumber", Value: 1}},
// 		Options: options.Index().SetUnique(true),
// 	}
// 	_, err := mongoClient.Database("cricket_db").Collection("students").Indexes().CreateOne(ctx, indexModel)
// 	if err != nil {
// 	}
// 	cp.Put(mongoClient)
// }

// SINGLE CONNECTION initDB - Uses MongoDB's built-in pooling
func initDB() {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
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

	collection = mongoClient.Database("cricket_db").Collection("students")

	// Create unique index on rollNumber
	indexModel := mongo.IndexModel{
		Keys:    bson.D{{Key: "rollNumber", Value: 1}},
		Options: options.Index().SetUnique(true),
	}
	_, err = collection.Indexes().CreateOne(ctx, indexModel)
	if err != nil {
		fmt.Println("Index creation:", err.Error())
	}

	fmt.Println("Connected to MongoDB with built-in connection pooling (default: 100)")
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
	requestStart := time.Now() // ⏱️ TIMING: Request start

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

	dbStart := time.Now() // ⏱️ TIMING: DB start
	_, err := collection.UpdateOne(ctx, filter, update, opts)
	dbDuration := time.Since(dbStart) // ⏱️ TIMING: DB end

	if err != nil {
		fmt.Println(err.Error())
		http.Error(w, "Error updating score", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"message": "Shot recorded successfully"})

	// ⏱️ TIMING LOG
	fmt.Printf("[hitShot] Total: %v | DB: %v\n",
		time.Since(requestStart),
		dbDuration)
}

func getScoreboard(w http.ResponseWriter, r *http.Request) {
	requestStart := time.Now() // ⏱️ TIMING: Request start

	w.Header().Add("Content-Type", "application/json; charset=UTF-8")

	// Check if cache is valid
	scoreboardCacheMutex.RLock()
	if time.Since(cacheLastUpdated).Seconds() < CACHE_TTL_SECONDS && cachedScoreboard != nil {
		json.NewEncoder(w).Encode(cachedScoreboard)
		scoreboardCacheMutex.RUnlock()
		fmt.Printf("[getScoreboard] Total: %v | CACHE HIT\n", time.Since(requestStart))
		return
	}
	scoreboardCacheMutex.RUnlock()

	// Cache miss - fetch from database
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Sort by score descending
	opts := options.Find().SetSort(bson.D{{Key: "score", Value: -1}})

	dbStart := time.Now() // ⏱️ TIMING: DB start
	cursor, err := collection.Find(ctx, bson.M{}, opts)
	if err != nil {
		fmt.Println(err.Error())
		http.Error(w, "Error fetching scoreboard", http.StatusInternalServerError)
		return
	}

	var students []Student
	if err := cursor.All(ctx, &students); err != nil {
		cursor.Close(ctx)
		fmt.Println(err.Error())
		http.Error(w, "Error decoding data", http.StatusInternalServerError)
		return
	}
	cursor.Close(ctx)
	dbDuration := time.Since(dbStart) // ⏱️ TIMING: DB end

	// Update cache
	scoreboardCacheMutex.Lock()
	cachedScoreboard = students
	cacheLastUpdated = time.Now()
	scoreboardCacheMutex.Unlock()

	json.NewEncoder(w).Encode(students)

	// ⏱️ TIMING LOG
	fmt.Printf("[getScoreboard] Total: %v | DB: %v\n",
		time.Since(requestStart),
		dbDuration)
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
	// Start pprof server on port 5566
	go func() {
		fmt.Println("pprof running on :5566")
		log.Println(http.ListenAndServe(":5566", nil))
	}()

	initDB() // Uses MongoDB's built-in connection pooling (default: 100)

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
