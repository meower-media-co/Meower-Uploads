package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/getsentry/sentry-go"
	_ "github.com/glebarez/go-sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/redis/go-redis/v9"
	"github.com/rs/cors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/reflection"

	grpcAuth "github.com/meower-media-co/Meower-Uploads/grpc_auth"
	grpcUploads "github.com/meower-media-co/Meower-Uploads/grpc_uploads"
)

var ctx context.Context = context.Background()
var db *sql.DB
var rdb *redis.Client
var s3Clients = make(map[string]*minio.Client)
var s3RegionOrder = []string{}

var grpcAuthClient grpcAuth.AuthClient

func main() {
	var err error

	// Load dotenv
	godotenv.Load()

	// Initialise Sentry
	sentry.Init(sentry.ClientOptions{
		Dsn: os.Getenv("SENTRY_DSN"),
	})

	// Connect to the SQL database
	db, err = sql.Open(os.Getenv("DB_DRIVER"), os.Getenv("DB_URI"))
	if err != nil {
		log.Fatalln(err)
	}
	if err := db.Ping(); err != nil {
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

	// Connect to MinIO regions
	var s3Endpoints [][2]string
	err = json.Unmarshal([]byte(os.Getenv("MINIO_REGIONS")), &s3Endpoints)
	if err != nil {
		log.Fatalln(err)
	}
	for _, region := range s3Endpoints {
		name := region[0]
		endpoint := region[1]
		s3Clients[name], err = minio.New(endpoint, &minio.Options{
			Creds:  credentials.NewStaticV4(os.Getenv("MINIO_ACCESS_KEY"), os.Getenv("MINIO_SECRET_KEY"), ""),
			Secure: os.Getenv("MINIO_SSL") == "1",
		})
		if err != nil {
			log.Fatalln(err)
		}
		s3RegionOrder = append(s3RegionOrder, name)
	}

	if os.Getenv("PRIMARY_NODE") == "1" {
		// Run migrations
		if err := runMigrations(); err != nil {
			log.Fatalln(err)
		}

		// Files cleanup
		go func() {
			for {
				time.Sleep(time.Minute)
				if err := cleanupFiles(); err != nil {
					sentry.CaptureException(err)
				}
			}
		}()

		// Start gRPC Uploads service
		go func() {
			lis, err := net.Listen("tcp", os.Getenv("GRPC_UPLOADS_ADDRESS"))
			if err != nil {
				log.Fatalln(err)
			}
			s := grpc.NewServer()
			reflection.Register(s)
			grpcUploads.RegisterUploadsServer(s, grpcUploadsServer{})
			if err := s.Serve(lis); err != nil {
				log.Fatalln(err)
			}
		}()
	}

	// Connect to gRPC Auth service
	var opts []grpc.DialOption
	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	grpcAuthConn, err := grpc.Dial(os.Getenv("GRPC_AUTH_ADDRESS"), opts...)
	if err != nil {
		log.Fatalln(err)
	}
	defer grpcAuthConn.Close()
	grpcAuthClient = grpcAuth.NewAuthClient(grpcAuthConn)

	// Create HTTP router
	r := chi.NewRouter()
	r.Use(cors.New(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		AllowCredentials: true,
	}).Handler)
	r.Route("/", router)

	// Send Sentry message
	sentry.CaptureMessage("Starting uploads service")

	// Serve HTTP router
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "3000"
	}
	log.Println("Serving HTTP server on :" + port)
	http.ListenAndServe(":"+port, r)

	// Wait for Sentry events to flush
	sentry.Flush(time.Second * 5)
}
