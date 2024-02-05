package main

import (
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

	go recordMetrics(oAuthToken, ycBillingId)

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

func recordMetrics(oAuthToken string, ycBillingId string) {
	gauge := initMetrics()
	slog.Info("Record prometeus metric")
	for {
		getToken, _ := getIAMToken(oAuthToken)
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

func getIAMToken(oAuthToken string) (string, error) {
	slog.Info("Getting IAM token...")
	resp, err := http.Post(
		"https://iam.api.cloud.yandex.net/iam/v1/tokens",
		"application/json",
		strings.NewReader(fmt.Sprintf(`{"yandexPassportOauthToken":"%s"}`, oAuthToken)),
	)
	if err != nil {
		slog.Error("Can't make request")
		return "", err
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
		return "", err
	}

	slog.Info("IAM token received")
	return data.IAMToken, nil

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
		//log.Fatal("Can't convert string to float64")
		slog.Error("Can't convert string to float64")
	}
	slog.Info("Received value of balance of Yandex cloud")

	return flBalance, nil
}
