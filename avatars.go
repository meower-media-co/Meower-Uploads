package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/h2non/bimg"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func avatarRoutes(router fiber.Router, db *mongo.Database) {
	router.Post("/", func(c *fiber.Ctx) error {
		// Get authorization token
		token := c.Params("token")

		// Get token claims
		tokenValid, tokenClaims, err := getTokenClaims(token)
		if err != nil {
			log.Panicln(err)
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		// Make sure token is valid
		if !tokenValid || tokenClaims.Type != "upload_file" || tokenClaims.Upload.UploadType != "avatar" {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		// Get file from body
		file, err := c.FormFile("file")
		if err != nil {
			return c.SendStatus(fiber.StatusInternalServerError)
		}

		// Make sure file doesn't exceed maximum file size
		if file.Size > tokenClaims.Upload.MaxSize {
			return c.SendStatus(fiber.StatusRequestEntityTooLarge)
		}

		// Make sure content type is allowed
		allowedContentTypes := []string{
			"image/png",
			"image/jpeg",
			"image/gif",
			"image/svg+xml",
		}
		contentTypeAllowed := false
		for _, value := range allowedContentTypes {
			if value == file.Header.Get("Content-Type") {
				contentTypeAllowed = true
				break
			}
		}
		if !contentTypeAllowed {
			return c.SendStatus(fiber.StatusBadRequest)
		}

		// Get file content
		fileContent, err := file.Open()
		if err != nil {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		defer fileContent.Close()

		// Convert file content into a byte slice
		fileBytes, err := io.ReadAll(fileContent)
		if err != nil {
			return err
		}

		// Get hash of file
		fileHash := getFileHash(fileBytes)

		// Get fid and file size, upload file if none already exists in the database
		var upload Upload
		opts := options.FindOne().SetProjection(bson.M{"fid": 1, "size": 1})
		err = db.Collection("uploads").FindOne(context.TODO(), bson.M{"hash": fileHash}, opts).Decode(&upload)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				// Process image
				image, err := bimg.NewImage(fileBytes).Process(bimg.Options{
					Width:     256,
					Height:    0, // auto, to keep aspect ratio
					Quality:   80,
					Interlace: true,
					Lossless:  false,
					Type:      bimg.WEBP,
				})
				if err != nil {
					log.Fatalln(err)
					return c.SendStatus(fiber.StatusInternalServerError)
				}

				// Upload to SeaweedFS
				fid, err := saveFile(image, tokenClaims.Upload.UploadID+".webp")
				if err != nil {
					log.Fatalln(err)
				}

				// Create temp upload object
				upload = Upload{
					Fid:  fid,
					Size: int64(len(image)),
				}
			} else {
				panic(err)
			}
		}

		// Create new upload object
		newUpload := Upload{
			ID:        tokenClaims.Upload.UploadID,
			Hash:      fileHash,
			Fid:       upload.Fid,
			Type:      "avatar",
			Filename:  tokenClaims.Upload.UploadID + ".webp", // we use the upload ID here instead of the original name for privacy
			Mime:      "image/webp",
			Size:      upload.Size,
			CreatedAt: time.Now().Unix(),
		}

		// Add upload to database
		_, err = db.Collection("uploads").InsertOne(context.TODO(), newUpload)
		if err != nil {
			panic(err)
		}

		// Return new upload
		return c.JSON(newUpload)
	})

	router.Get("/:id.webp", func(c *fiber.Ctx) error {
		// Get upload ID from request
		uploadID := c.Params("id")

		// Get upload from database
		var upload Upload
		filter := bson.M{"_id": uploadID, "type": "avatar"}
		opts := options.FindOne().SetProjection(bson.M{"fid": 1, "created_at": 1})
		err := db.Collection("uploads").FindOne(context.TODO(), filter, opts).Decode(&upload)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return c.SendStatus(fiber.StatusNotFound)
			}
			panic(err)
		}

		// Return 304 Not Modified response if the client has valid cache
		parsedTime, err := time.Parse(http.TimeFormat, c.Get("If-Modified-Since", "Thu, 01 Jan 1970 00:00:00 GMT"))
		if err != nil {
			panic(err)
		}
		if parsedTime.Unix() >= upload.CreatedAt {
			return c.SendStatus(fiber.StatusNotModified)
		}

		// Get file from SeaweedFS
		resp, err := loadFile(upload.Fid)
		if err != nil {
			panic(err)
		}
		defer resp.Body.Close()

		// Read file content
		fileContent, err := io.ReadAll(resp.Body)
		if err != nil {
			panic(err)
		}

		// Set headers
		c.Set("Content-Type", resp.Header.Get("Content-Type"))
		c.Set("Last-Modified", resp.Header.Get("Last-Modified"))

		// Send file content
		return c.Send(fileContent)
	})
}
