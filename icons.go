package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/h2non/bimg"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func iconRoutes(router fiber.Router, db *mongo.Database) {
	router.Post("/", func(c *fiber.Ctx) error {
		// Get authorization token
		token := c.Query("token")

		// Get token claims
		tokenValid, tokenClaims, err := getTokenClaims(token)
		if err != nil {
			fmt.Println(err)
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		// Make sure token is valid
		if !tokenValid || tokenClaims.Type != "upload_icon" {
			return c.SendStatus(fiber.StatusUnauthorized)
		}

		// Make sure file doesn't already been used
		count, err := db.Collection("uploads").CountDocuments(context.TODO(), bson.M{"_id": tokenClaims.Upload.ID})
		if err != nil {
			fmt.Println(err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		if count > 0 {
			return c.SendStatus(fiber.StatusConflict)
		}

		// Get file from body
		file, err := c.FormFile("file")
		if err != nil {
			fmt.Println(err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}

		// Make sure file doesn't exceed maximum file size
		if file.Size > tokenClaims.Upload.MaxSize {
			return c.SendStatus(fiber.StatusRequestEntityTooLarge)
		}

		// Get file extension and make sure content type is allowed
		fileExt := map[string]string{
			"image/png":     ".webp",
			"image/jpeg":    ".webp",
			"image/svg+xml": ".webp",
			"image/gif":     ".gif",
		}[file.Header.Get("Content-Type")]
		if fileExt == "" {
			return c.SendStatus(fiber.StatusBadRequest)
		}

		// Get file content
		fileContent, err := file.Open()
		if err != nil {
			fmt.Println(err)
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

		// Get existing SeaweedFS fid
		var upload Upload
		opts := options.FindOne().SetProjection(bson.M{"fid": 1, "size": 1})
		err = db.Collection("uploads").FindOne(context.TODO(), bson.M{"hash": fileHash}, opts).Decode(&upload)
		if err != nil && err != mongo.ErrNoDocuments {
			fmt.Println(err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}

		// Upload to SeaweedFS if a file doesn't exist
		if upload.ID == "" {
			bimgOptions := bimg.Options{
				Width:     256,
				Height:    0, // auto, to keep aspect ratio
				Quality:   80,
				Interlace: true,
				Lossless:  false,
			}
			if fileExt != ".gif" {
				bimgOptions.Type = bimg.WEBP
			}
			image, err := bimg.NewImage(fileBytes).Process(bimgOptions)
			if err != nil {
				fmt.Println(err)
				return c.SendStatus(fiber.StatusInternalServerError)
			}

			fid, err := saveFile(image, tokenClaims.Upload.ID+fileExt)
			if err != nil {
				fmt.Println(err)
				return c.SendStatus(fiber.StatusInternalServerError)
			}

			upload = Upload{
				Fid:  fid,
				Size: int64(len(image)),
			}
		}

		// Create new upload
		newUpload := Upload{
			ID:        tokenClaims.Upload.ID,
			Hash:      fileHash,
			Fid:       upload.Fid,
			Type:      "icon",
			Filename:  tokenClaims.Upload.ID + fileExt, // we use the upload ID here instead of the original name for privacy
			Mime:      file.Header.Get("Content-Type"),
			Size:      upload.Size,
			CreatedAt: time.Now().Unix(),
		}
		_, err = db.Collection("uploads").InsertOne(context.TODO(), newUpload)
		if err != nil {
			fmt.Println(err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}

		// Return new upload
		return c.JSON(newUpload)
	})

	router.Get("/:uploadID.:fileExt", func(c *fiber.Ctx) error {
		// Get upload ID from request
		uploadID := c.Params("uploadID")

		// Get upload from database
		var upload Upload
		filter := bson.M{"_id": uploadID, "type": "icon"}
		opts := options.FindOne().SetProjection(bson.M{"fid": 1, "created_at": 1})
		err := db.Collection("uploads").FindOne(context.TODO(), filter, opts).Decode(&upload)
		if err != nil {
			if err == mongo.ErrNoDocuments {
				return c.SendStatus(fiber.StatusNotFound)
			} else {
				fmt.Println(err)
				return c.SendStatus(fiber.StatusInternalServerError)
			}
		}

		// Return 304 Not Modified response if the client has valid cache
		parsedTime, err := time.Parse(http.TimeFormat, c.Get("If-Modified-Since", "Thu, 01 Jan 1970 00:00:00 GMT"))
		if err != nil {
			fmt.Println(err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		if parsedTime.Unix() >= upload.CreatedAt {
			return c.SendStatus(fiber.StatusNotModified)
		}

		// Get file from SeaweedFS
		resp, err := loadFile(upload.Fid)
		if err != nil {
			fmt.Println(err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		defer resp.Body.Close()

		// Read file content
		fileContent, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Println(err)
			return c.SendStatus(fiber.StatusInternalServerError)
		}

		// Set headers
		c.Set("Content-Type", resp.Header.Get("Content-Type"))
		c.Set("Last-Modified", resp.Header.Get("Last-Modified"))

		// Send file content
		return c.Send(fileContent)
	})
}
