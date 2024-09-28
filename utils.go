package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/discord/lilliput"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"
)

var SupportedImages = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/webp": true,
	"image/gif":  true,
}

var (
	ErrUnsupportedFile    = errors.New("unsupported file")
	ErrFileBlocked        = errors.New("file blocked")
	ErrFileAlreadyClaimed = errors.New("file already claimed")
	ErrMismatchedBucket   = errors.New("mismatched bucket")
	ErrUnauthorized       = errors.New("unauthorized")
)

func generateId() (string, error) {
	// Generate bytes
	b := make([]byte, 18)
	_, err := rand.Read(b)
	if err != nil {
		return "", err
	}

	// Construct ID
	id := base64.URLEncoding.EncodeToString(b)
	id = strings.ReplaceAll(id, "-", "a")
	id = strings.ReplaceAll(id, "_", "b")
	id = strings.ReplaceAll(id, "=", "c")
	return id, err
}

func cleanFilename(filename string) string {
	re := regexp.MustCompile(`[^A-Za-z0-9\.\-\_\+\!\(\)$]`)
	return re.ReplaceAllString(filename, "_")
}

func optimizeImage(imageBytes []byte, mime string, maxSize int) ([]byte, string, error) {
	// Get file extension
	fileExt := map[string]string{
		"image/png":  ".webp",
		"image/jpeg": ".webp",
		"image/webp": ".webp",
		"image/gif":  ".gif",
	}[mime]
	if fileExt == "" {
		return nil, "", ErrUnsupportedFile
	}

	// Get lilliput decoder
	lilliputDecoder, err := lilliput.NewDecoder(imageBytes)
	if err != nil {
		return nil, "", err
	}
	defer lilliputDecoder.Close()

	// Original image dimensions
	originalWidth, originalHeight, err := getMediaDimensions(lilliputDecoder)
	if err != nil {
		return nil, "", err
	}

	// Calculate aspect ratio of the original image
	aspectRatio := float64(originalWidth) / float64(originalHeight)

	// Calculate new dimensions based on max width constraint
	newWidth := maxSize
	newHeight := int(float64(maxSize) / aspectRatio)

	// Shrink height if still too high
	if newHeight > maxSize {
		aspectRatio = float64(newHeight) / float64(newWidth)
		newHeight = maxSize
		newWidth = int(float64(newWidth) / aspectRatio)
	}

	// Create lilliput options
	lilliputOpts := lilliput.ImageOptions{
		FileType:     fileExt,
		Width:        newWidth,
		Height:       newHeight,
		ResizeMethod: lilliput.ImageOpsResize,
	}

	// Create ops
	lilliputOps := lilliput.NewImageOps(8192)
	defer lilliputOps.Close()

	// Transform image
	// Allow images up to 10 MiB to be resized
	newImageBytes, err := lilliputOps.Transform(lilliputDecoder, &lilliputOpts, make([]byte, 0, 10<<20))
	newMime := map[string]string{
		".webp": "image/webp",
		".gif":  "image/gif",
	}[fileExt]
	return newImageBytes, newMime, err
}

// returns width x height
func getMediaDimensions(lilliputDecoder lilliput.Decoder) (int, int, error) {
	// Get lilliput header
	lilliputHeader, err := lilliputDecoder.Header()
	if err != nil {
		return 0, 0, err
	}

	// Return width x height
	// Anything above orientation 4, we swap width and height.
	// Since anything above orientation 4 does rotations which swaps the width
	// and height for some reason.
	if lilliputHeader.Orientation() > 4 {
		return lilliputHeader.Height(), lilliputHeader.Width(), nil
	} else {
		return lilliputHeader.Width(), lilliputHeader.Height(), nil
	}
}

// Delete unclaimed files that are more than 30 minutes old
func cleanupFiles() error {
	cur, err := db.Collection("files").Find(context.TODO(), bson.M{
		"claimed":     false,
		"uploaded_at": bson.M{"$lt": time.Now().Unix() - 1800},
	})
	if err != nil {
		return err
	}

	var files []File
	if err := cur.All(context.TODO(), &files); err != nil {
		return err
	}

	for _, file := range files {
		if err := file.Delete(); err != nil {
			return err
		}
	}

	return nil
}

// Get the block status of a file by its hash.
// Returns whether it's blocked.
func getBlockStatus(hashHex string) (bool, error) {
	opts := options.Count()
	opts.SetLimit(1)
	count, err := db.Collection("blocked_files").CountDocuments(context.TODO(), bson.M{"_id": hashHex}, opts)
	return count > 0, err
}
