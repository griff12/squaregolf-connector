package camera

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/brentyates/squaregolf-connector/internal/core"
)

// SwingCamVendor talks to the SwingCam HTTP REST API. It is one implementation
// of Vendor; other camera systems implement Vendor in their own files.
type SwingCamVendor struct {
	mu         sync.Mutex
	baseURL    string
	httpClient *http.Client
}

// NewSwingCamVendor creates a SwingCam vendor pointed at baseURL.
func NewSwingCamVendor(baseURL string) *SwingCamVendor {
	if baseURL == "" {
		baseURL = "http://localhost:5000"
	}
	return &SwingCamVendor{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *SwingCamVendor) Name() string { return "SwingCam" }

func (v *SwingCamVendor) SetBaseURL(baseURL string) {
	if baseURL == "" {
		baseURL = "http://localhost:5000"
	}
	v.mu.Lock()
	v.baseURL = baseURL
	v.mu.Unlock()
}

func (v *SwingCamVendor) url() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.baseURL
}

func (v *SwingCamVendor) Arm() error {
	resp, err := v.httpClient.Post(fmt.Sprintf("%s/api/lm/arm", v.url()), "application/json", nil)
	if err != nil {
		return fmt.Errorf("arm request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("arm request returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (v *SwingCamVendor) ShotDetected(ball *core.BallMetrics) (string, error) {
	payloadBytes, err := json.Marshal(convertBallMetrics(ball))
	if err != nil {
		return "", fmt.Errorf("marshal ball data: %w", err)
	}

	resp, err := v.httpClient.Post(fmt.Sprintf("%s/api/lm/shot-detected", v.url()), "application/json", bytes.NewBuffer(payloadBytes))
	if err != nil {
		return "", fmt.Errorf("shot-detected request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("shot-detected request returned %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil
	}
	var shotResponse ShotResponse
	if err := json.Unmarshal(body, &shotResponse); err != nil {
		return "", nil
	}
	return shotResponse.Filename, nil
}

func (v *SwingCamVendor) UpdateMetadata(filename string, club *core.ClubMetrics, clubName string) error {
	clubData := convertClubMetrics(club)
	if clubData == nil {
		return fmt.Errorf("no club metrics to send")
	}
	if clubName != "" {
		clubData.ClubType = clubName
	}

	payloadBytes, err := json.Marshal(clubData)
	if err != nil {
		return fmt.Errorf("marshal club data: %w", err)
	}

	url := fmt.Sprintf("%s/api/recordings/%s/metadata", v.url(), filename)
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return fmt.Errorf("create metadata request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := v.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("metadata update request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("metadata update returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (v *SwingCamVendor) Cancel() error {
	resp, err := v.httpClient.Post(fmt.Sprintf("%s/api/lm/cancel", v.url()), "application/json", nil)
	if err != nil {
		return fmt.Errorf("cancel request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cancel request returned %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
