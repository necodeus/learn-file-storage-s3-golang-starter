package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

// Complete the (currently empty) handlerUploadVideo handler to store video files in S3. Images will stay on the local file system for now. I recommend using the image upload handler as a reference.
func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// Set an upload limit of 1 GB (1 << 30 bytes) using http.MaxBytesReader.
	const maxMemory = 1 << 30
	http.MaxBytesReader(w, r.Body, maxMemory)

	// Extract the videoID from the URL path parameters and parse it as a UUID
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	// Authenticate the user to get a userID
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	// Get the video metadata from the database, if the user is not the video owner, return a http.StatusUnauthorized response
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't get video", err)
		return
	}
	if video.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User is not the video owner", nil)
		return
	}

	// Parse the uploaded video file from the form data

	// Use (http.Request).FormFile with the key "video" to get a multipart.File in memory
	videoFile, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	// Remember to defer closing the file with (os.File).Close - we don't want any memory leaks
	defer videoFile.Close()

	// Validate the uploaded file to ensure it's an MP4 video
	// - Use mime.ParseMediaType and "video/mp4" as the MIME type
	mediaType, _, err := mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse media type", err)
		return
	}
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "Invalid media type", nil)
		return
	}

	// Save the uploaded file to a temporary file on disk.

	// Use os.CreateTemp to create a temporary file. I passed in an empty string for the directory to use the system default, and the name "tubely-upload.mp4" (but you can use whatever you want)
	tempFile, err := os.CreateTemp("", "tubely-upload.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to create temp file", err)
		return
	}
	// defer remove the temp file with os.Remove
	defer os.Remove(tempFile.Name())
	// defer close the temp file (defer is LIFO, so it will close before the remove)
	defer tempFile.Close()

	// io.Copy the contents over from the wire to the temp file
	_, err = io.Copy(tempFile, videoFile)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to save video file", err)
		return
	}

	// Reset the tempFile's file pointer to the beginning with .Seek(0, io.SeekStart) - this will allow us to read the file again from the beginning
	_, err = tempFile.Seek(0, io.SeekStart)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to seek temp file", err)
		return
	}

	// The file key. Use the same <random-32-byte-hex>.ext format as the key. e.g. 1a2b3c4d5e6f7890abcd1234ef567890.mp4
	randomHex, err := uuid.NewRandom()
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to generate random hex", err)
		return
	}

	// Update handlerUploadVideo to create a processed version of the video. Upload the processed video to S3, and discard the original.
	processedFilePath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to process video for fast start", err)
		return
	}
	defer os.Remove(processedFilePath)

	// Open the processed file
	processedFile, err := os.Open(processedFilePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to open processed file", err)
		return
	}
	defer processedFile.Close()

	// Update the handlerUploadVideo to get the aspect ratio of the video file from the temporary file once it's saved to disk.
	// Depending on the aspect ratio, add a "landscape", "portrait", or "other" prefix to the key before uploading it to S3.
	aspectRatio, err := getVideoAspectRatio(processedFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to get video aspect ratio", err)
		return
	}
	prefix := "other"
	switch aspectRatio {
	case "16:9":
		prefix = "landscape"
	case "9:16":
		prefix = "portrait"
	}

	// fileKey := randomHex.String() + ".mp4" // without prefix
	fileKey := prefix + "/" + randomHex.String() + ".mp4" // with prefix

	// Put the object into S3 using PutObject
	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         &fileKey,
		Body:        processedFile,
		ContentType: &mediaType,
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to upload video to S3", err)
		return
	}

	// Store an actual URL again in the video_url column, but this time, use the cloudfront URL. Use your distribution's domain name, and then dynamically inject the S3 object's key.
	// Set the distribution's domain name in your .env and grab it from the apiConfig's s3CfDistribution field.
	videoURL := fmt.Sprintf("%s/%s", cfg.s3CfDistribution, fileKey)
	video.VideoURL = &videoURL
	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video", err)
		return
	}

	// signedVideo, err := cfg.dbVideoToSignedVideo(video)
	// if err != nil {
	// 	respondWithError(w, http.StatusInternalServerError, "Unable to sign video", err)
	// 	return
	// }

	respondWithJSON(w, http.StatusOK, video)
}

