package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"os"

	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

var ctx context.Context = context.Background()
var minioClient *minio.Client
var db *sql.DB

func main() {
	// Attempt to load .env
	godotenv.Load()

	// Initialise MinIO client
	var err error
	minioClient, err = minio.New(os.Getenv("MINIO_HOST"), &minio.Options{
		Creds:  credentials.NewStaticV4(os.Getenv("MINIO_ROOT_USER"), os.Getenv("MINIO_ROOT_PASSWORD"), ""),
		Secure: os.Getenv("MINIO_SSL") == "1",
	})
	if err != nil {
		panic(err)
	}

	// Create MinIO buckets
	if err := createMinIOBuckets(); err != nil {
		panic(err)
	}

	// Initialise database connection
	db, err = sql.Open(os.Getenv("DB_DRIVER"), os.Getenv("DB_URI"))
	if err != nil {
		panic(err)
	}

	// Test database connection
	if err := db.Ping(); err != nil {
		panic(err)
	}

	// Create database tables
	if err := createDBTables(); err != nil {
		panic(err)
	}

	// Create HTTP router
	r := chi.NewRouter()
	r.Route("/icons", IconsRouter)
	r.Route("/attachments", AttachmentsRouter)

	// Serve HTTP router
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "3000"
	}
	log.Println("Serving HTTP server on :" + port)
	http.ListenAndServe(":"+port, r)
}
