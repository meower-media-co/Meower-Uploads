package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type Upload struct {
	ID        string `bson:"_id"`
	FileID    string `bson:"fid"`
	FileName  string `bson:"file_name"`
	FileType  string `bson:"file_type"`
	FileSize  int64  `bson:"file_size"`
	CreatedAt int64  `bson:"created_at"`
}

type DirAssignment struct {
	FileID string `json:"fid"`
	Url    string `json:"url"`
}

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
	//db := client.Database("meowerserver")

	// Create fiber app
	app := fiber.New()

	// Upload file
	app.Post("/", func(c *fiber.Ctx) error {
		// Get file from form body
		file, err := c.FormFile("file")
		if err != nil {
			log.Fatalln(err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}

		// Make sure file doesn't exceed maximum file size
		if file.Size > 10000 {
			return c.SendStatus(fiber.StatusRequestEntityTooLarge)
		}

		// Open the file
		fileSrc, err := file.Open()
		if err != nil {
			return err
		}
		defer fileSrc.Close()

		// Assign a File ID within SeaweedFS
		resp, err := http.Get("http://192.168.1.141:9333/dir/assign")
		if err != nil {
			log.Fatalln(err)
		}
		defer resp.Body.Close()
		var dirAssignment DirAssignment
		err = json.NewDecoder(resp.Body).Decode(&dirAssignment)
		if err != nil {
			log.Fatalln(err)
		}

		// Upload the file to SeaweedFS
		req, err := http.NewRequest("PUT", "http://"+dirAssignment.Url+"/"+dirAssignment.FileID, fileSrc)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, "file", file.Filename))
		client := &http.Client{}
		resp, err = client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		fmt.Printf("status: %s", strconv.Itoa(resp.StatusCode))
		fmt.Println()
		log.Printf("uplodaed fid %s to %s", dirAssignment.FileID, dirAssignment.Url)

		return c.SendStatus(fiber.StatusOK)
	})

	// Start fiber app
	app.Listen(":3000")
}
