package main

import (
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	BaseUrl string = "https://billing.api.cloud.yandex.net/billing/v1/billingAccounts/"
)

type ycBillingResponse struct {
	CreatedAt   time.Time `json:"createdAt"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CountryCode string    `json:"countryCode"`
	Currency    string    `json:"currency"`
	Balance     string    `json:"balance"`
	Active      bool      `json:"active"`
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	logger.Info("Yandex cloud billing exporter is running...")

	serviceAccountID, ok := os.LookupEnv("SERVICE_ACCOUNT_ID")
	if !ok {
		slog.Error("SERVICE_ACCOUNT_ID not set")
		os.Exit(1)
	}

	keyID, ok := os.LookupEnv("KEY_ID")
	if !ok {
		slog.Error("KEY_ID not set")
		os.Exit(1)
	}

	secretKeyPath, ok := os.LookupEnv("SECRET_KEY_PATH")
	if !ok {
		slog.Error("SECRET_KEY_PATH not set")
		os.Exit(1)
	}

	ycBillingId, ok := os.LookupEnv("YC_BILLING_ID")
	if !ok {
		slog.Error("YC_BILLING_ID not set")
		os.Exit(1)
	}

	go recordMetrics(serviceAccountID, keyID, secretKeyPath, ycBillingId)

	srv := &http.Server{
		Addr:    ":2112",
		Handler: promhttp.Handler(),
	}

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("listen: %s\n", err)
		}
	}()
	slog.Info("Server Started")

	<-done

	slog.Info("Http server stopped")
}

func recordMetrics(serviceAccountID string, keyID string, secretKeyPath string, ycBillingId string) {
	gauge := initMetrics()
	slog.Info("Record prometeus metric")
	for {
		getToken := exchangeJWTToIAM(serviceAccountID, keyID, secretKeyPath)
		bl, _ := getYandexCloudBilling(getToken, ycBillingId)
		gauge.Set(bl)
		time.Sleep(time.Hour * 1)
	}
}

func initMetrics() prometheus.Gauge {
	slog.Info("Build prometeus metric")
	return promauto.NewGauge(prometheus.GaugeOpts{
		Name: "yc_billing_balance",
		Help: "The total balance fo Yandex cloud account",
	})
}

func createJWTToken(serviceAccountID string, keyID string, keyFile string) string {
	claims := jwt.RegisteredClaims{
		Issuer:    serviceAccountID,
		ExpiresAt: jwt.NewNumericDate(time.Now().UTC().Add(1 * time.Hour)),
		IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
		NotBefore: jwt.NewNumericDate(time.Now().UTC()),
		Audience:  []string{"https://iam.api.cloud.yandex.net/iam/v1/tokens"},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodPS256, claims)
	token.Header["kid"] = keyID

	privateKey := loadPrivateKey(keyFile)
	signed, err := token.SignedString(privateKey)
	if err != nil {
		slog.Error("Error get JWT token: %s\n", err)
	}

	return signed
}

func loadPrivateKey(keyFile string) *rsa.PrivateKey {
	data, err := os.ReadFile(keyFile)
	if err != nil {
		slog.Error("Can't read privatekey file: %s\n", err)
	}
	rsaPrivateKey, err := jwt.ParseRSAPrivateKeyFromPEM(data)
	if err != nil {
		slog.Error("Can't parse privatekey file: %s\n", err)
	}
	return rsaPrivateKey
}

func exchangeJWTToIAM(serviceAccountID string, keyID string, keyFile string) string {
	jot := createJWTToken(serviceAccountID, keyID, keyFile)
	resp, err := http.Post(
		"https://iam.api.cloud.yandex.net/iam/v1/tokens",
		"application/json",
		strings.NewReader(fmt.Sprintf(`{"jwt":"%s"}`, jot)),
	)
	if err != nil {
		slog.Error("Can't make request to IAM API: %s\n", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		panic(fmt.Sprintf("%s: %s", resp.Status, body))
	}
	var data struct {
		IAMToken string `json:"iamToken"`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		slog.Error("Can't decode json from IAM API request: %s\n", err)
	}

	return data.IAMToken
}

func getYandexCloudBilling(iamToken string, ycBillingId string) (float64, error) {
	client := &http.Client{}
	ycMetrics := ycBillingResponse{}

	URL := BaseUrl + ycBillingId

	slog.Info("Trying get info about balance of Yandex cloud")
	req, err := http.NewRequest(http.MethodGet, URL, nil)

	slog.Info("Try authorize at Yandex cloud")
	if err != nil {
		slog.Error("Can't make auth request")
		return 0, err
	}
	req.Header.Add("Authorization", "Bearer "+iamToken)

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Can't get response")
		return 0, err
	}
	defer resp.Body.Close()
	temp, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("Can't read response body")
		return 0, err
	}

	if err := json.Unmarshal(temp, &ycMetrics); err != nil {
		slog.Error("Can't make unmarshal json")
		return 0, err
	}

	flBalance, err := strconv.ParseFloat(ycMetrics.Balance, 64)
	if err != nil {
		slog.Error("Can't convert string to float64")
	}
	slog.Info("Received value of balance of Yandex cloud")

	return flBalance, nil
}
