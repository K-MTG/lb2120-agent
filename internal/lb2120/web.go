package lb2120

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var reToken = regexp.MustCompile(`name="token"\s+value="([^"]*)"`)

// Model is the subset of the LB2120's /api/model.json response used for
// monitoring. Unknown fields are ignored by encoding/json.
type Model struct {
	General struct {
		Manufacturer    string `json:"manufacturer"`
		Model           string `json:"model"`
		FWversion       string `json:"FWversion"`
		HWversion       string `json:"HWversion"`
		IMEI            string `json:"IMEI"`
		SystemAlertList []struct {
			Description string `json:"description"`
			Active      string `json:"active"`
			Type        string `json:"type"`
			Timestamp   string `json:"timestamp"`
		} `json:"systemAlertList"`
	} `json:"general"`
	Power struct {
		PMState     string `json:"PMState"`
		SmState     string `json:"SmState"`
		ResetReason int    `json:"resetreason"`
	} `json:"power"`
	WWAN struct {
		Connection             string `json:"connection"`
		CurrentNWserviceType   string `json:"currentNWserviceType"`
		CurrentPSserviceType   string `json:"currentPSserviceType"`
		NetScanStatus          string `json:"netScanStatus"`
		IP                     string `json:"IP"`
		RegisterNetworkDisplay string `json:"registerNetworkDisplay"`
		Roaming                bool   `json:"roaming"`
		SignalStrength         struct {
			RSSI float64 `json:"rssi"`
			RSRP float64 `json:"rsrp"`
			RSRQ float64 `json:"rsrq"`
			SINR float64 `json:"sinr"`
			Bars float64 `json:"bars"`
		} `json:"signalStrength"`
	} `json:"wwan"`
	SIM struct {
		Status string `json:"status"`
	} `json:"sim"`
	Custom struct {
		AtTcpEnable bool `json:"AtTcpEnable"`
	} `json:"custom"`
}

// hasActiveWWANDisconnectedAlert reports whether the device's own alert list
// currently contains an active WWANdisconnected entry.
func (m *Model) HasActiveWWANDisconnectedAlert() bool {
	for _, a := range m.General.SystemAlertList {
		if a.Type == "WWANdisconnected" && a.Active == "true" {
			return true
		}
	}
	return false
}

// WebClient logs into the LB2120 web UI and fetches model.json. Each call to
// FetchModel performs a fresh login: sessions were observed to expire within
// a few minutes, so there is no benefit to trying to keep one alive across
// poll cycles.
type WebClient struct {
	BaseURL    string // e.g. http://192.168.1.1
	Password   string
	HTTPClient *http.Client
}

func NewWebClient(baseURL, password string, timeout time.Duration) (*WebClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &WebClient{
		BaseURL:  baseURL,
		Password: password,
		HTTPClient: &http.Client{
			Jar:     jar,
			Timeout: timeout,
		},
	}, nil
}

// login fetches the CSRF token and posts the admin password. It returns an
// error if the device rejects the login (e.g. wrong password).
func (c *WebClient) login() error {
	resp, err := c.HTTPClient.Get(c.BaseURL + "/index.html")
	if err != nil {
		return fmt.Errorf("fetch login page: %w", err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read login page: %w", err)
	}

	m := reToken.FindSubmatch(body)
	if m == nil {
		return fmt.Errorf("could not find CSRF token on login page")
	}
	token := string(m[1])

	form := url.Values{
		"session.password": {c.Password},
		"token":            {token},
		"ok_redirect":      {"/index.html"},
		"err_redirect":     {"/index.html"},
	}

	// Don't auto-follow this redirect: the Location header's errno query
	// param tells us whether the login was accepted.
	noRedirect := *c.HTTPClient
	noRedirect.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}

	loginResp, err := noRedirect.PostForm(c.BaseURL+"/Forms/config", form)
	if err != nil {
		return fmt.Errorf("post login: %w", err)
	}
	loc := loginResp.Header.Get("Location")
	loginResp.Body.Close()

	if strings.Contains(loc, "errno=") {
		return fmt.Errorf("login rejected by device: redirected to %s", loc)
	}
	return nil
}

// FetchModel logs in and retrieves the current model.json snapshot.
func (c *WebClient) FetchModel() (*Model, error) {
	if err := c.login(); err != nil {
		return nil, err
	}

	apiURL := fmt.Sprintf("%s/api/model.json?internalapi=1&x=%d", c.BaseURL, rand.Intn(100000))
	resp, err := c.HTTPClient.Get(apiURL)
	if err != nil {
		return nil, fmt.Errorf("fetch model.json: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read model.json: %w", err)
	}

	var model Model
	if err := json.Unmarshal(body, &model); err != nil {
		return nil, fmt.Errorf("parse model.json (session likely not authenticated): %w", err)
	}
	return &model, nil
}