// func (cfg *apiConfig) dbVideoToSignedVideo(video database.Video) (database.Video, error) {
// 	// Split the video.VideoURL on the comma to get the bucket and key:
// 	bucketAndKey := strings.Split(*video.VideoURL, ",")

// 	// Use generatePresignedURL to get a presigned URL for the video:
// 	duration := 10 * time.Minute
// 	presignedURL, err := generatePresignedURL(cfg.s3Client, bucketAndKey[0], bucketAndKey[1], duration)
// 	if err != nil {
// 		return database.Video{}, err
// 	}

// 	// Set the VideoURL field of the video to the presigned URL and return the updated video:
// 	video.VideoURL = &presignedURL

// 	// Return a database.Video with the VideoURL field set to a presigned URL and an error (to be returned from the handler):
// 	return video, nil
// }

// func generatePresignedURL(s3Client *s3.Client, bucket, key string, expireTime time.Duration) (string, error) {
// 	// Use the SDK to create a s3.PresignClient with s3.NewPresignClient:
// 	presignClient := s3.NewPresignClient(s3Client)

// 	// Use the client's .PresignGetObject() method with s3.WithPresignExpires as a functional option:
// 	presignedReq, err := presignClient.PresignGetObject(context.Background(), &s3.GetObjectInput{
// 		Bucket: &bucket,
// 		Key:    &key,
// 	}, func(options *s3.PresignOptions) {
// 		options.Expires = expireTime
// 	})
// 	if err != nil {
// 		return "", err
// 	}

// 	// Return the .URL field of the v4.PresignedHTTPRequest created by .PresignGetObject()
// 	return presignedReq.URL, nil
// }

// Create a function getVideoAspectRatio(filePath string) (string, error) that takes a file path and returns the aspect ratio as a string.
func getVideoAspectRatio(filePath string) (string, error) {
	// It should use exec.Command to run the same ffprobe command as above. In this case, the command is ffprobe and the arguments are -v, error, -print_format, json, -show_streams, and the file path:
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)

	// Set the resulting exec.Cmd's Stdout field to a pointer to a new bytes.Buffer:
	cmd.Stdout = &bytes.Buffer{}

	// .Run() the command:
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	// Unmarshal the stdout of the command from the buffer's .Bytes into a JSON struct so that you can get the width and height fields:
	streams := struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}{}
	err = json.Unmarshal(cmd.Stdout.(*bytes.Buffer).Bytes(), &streams)
	if err != nil {
		return "", err
	}
	if len(streams.Streams) == 0 {
		return "", fmt.Errorf("no streams found")
	}

	// I did a bit of math to determine the ratio, then returned one of three strings: 16:9, 9:16, or other.
	// Aspect ratios might be slightly off due to rounding errors. You can use a tolerance range (or just use integer division and call it a day).

	width := streams.Streams[0].Width
	height := streams.Streams[0].Height
	tolerance := 0.01
	ratio := float64(width) / float64(height)
	if ratio > 16.0/9.0-tolerance && ratio < 16.0/9.0+tolerance {
		return "16:9", nil
	}
	if ratio > 9.0/16.0-tolerance && ratio < 9.0/16.0+tolerance {
		return "9:16", nil
	}
	return "other", nil
}

// Create a new function called processVideoForFastStart(filePath string) (string, error) that takes a file path as input and creates and returns a new path to a file with "fast start" encoding.
func processVideoForFastStart(filePath string) (string, error) {
	// Create a new string for the output file path. I just appended .processing to the input file (which should be the path to the temp file on disk)
	outputPath := filePath + ".processing"

	// Create a new exec.Cmd using exec.Command
	// The command is ffmpeg and the arguments are -i, the input file path, -c, copy, -movflags, faststart, -f, mp4 and the output file path.
	cmd := exec.Command("ffmpeg", "-i", filePath, "-c", "copy", "-movflags", "faststart", "-f", "mp4", outputPath)

	// Run the command with .Run()
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	// Return the output file path
	return outputPath, nil
}
