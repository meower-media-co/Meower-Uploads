package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"

	_ "github.com/glebarez/go-sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"github.com/rs/cors"
)

var ctx context.Context = context.Background()
var db *sql.DB
var rdb *redis.Client
var s3 *minio.Client

func main() {
	var err error

	// Load dotenv
	godotenv.Load()

	// Check token secret
	if os.Getenv("TOKEN_SECRET") == "" {
		log.Fatalln("TOKEN_SECRET is not set. Please set up your environment variables.")
	}

	// Connect to the SQL database
	db, err = sql.Open(os.Getenv("DB_DRIVER"), os.Getenv("DB_URI"))
	if err != nil {
		log.Fatalln(err)
	}
	if err := db.Ping(); err != nil {
		log.Fatalln(err)
	}

	// Run database migrations
	if err := runDBMigrations(); err != nil {
		log.Fatalln(err)
	}

	// Connect to Redis
	opt, err := redis.ParseURL(os.Getenv("REDIS_URI"))
	if err != nil {
		log.Fatalln(err)
	}
	rdb = redis.NewClient(opt)
	_, err = rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalln(err)
	}

	// Connect to MinIO
	s3, err = minio.New(os.Getenv("MINIO_ENDPOINT"), &minio.Options{
		Creds:  credentials.NewStaticV4(os.Getenv("MINIO_ACCESS_KEY"), os.Getenv("MINIO_SECRET_KEY"), ""),
		Secure: os.Getenv("MINIO_SSL") == "1",
	})
	if err != nil {
		log.Fatalln(err)
	}

	// Create MinIO buckets
	if err := createMinIOBuckets(); err != nil {
		log.Fatalln(err)
	}

	// Start pub/sub listener
	go startPubSubListener()

	// Create HTTP router
	r := chi.NewRouter()

	// Set CORS policy
	r.Use(cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}).Handler)

	// Add routes
	r.Route("/icons", iconsRouter)
	r.Route("/attachments", attachmentsRouter)
	r.Route("/data-exports", dataExportsRouter)

	// Serve HTTP router
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "3000"
	}
	log.Println("Serving HTTP server on :" + port)
	http.ListenAndServe(":"+port, r)
}
