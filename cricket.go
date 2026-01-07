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

var (
	mongoClient *mongo.Client
	collection  *mongo.Collection
	mutex       sync.Mutex
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

	collection = mongoClient.Database("cricket_db").Collection("students")

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

func hitShot(w http.ResponseWriter, r *http.Request) {
	mutex.Lock()
	defer mutex.Unlock()
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

	// fmt.Println("Student:", input.Name, "| Roll Number =", input.RollNumber, "| Shot =", input.Shot)

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
	mutex.Lock()
	defer mutex.Unlock()

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

	w.Header().Add("Content-Type", "application/json; charset=UTF-8")
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
