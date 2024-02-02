package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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

func recordMetrics(ctx context.Context, oAuthToken string, ycBillingId string) {
	gauge := initMetrics()

	go func() {
		for {
			select {
			case <-ctx.Done():
				// Context was cancelled or deadline exceeded, exit the goroutine
				return
			default:
				bl := getYandexCloudBilling(getIAMToken(oAuthToken), ycBillingId)
				gauge.Set(bl)
				time.Sleep(time.Hour * 1)
			}
		}
	}()
}

func initMetrics() prometheus.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Name: "yc_billing_balance",
		Help: "The total balance fo Yandex cloud account",
	})
}

func getYandexCloudBilling(iamToken string, ycBillingId string) float64 {
	client := &http.Client{}
	ycMetrics := ycBillingResponse{}

	URL := BaseUrl + ycBillingId

	req, err := http.NewRequest(http.MethodGet, URL, nil)

	if err != nil {
		slog.Error("Can't make request")
	}
	req.Header.Add("Authorization", "Bearer "+iamToken)

	resp, err := client.Do(req)
	if err != nil {
		slog.Error("Can't get response")
	}
	defer resp.Body.Close()
	temp, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Error("Can't read response body")
	}

	if err := json.Unmarshal(temp, &ycMetrics); err != nil {
		slog.Error("Can't make unmarshal json")
	}
	flBalance, err := strconv.ParseFloat(ycMetrics.Balance, 64)
	if err != nil {
		//log.Fatal("Can't convert string to float64")
		slog.Error("Can't convert string to float64")
	}
	return flBalance
}

func getIAMToken(oAuthToken string) string {
	resp, err := http.Post(
		"https://iam.api.cloud.yandex.net/iam/v1/tokens",
		"application/json",
		strings.NewReader(fmt.Sprintf(`{"yandexPassportOauthToken":"%s"}`, oAuthToken)),
	)
	if err != nil {
		slog.Error("Can't make request")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		slog.Error("HTTP request err", resp.Status, body)
	}
	var data struct {
		IAMToken string `json:"iamToken"`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	if err != nil {
		slog.Error("Can't decode response from IAM yandex cloud API")
	}

	return data.IAMToken

}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	logger.Info("Yandex cloud billing exporter is running...")

	oAuthToken, ok := os.LookupEnv("TOKEN")
	if !ok {
		slog.Error("oAuthToken not set")
		os.Exit(1)
	}

	ycBillingId, ok := os.LookupEnv("YCBILLINGID")
	if !ok {
		slog.Error("YCBILLINGID not set")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	recordMetrics(ctx, oAuthToken, ycBillingId)

	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(":2112", nil)
}
