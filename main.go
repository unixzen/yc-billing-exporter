package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/maypok86/otter"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	BASE_URL string = "https://billing.api.cloud.yandex.net/billing/v1/billingAccounts/"
)

type ycBillingResponse struct {
	Active      bool      `json:"active"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"createdAt"`
	CountryCode string    `json:"countryCode"`
	Currency    string    `json:"currency"`
	Balance     string    `json:"balance"`
}

func recordMetrics(oAuthToken string, ycBillingId string) {
	gauge := initMetrics()

	bl, _ := getYandexCloudBilling(getIAMToken(oAuthToken), ycBillingId)

	go func() {
		for {
			gauge.Set(bl)
		}
	}()
}

func initMetrics() prometheus.Gauge {
	return promauto.NewGauge(prometheus.GaugeOpts{
		Name: "yc_billing_balance",
		Help: "The total balance fo Yandex cloud account",
	})
}

func getYandexCloudBilling(iamToken string, ycBillingId string) (float64, error) {
	client := &http.Client{}
	ycMetrics := ycBillingResponse{}

	URL := BASE_URL + ycBillingId

	req, err := http.NewRequest("GET", URL, nil)
	if err != nil {
		return 0.0, err
	}
	req.Header.Add("Authorization", "Bearer "+iamToken)

	resp, err := client.Do(req)
	if err != nil {
		return 0.0, err
	}

	temp, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0.0, err
	}

	if err := json.Unmarshal(temp, &ycMetrics); err != nil {
		return 0.0, err
	}
	flBalance, err := strconv.ParseFloat(ycMetrics.Balance, 64)
	if err != nil {
		log.Fatal("Can't convert string to float64")
	}
	return flBalance, nil
}

func getIAMToken(oAuthToken string) string {

	cache, err := otter.MustBuilder[string, string](100).
		CollectStats().
		Cost(func(iamtoken string, value string) uint32 {
			return 1
		}).
		WithTTL(time.Hour).
		Build()
	if err != nil {
		slog.Error("Can't create cache for IAM token")
	}

	iamToken, ok := cache.Get("iamtoken")

	if !ok {
		slog.Info("IAM token not found at cache. Try to request IAM token...")
		resp, err := http.Post(
			"https://iam.api.cloud.yandex.net/iam/v1/tokens",
			"application/json",
			strings.NewReader(fmt.Sprintf(`{"yandexPassportOauthToken":"%s"}`, oAuthToken)),
		)
		if err != nil {
			panic(err)
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
			panic(err)
		}

		cache.Set("iamtoken", data.IAMToken)
		slog.Info("IAM token added to cache")

		iamToken, ok := cache.Get("iamtoken")

		if !ok {
			slog.Error("Can't get IAM token from cache")
			os.Exit(1)
		}

		slog.Info("IAM token succesfully installed")

		return iamToken
		//return data.IAMToken

	}

	return iamToken
	//return data.IAMToken
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

	recordMetrics(oAuthToken, ycBillingId)

	http.Handle("/metrics", promhttp.Handler())
	http.ListenAndServe(":2112", nil)
}
