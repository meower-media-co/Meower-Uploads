package main

import (
	"context"
	"log"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func main() {
	// Load dotenv
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found!")
	}

	// Get MongoDB URI and MongoDB database name
	var mongoUri = ""
	if mongoUri = os.Getenv("MONGODB_URI"); mongoUri == "" {
		mongoUri = "mongodb://127.0.0.1:27017"
	}
	var mongoDB = ""
	if mongoDB = os.Getenv("MONGODB_NAME"); mongoDB == "" {
		mongoDB = "meowerserver"
	}

	// Connect to MongoDB database
	client, err := mongo.Connect(context.TODO(), options.Client().ApplyURI(mongoUri))
	if err != nil {
		panic(err)
	}
	defer func() {
		if err := client.Disconnect(context.TODO()); err != nil {
			panic(err)
		}
	}()
	db := client.Database("meowerserver")

	// Create fiber app
	app := fiber.New(fiber.Config{
		BodyLimit: (32 << 20), // 32 MiB
	})

	// Add routes
	iconsRouter := app.Group("/icons")
	iconRoutes(iconsRouter, db)

	// Start fiber app
	app.Listen(":3000")
}
