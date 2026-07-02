package pdbapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"pdbbot/internal/token"
)

const iconSize = 100

// generateJPEG returns a small solid-color JPEG — used as the default group chat icon.
func generateJPEG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	c := color.RGBA{R: 180, G: 120, B: 200, A: 255} // soft purple
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// UploadGroupChatIcon generates a default JPEG, uploads it via the PDB image
// pipeline, and returns the signed icon token for use in CreateGroupChat.
func (c *Client) UploadGroupChatIcon(ctx context.Context) (string, error) {
	imgData, err := generateJPEG()
	if err != nil {
		return "", fmt.Errorf("generate jpeg: %w", err)
	}

	// Step 1: get S3 upload params + metadata token from PDB.
	lambdaBody, _ := json.Marshal(map[string]any{
		"bizType":     6,
		"contentType": "image/jpeg",
		"filesize":    len(imgData),
		"height":      iconSize,
		"isGiphy":     false,
		"width":       iconSize,
	})
	req, err := token.NewAPIRequest(ctx, "POST", "/aws/lambda/images", lambdaBody)
	if err != nil {
		return "", fmt.Errorf("lambda/images request: %w", err)
	}
	resp, err := c.mgr.Do(ctx, req)
	if err != nil {
		return "", fmt.Errorf("lambda/images: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("lambda/images: status %d: %s", resp.StatusCode, raw)
	}

	var uploadResp struct {
		Data struct {
			S3Endpoint struct {
				URL      string            `json:"url"`
				FormData map[string]string `json:"formData"`
			} `json:"s3Endpoint"`
			ImageEndpoint struct {
				URL      string            `json:"url"`
				FormData map[string]string `json:"formData"`
			} `json:"imageEndpoint"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&uploadResp); err != nil {
		return "", fmt.Errorf("lambda/images decode: %w", err)
	}
	metadataToken := uploadResp.Data.ImageEndpoint.FormData["metadata"]

	// Step 2: upload image to S3 via pre-signed multipart POST.
	if err := uploadToS3(ctx, uploadResp.Data.S3Endpoint.URL, uploadResp.Data.S3Endpoint.FormData, imgData); err != nil {
		return "", fmt.Errorf("s3 upload: %w", err)
	}

	// Step 3: notify PDB image processor. It returns the final token with
	// origin+sizes fields; fall back to the metadata token if it doesn't.
	finalToken, err := notifyImageEndpoint(ctx, uploadResp.Data.ImageEndpoint.URL, metadataToken)
	if err != nil || finalToken == "" {
		return metadataToken, nil
	}
	return finalToken, nil
}

// uploadToS3 performs a pre-signed multipart POST upload to S3.
// S3 requires all text fields before the file field.
func uploadToS3(ctx context.Context, url string, formData map[string]string, imgData []byte) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)

	// Write fields in the order S3 expects (policy fields before file).
	orderedKeys := []string{
		"Content-Type", "bucket", "key", "policy",
		"x-amz-algorithm", "x-amz-credential", "x-amz-date", "x-amz-signature",
	}
	written := map[string]bool{}
	for _, k := range orderedKeys {
		if v, ok := formData[k]; ok {
			_ = w.WriteField(k, v)
			written[k] = true
		}
	}
	for k, v := range formData {
		if !written[k] {
			_ = w.WriteField(k, v)
		}
	}
	// file must be last in S3 pre-signed POST.
	fw, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Disposition": []string{`form-data; name="file"; filename="image.jpeg"`},
		"Content-Type":        []string{"image/jpeg"},
	})
	if err != nil {
		return err
	}
	if _, err := fw.Write(imgData); err != nil {
		return err
	}
	w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// S3 pre-signed POST returns 204 on success.
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("s3 status %d: %s", resp.StatusCode, raw)
	}
	return nil
}

// notifyImageEndpoint POSTs the metadata token to PDB's image processor Lambda.
// It returns the final signed token (with origin+sizes) if the response contains one.
func notifyImageEndpoint(ctx context.Context, url, metadataToken string) (string, error) {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	_ = w.WriteField("metadata", metadataToken)
	w.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("imageEndpoint status %d: %s", resp.StatusCode, raw)
	}

	// Response shape is unknown — try a few candidate fields.
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err == nil {
		for _, k := range []string{"metadata", "token", "icon", "data"} {
			if v, ok := out[k]; ok {
				s := strings.Trim(string(v), `"`)
				if len(s) > 20 {
					return s, nil
				}
			}
		}
	}
	// Maybe the response body itself is the token string.
	s := strings.TrimSpace(string(raw))
	if len(s) > 20 && !strings.HasPrefix(s, "{") {
		return s, nil
	}
	return "", fmt.Errorf("unrecognised imageEndpoint response: %s", raw)
}
